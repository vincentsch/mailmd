package imap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/rfc822"
	"github.com/vincentsch/mailmd/internal/mail"
)

// headerLimit is how many bytes of headers mailmd reads for a message that
// is larger than the size limit.
const headerLimit = 1 << 20

// pendingUIDs remembers the UIDs a folder still has to fetch, so one SEARCH
// serves all batches of a sync. Gmail and other servers use sparse UIDs, so
// stepping through UID ranges would cost many round trips.
type pendingUIDs struct {
	validity uint32
	after    uint32
	since    time.Time
	uids     []imap.UID
}

// meta is what the first FETCH reports about a message.
type meta struct {
	size     int64
	flags    []imap.Flag
	received time.Time
}

// Fetch returns the next batch of messages of one folder. The cursor holds
// the folder's UIDVALIDITY and the last UID that was handled.
func (b *Backend) Fetch(ctx context.Context, req backend.FetchRequest) (backend.FetchResult, error) {
	req, err := req.WithDefaults()
	if err != nil {
		return backend.FetchResult{}, err
	}
	mailbox, delimiter, present, err := decodeScopeKey(req.ScopeKey)
	if err != nil {
		return backend.FetchResult{}, err
	}
	if !present {
		delimiter = ""
	}
	folder := displayPath(mailbox, delimiter)
	var pos cursor
	if len(req.Cursor) > 0 {
		if pos, err = decodeCursor(req.ScopeKey, req.Cursor); err != nil {
			return backend.FetchResult{}, err
		}
	}
	unlock, err := b.begin(ctx)
	if err != nil {
		return backend.FetchResult{}, err
	}
	defer unlock()

	b.conn.allow(1 << 20)
	selected, err := b.client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return backend.FetchResult{}, b.selectError(ctx, folder, err)
	}
	if selected.UIDValidity == 0 {
		return backend.FetchResult{}, &backend.Problem{
			Text: fmt.Sprintf("%s did not send a UIDVALIDITY for %s. Without it, mailmd can't tell which messages it already has.", b.host, folder),
			Err:  ErrProtocol,
		}
	}
	if pos.validity != selected.UIDValidity {
		// The server renumbered the folder. Start it over. Files that already
		// exist are left as they are.
		pos = cursor{validity: selected.UIDValidity}
	}

	uids, err := b.pendingFor(ctx, req.ScopeKey, folder, selected, pos.last, req.Since)
	if err != nil {
		return backend.FetchResult{}, err
	}
	if len(uids) == 0 {
		return backend.FetchResult{Cursor: encodeCursor(req.ScopeKey, pos), Done: true}, nil
	}
	batch := uids
	if len(batch) > req.Limit {
		batch = batch[:req.Limit]
	}
	result, n, err := b.download(ctx, req, folder, selected.UIDValidity, batch)
	if err != nil {
		return backend.FetchResult{}, err
	}
	batch = batch[:n]

	pos.last = uint32(batch[len(batch)-1])
	rest := uids[len(batch):]
	if len(rest) > 0 {
		b.pending[req.ScopeKey] = &pendingUIDs{validity: selected.UIDValidity, after: pos.last, since: req.Since, uids: rest}
	} else {
		delete(b.pending, req.ScopeKey)
	}
	result.Cursor = encodeCursor(req.ScopeKey, pos)
	result.Done = len(rest) == 0
	result.Remaining = len(rest)
	return result, nil
}

// FetchRefs fetches messages of one folder again by their refs, like a
// message that was larger than the size limit before.
func (b *Backend) FetchRefs(ctx context.Context, req backend.FetchRequest, refs []string) (backend.FetchResult, error) {
	req, err := req.WithDefaults()
	if err != nil {
		return backend.FetchResult{}, err
	}
	mailbox, delimiter, present, err := decodeScopeKey(req.ScopeKey)
	if err != nil {
		return backend.FetchResult{}, err
	}
	if !present {
		delimiter = ""
	}
	folder := displayPath(mailbox, delimiter)
	unlock, err := b.begin(ctx)
	if err != nil {
		return backend.FetchResult{}, err
	}
	defer unlock()

	b.conn.allow(1 << 20)
	selected, err := b.client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return backend.FetchResult{}, b.selectError(ctx, folder, err)
	}
	var result backend.FetchResult
	var uids []imap.UID
	for _, ref := range refs {
		validity, uid, ok := parseRef(ref)
		if !ok || validity != selected.UIDValidity {
			// The folder was renumbered since then, and the message has a new
			// ref.
			result.Diagnostics = append(result.Diagnostics, backend.Diagnostic{ScopeKey: req.ScopeKey, Ref: ref, Reason: backend.ReasonVanished, Backend: b.Name()})
			continue
		}
		uids = append(uids, uid)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	for len(uids) > 0 {
		part, n, err := b.download(ctx, req, folder, selected.UIDValidity, uids)
		if err != nil {
			return backend.FetchResult{}, err
		}
		result.Messages = append(result.Messages, part.Messages...)
		result.Diagnostics = append(result.Diagnostics, part.Diagnostics...)
		result.RawBytes += part.RawBytes
		uids = uids[n:]
	}
	return result, nil
}

