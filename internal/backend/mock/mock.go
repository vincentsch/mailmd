// Package mock is an offline provider with fixed messages. Tests and
// mailmd demo use it.
package mock

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/mailmd/internal/privatefs"
)

type Item struct {
	Message         *mail.Message
	Diagnostic      *backend.Diagnostic
	RawBytes        int64
	PreserveFolders bool
}
type Call struct {
	Instance  int
	Operation string
	Request   backend.FetchRequest
}

// Service holds the fixed mail for all backends that a test opens from it.
// Each queued error is returned once, so a test can check that mailmd opens a
// new connection after a failed one.
type Service struct {
	mu                                    sync.Mutex
	FolderRecords                         []backend.Folder
	Items                                 map[string][]Item
	OpenErrors, FolderErrors, FetchErrors []error
	Calls                                 []Call
	instances                             int
}

func take(queue *[]error) error {
	if len(*queue) == 0 {
		return nil
	}
	e := (*queue)[0]
	*queue = (*queue)[1:]
	return e
}
func (s *Service) Open(ctx context.Context) (backend.Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s.instances++
	s.Calls = append(s.Calls, Call{Instance: s.instances, Operation: "open"})
	if e := take(&s.OpenErrors); e != nil {
		return nil, e
	}
	return &Backend{s: s, id: s.instances}, nil
}
func (s *Service) History() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.Calls...)
}

type Backend struct {
	s      *Service
	id     int
	closed bool
	failed bool
}

func (*Backend) Name() string { return "mock" }
func (b *Backend) Folders(ctx context.Context) ([]backend.Folder, error) {
	b.s.mu.Lock()
	defer b.s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if b.closed || b.failed {
		return nil, backend.ErrClosed
	}
	b.s.Calls = append(b.s.Calls, Call{Instance: b.id, Operation: "folders"})
	if e := take(&b.s.FolderErrors); e != nil {
		b.failed = true
		return nil, e
	}
	return append([]backend.Folder(nil), b.s.FolderRecords...), nil
}

// Count returns the number of fixed messages of each folder.
func (b *Backend) Count(ctx context.Context, folders []backend.Folder) (map[string]int, error) {
	b.s.mu.Lock()
	defer b.s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if b.closed || b.failed {
		return nil, backend.ErrClosed
	}
	b.s.Calls = append(b.s.Calls, Call{Instance: b.id, Operation: "count"})
	out := map[string]int{}
	for _, f := range folders {
		if items, ok := b.s.Items[f.ScopeKey]; ok {
			out[f.Path] = len(items)
		}
	}
	return out, nil
}

type cursor struct {
	Schema   string `json:"schema"`
	Scope    string `json:"scope"`
	Position int    `json:"position"`
}

func (b *Backend) Fetch(ctx context.Context, req backend.FetchRequest) (backend.FetchResult, error) {
	b.s.mu.Lock()
	defer b.s.mu.Unlock()
	if ctx.Err() != nil {
		return backend.FetchResult{}, ctx.Err()
	}
	if b.closed || b.failed {
		return backend.FetchResult{}, backend.ErrClosed
	}
	var e error
	req, e = req.WithDefaults()
	if e != nil {
		return backend.FetchResult{}, e
	}
	req.Cursor = append(backend.Cursor(nil), req.Cursor...)
	b.s.Calls = append(b.s.Calls, Call{Instance: b.id, Operation: "fetch", Request: req})
	if e := take(&b.s.FetchErrors); e != nil {
		b.failed = true
		return backend.FetchResult{}, e
	}
	items, ok := b.s.Items[req.ScopeKey]
	if !ok {
		return backend.FetchResult{}, backend.ErrScopeNotFound
	}
	c := cursor{Schema: "mailmd-mock-cursor/1", Scope: req.ScopeKey}
	if len(req.Cursor) > 0 {
		if privatefs.Decode(req.Cursor, &c) != nil || c.Schema != "mailmd-mock-cursor/1" || c.Scope != req.ScopeKey || c.Position < 0 || c.Position > len(items) {
			return backend.FetchResult{}, backend.ErrInvalidCursor
		}
	}
	result := backend.FetchResult{}
	var total int64
	count := 0
	for c.Position < len(items) && count < req.Limit {
		item := items[c.Position]
		size := item.RawBytes
		if size < 0 {
			return backend.FetchResult{}, errors.New("mock: invalid fixture")
		}
		tooLarge := size > req.MaxMessageBytes
		if tooLarge {
			size = 0
		}
		if count > 0 && size > req.MaxBatchBytes-total {
			break
		}
		total += size
		count++
		if item.Message != nil && (req.Since.IsZero() || !item.Message.Date.Before(req.Since)) {
			data, _ := json.Marshal(item.Message)
			var m mail.Message
			_ = json.Unmarshal(data, &m)
			if req.FolderPath != "" && !item.PreserveFolders {
				m.Folders = []string{req.FolderPath}
			}
			if tooLarge {
				m.BodyText, m.BodyHTML, m.Attachments, m.OmittedSize = "", "", nil, item.RawBytes
				if m.Ref == "" {
					m.Ref = strconv.Itoa(c.Position)
				}
				result.Diagnostics = append(result.Diagnostics, backend.Diagnostic{ScopeKey: req.ScopeKey, Ref: m.Ref, Reason: backend.ReasonTooLarge, Backend: "mock"})
			}
			result.Messages = append(result.Messages, m)
		}
		if item.Diagnostic != nil {
			d := *item.Diagnostic
			d.ScopeKey = req.ScopeKey
			d.Backend = "mock"
			result.Diagnostics = append(result.Diagnostics, d)
		}
		c.Position++
	}
	result.Cursor, _ = json.Marshal(c)
	result.RawBytes = total
	result.Done = c.Position == len(items)
	result.Remaining = len(items) - c.Position
	return result, nil
}
func (b *Backend) Close() error {
	b.s.mu.Lock()
	defer b.s.mu.Unlock()
	if !b.closed {
		b.s.Calls = append(b.s.Calls, Call{Instance: b.id, Operation: "close"})
		b.closed = true
	}
	return nil
}
