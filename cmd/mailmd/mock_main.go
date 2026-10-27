//go:build mailmd_mock

// This build runs the offline conformance test against a fake mailbox. Release
// builds don't include it.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/mock"
	"github.com/vincentsch/mailmd/internal/cli"
	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/rungrad/update"
)

type offlineTransport struct{}

func (offlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("outbound network disabled")
}

type offlineRelease struct{}

func (offlineRelease) Latest() (update.Release, error) { return update.Release{Version: "v0.1.0"}, nil }
func main() {
	runtime.GOMAXPROCS(2)
	http.DefaultTransport = offlineTransport{}
	m := mail.Message{MessageID: "fixture@invalid", Subject: "SUBJECT_SENTINEL", BodyText: "BODY_SENTINEL", Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s := &mock.Service{FolderRecords: []backend.Folder{{Path: "Inbox", ScopeKey: "scope", Fetchable: true}}, Items: map[string][]mock.Item{"scope": {{Message: &m, RawBytes: 100}}}}
	opts := cli.Options{Version: "dev", UpdateFetcher: offlineRelease{}, Open: func(ctx context.Context, a accounts.AccountConfig, o accounts.OpenOptions) (backend.Backend, error) {
		if a.Backend != "mock" {
			return nil, backend.ErrNotImplemented
		}
		if a.Name == "forbidden" {
			return nil, backend.ErrPermissionDenied
		}
		o.Mock = s.Open
		b, e := accounts.OpenBackend(ctx, a, o)
		if e == nil && a.Name == "interrupt" {
			b = &interruptBackend{Backend: b}
		}
		return b, e
	}}
	os.Exit(cli.Main(opts))
}

type interruptBackend struct {
	backend.Backend
	calls int
}

func (b *interruptBackend) Fetch(ctx context.Context, r backend.FetchRequest) (backend.FetchResult, error) {
	b.calls++
	if b.calls > 1 {
		<-ctx.Done()
		return backend.FetchResult{}, ctx.Err()
	}
	result, e := b.Backend.Fetch(ctx, r)
	if e == nil && len(r.Cursor) == 0 {
		result.Done = false
	}
	return result, e
}