// formatRef names a message by the folder's UIDVALIDITY and its UID, like
// "1729000000:412". The UIDVALIDITY changes when a server renumbers a
// folder, so an old ref never matches a new message.
func formatRef(validity uint32, uid imap.UID) string {
	return strconv.FormatUint(uint64(validity), 10) + ":" + strconv.FormatUint(uint64(uid), 10)
}

func parseRef(ref string) (uint32, imap.UID, bool) {
	v, u, ok := strings.Cut(ref, ":")
	validity, err1 := strconv.ParseUint(v, 10, 32)
	uid, err2 := strconv.ParseUint(u, 10, 32)
	if !ok || err1 != nil || err2 != nil || uid == 0 {
		return 0, 0, false
	}
	return uint32(validity), imap.UID(uid), true
}

// download fetches the messages of batch, in order, until the byte budget
// is used up. It returns how many UIDs of batch it handled, at least one.
// A message over the size limit gets its headers only, and a message that
// can't be read is saved with its headers and original bytes.
func (b *Backend) download(ctx context.Context, req backend.FetchRequest, folder string, validity uint32, batch []imap.UID) (backend.FetchResult, int, error) {
	metas, err := b.fetchMeta(ctx, folder, batch)
	if err != nil {
		return backend.FetchResult{}, 0, err
	}

	var accepted, tooLarge []imap.UID
	var total int64
	n := 0
choose:
	for _, uid := range batch {
		m, ok := metas[uid]
		switch {
		case !ok:
		case m.size > req.MaxMessageBytes:
			tooLarge = append(tooLarge, uid)
		case len(accepted) > 0 && total+m.size > req.MaxBatchBytes:
			break choose
		default:
			total += m.size
			accepted = append(accepted, uid)
		}
		n++
	}
	batch = batch[:n]

	bodies, oversized, err := b.fetchBodies(ctx, folder, accepted, total, req.MaxMessageBytes)
	if err != nil {
		return backend.FetchResult{}, 0, err
	}
	for uid, size := range oversized {
		m := metas[uid]
		m.size = size
		metas[uid] = m
		tooLarge = append(tooLarge, uid)
	}
	isTooLarge := map[imap.UID]bool{}
	for _, uid := range tooLarge {
		isTooLarge[uid] = true
	}
	vanished := map[imap.UID]bool{}
	var missing []imap.UID
	for _, uid := range accepted {
		if _, ok := bodies[uid]; !ok && oversized[uid] == 0 {
			missing = append(missing, uid)
		}
	}
	if len(missing) > 0 {
		// The server listed these messages a moment ago. If they still
		// exist, it failed to send them, and mailmd must not move past them.
		gone, err := b.vanished(ctx, folder, missing)
		if err != nil {
			return backend.FetchResult{}, 0, err
		}
		for _, uid := range missing {
			if !gone[uid] {
				return backend.FetchResult{}, 0, &backend.Problem{
					Text: fmt.Sprintf("%s listed message %d in %s but did not send its content.", b.host, uid, folder),
					Hint: "Run the sync again. If it fails again, run it with --verbose and report the output.",
					Err:  ErrProtocol,
				}
			}
			vanished[uid] = true
		}
	}
	headers, err := b.fetchHeaders(ctx, folder, tooLarge)
	if err != nil {
		return backend.FetchResult{}, 0, err
	}

	result := backend.FetchResult{}
	diagnose := func(uid imap.UID, reason backend.DiagnosticReason) {
		result.Diagnostics = append(result.Diagnostics, backend.Diagnostic{
			ScopeKey: req.ScopeKey, Ref: formatRef(validity, uid), Reason: reason, Backend: b.Name(),
		})
	}
	for _, uid := range batch {
		m, ok := metas[uid]
		if !ok || vanished[uid] {
			diagnose(uid, backend.ReasonVanished)
			continue
		}
		var msg mail.Message
		if isTooLarge[uid] {
			header, ok := headers[uid]
			if !ok {
				diagnose(uid, backend.ReasonVanished)
				continue
			}
			if msg, err = rfc822.NormalizeHeaders(header, m.size); err != nil {
				// Only the headers are saved, with what can be read of them.
				msg = rfc822.Unreadable(header, err)
				msg.Raw, msg.ParseError, msg.OmittedSize = nil, "", m.size
			}
			diagnose(uid, backend.ReasonTooLarge)
		} else {
			raw := bodies[uid]
			result.RawBytes += int64(len(raw))
			if msg, err = rfc822.Normalize(raw); err != nil {
				msg = rfc822.Unreadable(raw, err)
				diagnose(uid, reasonFor(err))
			}
			msg.Raw = raw
		}
		msg.Folders, msg.Flags, msg.Received = []string{folder}, normalizeFlags(m.flags), m.received
		msg.Ref = formatRef(validity, uid)
		result.Messages = append(result.Messages, msg)
	}
	return result, n, nil
}

