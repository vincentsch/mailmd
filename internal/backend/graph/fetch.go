package graph

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"sort"
	"strings"

	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/rfc822"
	mdmail "github.com/vincentsch/mailmd/internal/mail"
)

// errBatchFull means the next message does not fit the rest of the batch.
var errBatchFull = errors.New("graph: the batch is full")

// download fetches one message. budget is how many bytes the batch has
// left. A message larger than that ends the batch, unless it is over the size
// limit and saved with its headers only. size is the bytes the message used,
// and reason is set when it was saved only in part.
func (b *Backend) download(ctx context.Context, item change, request backend.FetchRequest, budget int64) (message mdmail.Message, size int64, reason backend.DiagnosticReason, err error) {
	raw, err := b.response(ctx, b.endpoint("/me/messages/"+url.PathEscape(item.ID)+"/$value"), request.MaxMessageBytes, 0)
	switch {
	case errors.Is(err, backend.ErrResponseTooLarge):
		// Save only the headers, so one huge message doesn't block the
		// folder. Graph doesn't say how large the message is.
		if message, err = b.headersOnly(ctx, item.ID, request.MaxMessageBytes+1); err != nil {
			return mdmail.Message{}, 0, "", err
		}
		reason = backend.ReasonTooLarge
	case err != nil:
		return mdmail.Message{}, 0, "", err
	case int64(len(raw)) > budget:
		return mdmail.Message{}, 0, "", errBatchFull
	default:
		if message, err = rfc822.Normalize(raw); err != nil {
			reason = backend.ReasonParseFailure
			if errors.Is(err, rfc822.ErrIdentity) {
				reason = backend.ReasonIdentityFailure
			}
			message = rfc822.Unreadable(raw, err)
		}
		message.Raw = raw
	}
	message.Folders = []string{request.FolderPath}
	message.ThreadID = item.Conversation
	message.Ref = item.ID
	message.Received = item.receivedTime()
	message.Flags.Seen = item.Read
	message.Flags.Draft = item.Draft
	message.Flags.Flagged = strings.EqualFold(item.Flag.Status, "flagged")
	return message, int64(len(raw)), reason, nil
}

// FetchRefs fetches messages again by their IDs, like a message that was
// larger than the size limit before.
func (b *Backend) FetchRefs(ctx context.Context, request backend.FetchRequest, refs []string) (backend.FetchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || b.isClosed() {
		return backend.FetchResult{}, backend.ErrClosed
	}
	request, err := request.WithDefaults()
	if err != nil {
		return backend.FetchResult{}, err
	}
	b.op.Lock()
	defer b.op.Unlock()
	var result backend.FetchResult
	for _, ref := range refs {
		if !validID(ref) {
			return backend.FetchResult{}, backend.ErrInvalidCursor
		}
		var item change
		err := b.json(ctx, b.endpoint("/me/messages/"+url.PathEscape(ref)+"?$select=id,conversationId,receivedDateTime,isRead,isDraft,flag"), 1<<20, 0, &item)
		if err == nil && !validChange(item) {
			err = ErrProtocol
		}
		var message mdmail.Message
		var reason backend.DiagnosticReason
		if err == nil {
			message, _, reason, err = b.download(ctx, item, request, math.MaxInt64)
		}
		var provider *providerError
		switch {
		case errors.As(err, &provider) && provider.Status == 404:
			result.Diagnostics = append(result.Diagnostics, backend.Diagnostic{Backend: b.Name(), ScopeKey: request.ScopeKey, Ref: ref, Reason: backend.ReasonVanished})
		case err != nil:
			return backend.FetchResult{}, err
		default:
			result.Messages = append(result.Messages, message)
			if reason != "" {
				result.Diagnostics = append(result.Diagnostics, backend.Diagnostic{Backend: b.Name(), ScopeKey: request.ScopeKey, Ref: ref, Reason: reason})
			}
		}
	}
	return result, nil
}

