package gmail

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/rfc822"
	mdmail "github.com/vincentsch/mailmd/internal/mail"
)

const (
	defaultFetchLimit   = mdbackend.DefaultFetchLimit
	maxProviderPageSize = 500
)

// fetchedMessage is a message together with the values it is sorted by,
// until the FetchResult is built.
type fetchedMessage struct {
	rawBytes     int64
	id           string
	internalDate int64
	message      mdmail.Message
}

// fetchedDiagnostic is the same for a diagnostic.
type fetchedDiagnostic struct {
	rawBytes     int64
	id           string
	internalDate int64
	diagnostic   mdbackend.Diagnostic
}

// fetch does the work of Fetch, after Fetch has checked the request and
// decoded the cursor.
func (b *Backend) fetch(ctx context.Context, req mdbackend.FetchRequest, state *cursorState) (mdbackend.FetchResult, error) {
	if err := ctx.Err(); err != nil {
		return mdbackend.FetchResult{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultFetchLimit
	}

	profile, err := b.client.Profile(ctx, b.userID)
	if err != nil {
		return mdbackend.FetchResult{}, classifyAPIError(err)
	}
	fp, err := accountFingerprint(profile)
	if err != nil {
		return mdbackend.FetchResult{}, err
	}
	if err := validateProfileHistory(profile); err != nil {
		return mdbackend.FetchResult{}, err
	}
	if state != nil {
		if err := state.validateFingerprint(fp); err != nil {
			return mdbackend.FetchResult{}, err
		}
	}

	labels, err := b.client.Labels(ctx, b.userID)
	if err != nil {
		return mdbackend.FetchResult{}, classifyAPIError(err)
	}
	catalog := buildLabelCatalog(labels)

	if state == nil {
		return b.fetchEnumeration(ctx, fp, profile.HistoryID, "", nil, true, catalog, limit)
	}

	switch state.Mode {
	case modeReady:
		if profile.HistoryID == state.HistoryID {
			return mdbackend.FetchResult{Cursor: encodeReadyCursor(fp, profile.HistoryID), Done: true}, nil
		}
		return b.fetchHistory(ctx, fp, state.HistoryID, profile.HistoryID, "", nil, nil, true, catalog, limit)
	case modeEnumerate:
		return b.fetchEnumeration(ctx, fp, state.TargetHistoryID, state.PageToken, state.PendingIDs, false, catalog, limit)
	case modeHistory:
		return b.fetchHistory(ctx, fp, state.BaseHistoryID, state.TargetHistoryID, state.PageToken, state.PendingIDs, state.PendingDeletedIDs, false, catalog, limit)
	default:
		return mdbackend.FetchResult{}, invalidCursor("unknown mode")
	}
}

// fetchEnumeration lists the account's messages and fetches up to limit of
// them. Until the listing is done, the cursor continues it. After that, the
// cursor holds targetHistoryID, and the next sync asks for the changes since
// then.
func (b *Backend) fetchEnumeration(ctx context.Context, fp [32]byte, targetHistoryID, pageToken string, pending []string, forceList bool, catalog labelCatalog, limit int) (mdbackend.FetchResult, error) {
	pending = sortedUniqueNonEmpty(pending)
	for (forceList || pageToken != "") && len(pending) < limit {
		forceList = false
		req := requestBudget(ctx)
		query := ""
		if !req.Since.IsZero() {
			// Gmail reads a date like 2026/01/31 as midnight in California,
			// so mailmd sends seconds since 1970. It asks for one second
			// earlier, so a message received at midnight is included.
			query = "after:" + strconv.FormatInt(req.Since.Unix()-1, 10)
		}
		page, err := b.client.ListMessages(ctx, b.userID, pageToken, !req.SkipJunk, query, providerPageSize(limit-len(pending)))
		if err != nil {
			if isInvalidPageTokenError(err) && pageToken != "" {
				// A page token can expire while the history ID is still
				// fine. Start the listing again with a new target, so the
				// cursor never saves a position from a broken chain of pages.
				historyID, err := b.captureFreshProfileHistory(ctx, fp)
				if err != nil {
					return mdbackend.FetchResult{}, err
				}
				targetHistoryID = historyID
				pageToken = ""
				pending = nil
				forceList = true
				continue
			}
			return mdbackend.FetchResult{}, classifyAPIError(err)
		}
		for _, ref := range page.Messages {
			if ref.ID != "" {
				if !validPendingID(ref.ID) {
					return mdbackend.FetchResult{}, mdbackend.ErrProviderStateLimit
				}
				pending = append(pending, ref.ID)
			}
		}
		pending = sortedUniqueNonEmpty(pending)
		if len(page.NextPageToken) > maxPageTokenBytes {
			return mdbackend.FetchResult{}, mdbackend.ErrProviderStateLimit
		}
		pageToken = page.NextPageToken
		if pageToken == "" {
			break
		}
	}

	messages, diagnostics, remaining, err := b.fetchPendingMessages(ctx, pending, catalog, limit)
	if err != nil {
		return mdbackend.FetchResult{}, err
	}
	return finishEnumeration(fp, targetHistoryID, pageToken, remaining, messages, diagnostics)
}

// finishEnumeration saves the target history ID as the new position only
// when no page and no pending message is left.
func finishEnumeration(fp [32]byte, targetHistoryID, pageToken string, pending []string, messages []fetchedMessage, diagnostics []fetchedDiagnostic) (mdbackend.FetchResult, error) {
	sortFetched(messages, diagnostics)
	result := materializeFetchResult(messages, diagnostics)
	if len(pending) == 0 && pageToken == "" {
		result.Cursor = encodeReadyCursor(fp, targetHistoryID)
		result.Done = true
		return result, nil
	}
	var err error
	result.Cursor, err = persistableCursor(cursorState{Mode: modeEnumerate, Fingerprint: fp, TargetHistoryID: targetHistoryID, PageToken: pageToken, PendingIDs: sortedUniqueNonEmpty(pending)})
	if err != nil {
		return mdbackend.FetchResult{}, err
	}
	result.Done = false
	return result, nil
}

// fetchHistory reads one page of Gmail's history per call, then fetches
// pending messages up to limit. With one page per call, a long history of
// only deletions or repeated IDs still saves its progress after each page.
func (b *Backend) fetchHistory(ctx context.Context, fp [32]byte, baseHistoryID, targetHistoryID, pageToken string, pending, deleted []string, forceList bool, catalog labelCatalog, limit int) (mdbackend.FetchResult, error) {
	pending = sortedUniqueNonEmpty(pending)
	deleted = sortedUniqueNonEmpty(deleted)
	diagnostics := []fetchedDiagnostic{}
	pagesFetched := 0
	for (forceList || pageToken != "") && len(pending)+len(deleted) < limit && pagesFetched == 0 {
		forceList = false
		page, err := b.client.ListHistory(ctx, b.userID, baseHistoryID, pageToken, providerPageSize(maxProviderPageSize))
		if err != nil {
			switch {
			case isStaleHistoryError(err):
				return b.restartEnumeration(ctx, fp, catalog, limit)
			case isInvalidPageTokenError(err) && pageToken != "":
				// Start the history listing again from the saved base. If
				// Gmail no longer has that base, the next call fails as stale,
				// and mailmd lists all messages again.
				pageToken = ""
				forceList = true
				continue
			default:
				return mdbackend.FetchResult{}, classifyAPIError(err)
			}
		}
		pagesFetched++
		for _, rec := range page.History {
			for _, ref := range rec.MessagesAdded {
				if ref.ID != "" {
					if !validPendingID(ref.ID) {
						return mdbackend.FetchResult{}, mdbackend.ErrProviderStateLimit
					}
					pending = append(pending, ref.ID)
				}
			}
			for _, ref := range rec.LabelsAdded {
				if ref.ID != "" {
					if !validPendingID(ref.ID) {
						return mdbackend.FetchResult{}, mdbackend.ErrProviderStateLimit
					}
					pending = append(pending, ref.ID)
				}
			}
			for _, ref := range rec.LabelsRemoved {
				if ref.ID != "" {
					if !validPendingID(ref.ID) {
						return mdbackend.FetchResult{}, mdbackend.ErrProviderStateLimit
					}
					pending = append(pending, ref.ID)
				}
			}
			for _, ref := range rec.MessagesDeleted {
				refID := ref.ID
				if refID == "" {
					refID = "history:" + rec.ID
				}
				if !validPendingID(refID) {
					return mdbackend.FetchResult{}, mdbackend.ErrProviderStateLimit
				}
				deleted = append(deleted, refID)
			}
		}
		pending = sortedUniqueNonEmpty(pending)
		if len(page.NextPageToken) > maxPageTokenBytes {
			return mdbackend.FetchResult{}, mdbackend.ErrProviderStateLimit
		}
		pageToken = page.NextPageToken
		if pageToken == "" {
			break
		}
	}

	deleted = sortedUniqueNonEmpty(deleted)
	// A message that was added or relabeled and then deleted gets one
	// diagnostic for the deletion. mailmd doesn't also try to fetch it.
	deletedSet := make(map[string]bool, len(deleted))
	for _, id := range deleted {
		deletedSet[id] = true
	}
	livePending := pending[:0]
	for _, id := range pending {
		if !deletedSet[id] {
			livePending = append(livePending, id)
		}
	}
	pending = livePending
	take := min(len(deleted), limit)
	for _, id := range deleted[:take] {
		diagnostics = append(diagnostics, fetchedDiagnostic{id: id, diagnostic: diagnosticForMessage(accountScopeKey, b.Name(), id, mdbackend.ReasonRemoteRemoved)})
	}
	remainingDeleted := deleted[take:]
	messages, messageDiagnostics, remaining, err := b.fetchPendingMessages(ctx, pending, catalog, limit-take)
	if err != nil {
		// A message is too large to fetch. Return the deletions found so far,
		// and leave the pending messages for the next call.
		if take > 0 && errors.Is(err, mdbackend.ErrMessageTooLarge) {
			remaining = pending
		} else {
			return mdbackend.FetchResult{}, err
		}
	}
	diagnostics = append(diagnostics, messageDiagnostics...)
	sortFetched(messages, diagnostics)
	result := materializeFetchResult(messages, diagnostics)
	if len(remaining) == 0 && len(remainingDeleted) == 0 && pageToken == "" {
		result.Cursor = encodeReadyCursor(fp, targetHistoryID)
		result.Done = true
		return result, nil
	}
	result.Cursor, err = persistableCursor(cursorState{Mode: modeHistory, Fingerprint: fp, BaseHistoryID: baseHistoryID, TargetHistoryID: targetHistoryID, PageToken: pageToken, PendingIDs: sortedUniqueNonEmpty(remaining), PendingDeletedIDs: remainingDeleted})
	if err != nil {
		return mdbackend.FetchResult{}, err
	}
	result.Done = false
	return result, nil
}

// restartEnumeration lists all messages again when Gmail no longer has the
// saved history ID. The new target is the account's current history ID.
func (b *Backend) restartEnumeration(ctx context.Context, fp [32]byte, catalog labelCatalog, limit int) (mdbackend.FetchResult, error) {
	historyID, err := b.captureFreshProfileHistory(ctx, fp)
	if err != nil {
		return mdbackend.FetchResult{}, err
	}
	return b.fetchEnumeration(ctx, fp, historyID, "", nil, true, catalog, limit)
}

// captureFreshProfileHistory returns the account's current history ID. It
// also checks that the login still belongs to the account of the cursor.
func (b *Backend) captureFreshProfileHistory(ctx context.Context, expectedFP [32]byte) (string, error) {
	profile, err := b.client.Profile(ctx, b.userID)
	if err != nil {
		return "", classifyAPIError(err)
	}
	fp, err := accountFingerprint(profile)
	if err != nil {
		return "", err
	}
	if fp != expectedFP {
		return "", invalidCursor("account fingerprint mismatch")
	}
	if err := validateProfileHistory(profile); err != nil {
		return "", err
	}
	return profile.HistoryID, nil
}

// headersOnly builds a message from the headers of a message that is larger
// than the size limit.
func (b *Backend) headersOnly(ctx context.Context, id string, size int64, catalog labelCatalog) (mdmail.Message, error) {
	client, ok := b.client.(interface {
		MessageHeaders(context.Context, string, string) (gmailMessage, []gmailHeader, error)
	})
	if !ok {
		return mdmail.Message{}, &mdbackend.MessageTooLargeError{Backend: b.Name(), ScopeKey: accountScopeKey, Ref: id, ObservedBytes: size}
	}
	gm, headers, err := client.MessageHeaders(ctx, b.userID, id)
	if err != nil {
		return mdmail.Message{}, classifyAPIError(err)
	}
	var block strings.Builder
	for _, h := range headers {
		if h.Name != "" && !strings.ContainsAny(h.Name+h.Value, "\r\n") {
			block.WriteString(h.Name + ": " + h.Value + "\r\n")
		}
	}
	block.WriteString("\r\n")
	msg, err := rfc822.NormalizeHeaders([]byte(block.String()), size)
	if err != nil {
		msg = rfc822.Unreadable([]byte(block.String()), err)
		msg.Raw, msg.ParseError, msg.OmittedSize = nil, "", size
	}
	var folder string
	folder, msg.Labels, msg.Flags = catalog.labelsAndFlags(gm.LabelIDs)
	msg.Folders, msg.Ref, msg.ThreadID = []string{folder}, id, gm.ThreadID
	if gm.InternalDate > 0 {
		msg.Received = time.UnixMilli(gm.InternalDate)
	}
	return msg, nil
}

// fetchPendingMessages fetches up to limit of the pending messages and
// returns the IDs that are left. Messages that vanished or are too large
// become diagnostics. Any other error stops the batch, so the caller never
// saves a cursor past messages that it didn't report.
func (b *Backend) fetchPendingMessages(ctx context.Context, pending []string, catalog labelCatalog, limit int) ([]fetchedMessage, []fetchedDiagnostic, []string, error) {
	pending = sortedUniqueNonEmpty(pending)
	if len(pending) == 0 || limit <= 0 {
		return nil, nil, pending, nil
	}
	take := limit
	if take > len(pending) {
		take = len(pending)
	}
	selected := append([]string(nil), pending[:take]...)
	remaining := append([]string(nil), pending[take:]...)
	messages := make([]fetchedMessage, 0, len(selected))
	diagnostics := []fetchedDiagnostic{}
	limits := requestBudget(ctx)
	var used int64
	for index, id := range selected {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		// tooLarge saves only the headers, so one huge message doesn't block
		// the account.
		tooLarge := func(size int64) error {
			stub, err := b.headersOnly(ctx, id, size, catalog)
			if isVanishedMessageError(err) {
				diagnostics = append(diagnostics, fetchedDiagnostic{id: id, diagnostic: diagnosticForMessage(accountScopeKey, b.Name(), id, mdbackend.ReasonVanished)})
				return nil
			}
			if err != nil {
				return err
			}
			messages = append(messages, fetchedMessage{id: id, internalDate: stub.Received.UnixMilli(), message: stub})
			diagnostics = append(diagnostics, fetchedDiagnostic{id: id, internalDate: stub.Received.UnixMilli(), diagnostic: diagnosticForMessage(accountScopeKey, b.Name(), id, mdbackend.ReasonTooLarge)})
			return nil
		}
		if preflight, ok := b.client.(interface {
			MessageSize(context.Context, string, string) (int64, error)
		}); ok {
			size, err := preflight.MessageSize(ctx, b.userID, id)
			if err != nil && !isVanishedMessageError(err) {
				return nil, nil, nil, classifyAPIError(err)
			}
			if size > limits.MaxMessageBytes {
				if err := tooLarge(size); err != nil {
					return nil, nil, nil, err
				}
				continue
			}
			if size > limits.MaxBatchBytes-used && index > 0 {
				return messages, diagnostics, pending[index:], nil
			}
		}
		gm, err := b.client.GetMessage(ctx, b.userID, id)
		if err != nil {
			if errors.Is(err, mdbackend.ErrMessageTooLarge) {
				if err := tooLarge(limits.MaxMessageBytes + 1); err != nil {
					return nil, nil, nil, err
				}
				continue
			}
			if isVanishedMessageError(err) {
				diagnostics = append(diagnostics, fetchedDiagnostic{
					id:         id,
					diagnostic: diagnosticForMessage(accountScopeKey, b.Name(), id, mdbackend.ReasonVanished),
				})
				continue
			}
			return nil, nil, nil, classifyAPIError(err)
		}
		counted := rawMessageBytes(gm.Raw, limits.MaxMessageBytes)
		if gm.SizeEstimate > limits.MaxMessageBytes || counted > limits.MaxMessageBytes {
			if err := tooLarge(max(gm.SizeEstimate, counted)); err != nil {
				return nil, nil, nil, err
			}
			continue
		}
		if counted > limits.MaxBatchBytes-used {
			return messages, diagnostics, pending[index:], nil
		}
		used += counted
		ref := gm.ID
		if ref == "" {
			ref = id
		}
		msg, err := normalizeGmailMessage(gm, catalog)
		if err != nil {
			// mailmd saves what it can read of the message.
			diagnostics = append(diagnostics, fetchedDiagnostic{
				id:           ref,
				internalDate: gm.InternalDate,
				diagnostic:   diagnosticForMessage(accountScopeKey, b.Name(), ref, diagnosticReasonForNormalizeError(err)),
			})
		}
		messages = append(messages, fetchedMessage{id: ref, internalDate: gm.InternalDate, message: msg, rawBytes: counted})
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	return messages, diagnostics, remaining, nil
}

func providerPageSize(want int) int64 {
	if want <= 0 || want > maxProviderPageSize {
		return maxProviderPageSize
	}
	return int64(want)
}

// sortedUniqueNonEmpty sorts the IDs and drops empty and repeated ones. It
// only sees this batch. The cursor doesn't remember IDs from earlier pages.
func sortedUniqueNonEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	out := values[:0]
	var last string
	for _, value := range values {
		if value == "" || value == last {
			continue
		}
		out = append(out, value)
		last = value
	}
	return append([]string(nil), out...)
}

// sortFetched puts messages and diagnostics in the order the sync expects:
// oldest first.
func sortFetched(messages []fetchedMessage, diagnostics []fetchedDiagnostic) {
	sort.Slice(messages, func(i, j int) bool {
		if messages[i].internalDate != messages[j].internalDate {
			return messages[i].internalDate < messages[j].internalDate
		}
		return messages[i].id < messages[j].id
	})
	sort.Slice(diagnostics, func(i, j int) bool {
		if diagnostics[i].internalDate != diagnostics[j].internalDate {
			return diagnostics[i].internalDate < diagnostics[j].internalDate
		}
		return diagnostics[i].id < diagnostics[j].id
	})
}

// materializeFetchResult drops the sort keys and adds up the raw bytes.
func materializeFetchResult(messages []fetchedMessage, diagnostics []fetchedDiagnostic) mdbackend.FetchResult {
	result := mdbackend.FetchResult{
		Messages:    make([]mdmail.Message, 0, len(messages)),
		Diagnostics: make([]mdbackend.Diagnostic, 0, len(diagnostics)),
	}
	for _, msg := range messages {
		result.RawBytes += msg.rawBytes
		result.Messages = append(result.Messages, msg.message)
	}
	for _, diag := range diagnostics {
		result.RawBytes += diag.rawBytes
		result.Diagnostics = append(result.Diagnostics, diag.diagnostic)
	}
	return result
}

// validateProfileHistory rejects a history ID that a cursor can't store.
func validateProfileHistory(profile gmailProfile) error {
	if !validHistoryID(profile.HistoryID) {
		return fmt.Errorf("gmail: invalid profile history id")
	}
	return nil
}

// FetchRefs fetches messages again by their Gmail IDs, like a message that
// was larger than the size limit before.
func (b *Backend) FetchRefs(ctx context.Context, req mdbackend.FetchRequest, refs []string) (mdbackend.FetchResult, error) {
	ctx = nonNilContext(ctx)
	if b == nil || b.isClosed() {
		return mdbackend.FetchResult{}, fmt.Errorf("gmail: fetch on closed backend: %w", mdbackend.ErrClosed)
	}
	req, err := req.WithDefaults()
	if err != nil {
		return mdbackend.FetchResult{}, err
	}
	if err := validateFetchScope(req.ScopeKey); err != nil {
		return mdbackend.FetchResult{}, err
	}
	ctx = context.WithValue(ctx, fetchBudgetKey{}, req)
	b.opMu.Lock()
	defer b.opMu.Unlock()
	labels, err := b.client.Labels(ctx, b.userID)
	if err != nil {
		return mdbackend.FetchResult{}, classifyAPIError(err)
	}
	catalog := buildLabelCatalog(labels)
	var result mdbackend.FetchResult
	for pending := sortedUniqueNonEmpty(append([]string(nil), refs...)); len(pending) > 0; {
		messages, diagnostics, remaining, err := b.fetchPendingMessages(ctx, pending, catalog, len(pending))
		if err != nil {
			return mdbackend.FetchResult{}, err
		}
		part := materializeFetchResult(messages, diagnostics)
		result.Messages = append(result.Messages, part.Messages...)
		result.Diagnostics = append(result.Diagnostics, part.Diagnostics...)
		result.RawBytes += part.RawBytes
		if len(remaining) >= len(pending) {
			return mdbackend.FetchResult{}, errors.New("gmail: no progress while fetching messages again")
		}
		pending = remaining
	}
	return result, nil
}