func reasonFor(err error) backend.DiagnosticReason {
	if errors.Is(err, rfc822.ErrIdentity) {
		return backend.ReasonIdentityFailure
	}
	return backend.ReasonParseFailure
}

// selectError tells a folder that no longer exists apart from other
// failures, so the sync can mark it missing and leave its files alone.
func (b *Backend) selectError(ctx context.Context, folder string, err error) error {
	var imapErr *imap.Error
	if errors.As(err, &imapErr) {
		if imapErr.Code == imap.ResponseCodeNonExistent || (imapErr.Code == "" && looksMissing(imapErr.Text)) {
			return fmt.Errorf("imap: %s does not exist: %w", folder, backend.ErrScopeNotFound)
		}
	}
	return b.commandError(ctx, "open "+folder, err)
}

// looksMissing recognizes the usual wording of servers that report a
// missing mailbox without the NONEXISTENT response code.
func looksMissing(text string) bool {
	text = strings.ToLower(text)
	for _, s := range []string{"no such mailbox", "no such folder", "doesn't exist", "does not exist", "not found", "unknown mailbox", "nonexistent", "non-existent", "not selectable"} {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// pendingFor returns the UIDs after last that are still to be fetched, in
// ascending order.
func (b *Backend) pendingFor(ctx context.Context, scope, folder string, selected *imap.SelectData, last uint32, since time.Time) ([]imap.UID, error) {
	if p := b.pending[scope]; p != nil && p.validity == selected.UIDValidity && p.after == last && p.since.Equal(since) {
		return p.uids, nil
	}
	delete(b.pending, scope)
	if selected.NumMessages == 0 || (selected.UIDNext != 0 && uint32(selected.UIDNext) <= last+1) {
		return nil, nil
	}
	criteria := &imap.SearchCriteria{
		UID:   []imap.UIDSet{{imap.UIDRange{Start: imap.UID(last + 1)}}},
		Since: since,
	}
	b.conn.allow(64 << 20)
	data, err := b.client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, b.commandError(ctx, "search "+folder, err)
	}
	var uids []imap.UID
	if set, ok := data.All.(imap.UIDSet); ok {
		nums, ok := set.Nums()
		if !ok {
			return nil, &backend.Problem{Text: fmt.Sprintf("%s answered a search in %s with an open range.", b.host, folder), Err: ErrProtocol}
		}
		// "n:*" also matches the highest UID when it is below n.
		for _, uid := range nums {
			if uint32(uid) > last {
				uids = append(uids, uid)
			}
		}
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uids, nil
}

// fetchMeta asks for the size, flags and arrival time of each message.
// Messages that were deleted in the meantime are missing from the map.
func (b *Backend) fetchMeta(ctx context.Context, folder string, uids []imap.UID) (map[imap.UID]meta, error) {
	b.conn.allow(1<<20 + int64(len(uids))<<16)
	options := &imap.FetchOptions{UID: true, Flags: true, RFC822Size: true, InternalDate: true}
	messages, err := b.client.Fetch(imap.UIDSetNum(uids...), options).Collect()
	if err != nil {
		return nil, b.commandError(ctx, "list the messages in "+folder, err)
	}
	wanted := imap.UIDSetNum(uids...)
	out := make(map[imap.UID]meta, len(messages))
	for _, m := range messages {
		if m.UID == 0 || !wanted.Contains(m.UID) {
			continue
		}
		out[m.UID] = meta{size: m.RFC822Size, flags: m.Flags, received: m.InternalDate}
	}
	return out, nil
}

// fetchBodies downloads whole messages. A body that turns out to be larger
// than maximum is not read. Its size goes into oversized instead.
func (b *Backend) fetchBodies(ctx context.Context, folder string, uids []imap.UID, expected, maximum int64) (map[imap.UID][]byte, map[imap.UID]int64, error) {
	bodies := map[imap.UID][]byte{}
	oversized := map[imap.UID]int64{}
	if len(uids) == 0 {
		return bodies, oversized, nil
	}
	// Allow for servers whose RFC822.SIZE differs a little from the bytes
	// they send, plus the response lines around each message.
	b.conn.allow(2*expected + 1<<20 + int64(len(uids))<<16)
	wanted := imap.UIDSetNum(uids...)
	section := &imap.FetchItemBodySection{Peek: true}
	cmd := b.client.Fetch(wanted, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}})
	var readErr error
	for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
		var uid imap.UID
		var raw []byte
		var size int64
		got := false
		for item := msg.Next(); item != nil; item = msg.Next() {
			switch item := item.(type) {
			case imapclient.FetchItemDataUID:
				uid = item.UID
			case imapclient.FetchItemDataBodySection:
				if item.Literal == nil || item.Section.Specifier != imap.PartSpecifierNone || len(item.Section.Part) > 0 {
					continue
				}
				if size = item.Literal.Size(); size > maximum {
					continue
				}
				data, err := io.ReadAll(io.LimitReader(item.Literal, maximum+1))
				if err != nil && readErr == nil {
					readErr = err
				}
				raw, got = data, true
			}
		}
		if uid == 0 || !wanted.Contains(uid) {
			continue
		}
		if size > maximum {
			oversized[uid] = size
			continue
		}
		if got {
			bodies[uid] = raw
		}
	}
	if err := cmd.Close(); err != nil {
		return nil, nil, b.commandError(ctx, "download messages from "+folder, err)
	}
	if readErr != nil {
		return nil, nil, b.commandError(ctx, "download messages from "+folder, readErr)
	}
	return bodies, oversized, nil
}