// headersOnly builds a message from the internet headers of a message that
// is larger than the size limit. size is how large the message is at least.
func (b *Backend) headersOnly(ctx context.Context, id string, size int64) (mdmail.Message, error) {
	var page struct {
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"internetMessageHeaders"`
	}
	if err := b.json(ctx, b.endpoint("/me/messages/"+url.PathEscape(id)+"?$select=internetMessageHeaders"), 1<<20, 0, &page); err != nil {
		return mdmail.Message{}, err
	}
	var header strings.Builder
	for _, h := range page.Headers {
		if h.Name == "" || strings.ContainsAny(h.Name+h.Value, "\r\n") {
			continue
		}
		header.WriteString(h.Name + ": " + h.Value + "\r\n")
	}
	header.WriteString("\r\n")
	message, err := rfc822.NormalizeHeaders([]byte(header.String()), size)
	if err != nil {
		message = rfc822.Unreadable([]byte(header.String()), err)
		message.Raw, message.ParseError, message.OmittedSize = nil, "", size
	}
	return message, nil
}

type deltaPage struct {
	Value []struct {
		change
		Annotation json.RawMessage `json:"@removed"`
	} `json:"value"`
	Next  string `json:"@odata.nextLink"`
	Delta string `json:"@odata.deltaLink"`
}

func (b *Backend) Fetch(ctx context.Context, request backend.FetchRequest) (backend.FetchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return backend.FetchResult{}, ctx.Err()
	}
	if b == nil || b.isClosed() {
		return backend.FetchResult{}, backend.ErrClosed
	}
	request, err := request.WithDefaults()
	if err != nil {
		return backend.FetchResult{}, err
	}
	id, err := scopeID(request.ScopeKey)
	if err != nil {
		return backend.FetchResult{}, err
	}
	// mailmd applies --since itself. A delta query with $filter returns at
	// most 5,000 messages, and Graph doesn't say that more are left.
	initial := b.endpoint("/me/mailFolders/" + url.PathEscape(id) + "/messages/delta?$select=id,conversationId,receivedDateTime,isRead,isDraft,flag")
	state := cursor{Version: 1, Scope: request.ScopeKey, URL: initial}
	if len(request.Cursor) > 0 {
		state, err = b.decodeCursor(request.ScopeKey, request.Cursor)
		if err != nil {
			return backend.FetchResult{}, err
		}
	}
	// When nothing is pending, a new round of changes starts from the saved
	// delta link.
	if len(state.Pending) == 0 {
		state.Final = false
	}
	b.op.Lock()
	defer b.op.Unlock()
	if b.isClosed() {
		return backend.FetchResult{}, backend.ErrClosed
	}
	var result backend.FetchResult
	var used int64
	processed := 0
	reset := false
	visited := map[string]bool{}
	finish := func(done bool) (backend.FetchResult, error) {
		encoded, err := persistableCursor(state)
		if err != nil {
			return backend.FetchResult{}, err
		}
		result.RawBytes = used
		result.Done = done
		result.Cursor = encoded
		return result, nil
	}
	for {
		if ctx.Err() != nil {
			return backend.FetchResult{}, ctx.Err()
		}
		if len(state.Pending) == 0 {
			if state.Final {
				return finish(true)
			}
			if processed >= request.Limit {
				return finish(false)
			}
			if visited[state.URL] {
				return backend.FetchResult{}, ErrProtocol
			}
			visited[state.URL] = true
			var page deltaPage
			err := b.json(ctx, state.URL, request.MaxBatchBytes, request.Limit, &page)
			if err != nil {
				var provider *providerError
				if !reset && errors.As(err, &provider) && (provider.Status == 410 || provider.Code == "syncStateNotFound" || provider.Code == "SyncStateNotFound" || provider.Code == "InvalidDeltaToken" || provider.Code == "ErrorInvalidSyncStateData") {
					state = cursor{Version: 1, Scope: request.ScopeKey, URL: initial}
					reset = true
					visited = map[string]bool{}
					result = backend.FetchResult{}
					used = 0
					processed = 0
					continue
				}
				if errors.As(err, &provider) && provider.Status == 404 {
					return backend.FetchResult{}, backend.ErrScopeNotFound
				}
				return backend.FetchResult{}, err
			}
			if (page.Next == "") == (page.Delta == "") {
				return backend.FetchResult{}, ErrProtocol
			}
			state.Final = page.Delta != ""
			state.URL = page.Next
			if state.Final {
				state.URL = page.Delta
			}
			if !b.validURL(state.URL) {
				return backend.FetchResult{}, ErrProtocol
			}
			if len(page.Value) > maxCursorPending {
				return backend.FetchResult{}, backend.ErrProviderStateLimit
			}
			state.Pending = nil
			for _, wire := range page.Value {
				item := wire.change
				item.Removed = len(wire.Annotation) > 0 && string(wire.Annotation) != "null"
				if item.Removed {
					var marker map[string]json.RawMessage
					if json.Unmarshal(wire.Annotation, &marker) != nil || marker == nil {
						return backend.FetchResult{}, ErrProtocol
					}
					if raw, ok := marker["reason"]; ok {
						var reason string
						if json.Unmarshal(raw, &reason) != nil {
							return backend.FetchResult{}, ErrProtocol
						}
					}
				}
				if !validChange(item) {
					return backend.FetchResult{}, ErrProtocol
				}
				state.Pending = append(state.Pending, item)
			}
			for _, item := range state.Pending {
				if !validID(item.ID) {
					return backend.FetchResult{}, ErrProtocol
				}
			}
			sort.SliceStable(state.Pending, func(i, j int) bool { return state.Pending[i].ID < state.Pending[j].ID })
			// Check that the cursor with this whole page fits into the state
			// file before any message is fetched. Otherwise mailmd could fetch
			// a few messages and then fail to save the position of the rest.
			if _, err := persistableCursor(state); err != nil {
				return backend.FetchResult{}, err
			}
		}
		for len(state.Pending) > 0 {
			if processed >= request.Limit {
				return finish(false)
			}
			item := state.Pending[0]
			reason := backend.DiagnosticReason("")
			if item.Removed {
				reason = backend.ReasonRemoteRemoved
			} else if !request.Since.IsZero() && item.receivedTime().Before(request.Since) {
				// The message is older than --since, so it is passed by
				// without a download.
			} else {
				message, size, why, err := b.download(ctx, item, request, request.MaxBatchBytes-used)
				var provider *providerError
				switch {
				case errors.Is(err, errBatchFull):
					return finish(false)
				case errors.As(err, &provider) && provider.Status == 404:
					reason = backend.ReasonVanished
				case err != nil:
					return backend.FetchResult{}, err
				default:
					used += size
					reason = why
					result.Messages = append(result.Messages, message)
				}
			}
			if reason != "" {
				result.Diagnostics = append(result.Diagnostics, backend.Diagnostic{Backend: b.Name(), ScopeKey: request.ScopeKey, Ref: item.ID, Reason: reason})
			}
			state.Pending = state.Pending[1:]
			processed++
		}
	}
}