// fetchHeaders downloads only the header section of each message.
func (b *Backend) fetchHeaders(ctx context.Context, folder string, uids []imap.UID) (map[imap.UID][]byte, error) {
	headers := map[imap.UID][]byte{}
	if len(uids) == 0 {
		return headers, nil
	}
	b.conn.allow(1<<20 + int64(len(uids))*(headerLimit+1<<16))
	wanted := imap.UIDSetNum(uids...)
	section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true}
	cmd := b.client.Fetch(wanted, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}})
	var readErr error
	for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
		var uid imap.UID
		var raw []byte
		for item := msg.Next(); item != nil; item = msg.Next() {
			switch item := item.(type) {
			case imapclient.FetchItemDataUID:
				uid = item.UID
			case imapclient.FetchItemDataBodySection:
				if item.Literal == nil {
					continue
				}
				data, err := io.ReadAll(io.LimitReader(item.Literal, headerLimit))
				if err != nil && readErr == nil {
					readErr = err
				}
				raw = data
			}
		}
		if uid != 0 && wanted.Contains(uid) && raw != nil {
			headers[uid] = raw
		}
	}
	if err := cmd.Close(); err != nil {
		return nil, b.commandError(ctx, "download message headers from "+folder, err)
	}
	if readErr != nil {
		return nil, b.commandError(ctx, "download message headers from "+folder, readErr)
	}
	return headers, nil
}

// vanished reports which of uids no longer exist in the selected folder.
func (b *Backend) vanished(ctx context.Context, folder string, uids []imap.UID) (map[imap.UID]bool, error) {
	b.conn.allow(1<<20 + int64(len(uids))<<4)
	data, err := b.client.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{imap.UIDSetNum(uids...)}}, nil).Wait()
	if err != nil {
		return nil, b.commandError(ctx, "search "+folder, err)
	}
	present := imap.UIDSet{}
	if set, ok := data.All.(imap.UIDSet); ok {
		present = set
	}
	gone := map[imap.UID]bool{}
	for _, uid := range uids {
		if !present.Contains(uid) {
			gone[uid] = true
		}
	}
	return gone, nil
}
