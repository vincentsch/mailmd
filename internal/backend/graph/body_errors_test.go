package graph

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/vincentsch/mailmd/internal/backend"
	"golang.org/x/oauth2"
)

type bodyFailure struct{ err error }

func (r bodyFailure) Read([]byte) (int, error) { return 0, r.err }
func (r bodyFailure) Close() error             { return nil }

type timeoutFailure struct{}

func (timeoutFailure) Error() string   { return "PRIVATE-TIMEOUT-SENTINEL" }
func (timeoutFailure) Timeout() bool   { return true }
func (timeoutFailure) Temporary() bool { return true }

type bodyTransport struct {
	body   io.ReadCloser
	status int
	length int64
}

func (t bodyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: t.status, Header: make(http.Header), Body: t.body, ContentLength: t.length}, nil
}

func TestBodyDownloadErrors(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, timeoutFailure{}} {
		b, e := Open(context.Background(), Config{BaseURL: "http://127.0.0.1/v1.0", HTTPClient: &http.Client{Transport: bodyTransport{bodyFailure{err}, 200, 200}}, TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fixture"})})
		if e != nil {
			t.Fatal(e)
		}
		_, e = b.response(context.Background(), b.endpoint("/me/mailFolders"), 1000, 0)
		b.Close()
		if !errors.Is(e, backend.ErrRetryable) || strings.Contains(e.Error(), "PRIVATE-") {
			t.Fatalf("body transport: %v", e)
		}
	}
	for _, status := range []int{401, 403} {
		b, e := Open(context.Background(), Config{BaseURL: "http://127.0.0.1/v1.0", HTTPClient: &http.Client{Transport: bodyTransport{bodyFailure{io.ErrUnexpectedEOF}, status, 200}}, TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fixture"})})
		if e != nil {
			t.Fatal(e)
		}
		_, e = b.response(context.Background(), b.endpoint("/me/mailFolders"), 1000, 0)
		b.Close()
		if e == nil || errors.Is(e, backend.ErrRetryable) {
			t.Fatalf("status boundary: %v", e)
		}
	}
	b, _ := Open(context.Background(), Config{BaseURL: "http://127.0.0.1/v1.0", HTTPClient: &http.Client{Transport: bodyTransport{io.NopCloser(strings.NewReader(`{"value":`)), 200, 0}}, TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fixture"})})
	defer b.Close()
	var value any
	if e := b.json(context.Background(), b.endpoint("/me/mailFolders"), 1000, 0, &value); !errors.Is(e, ErrProtocol) || errors.Is(e, backend.ErrRetryable) {
		t.Fatalf("fully received invalid JSON: %v", e)
	}
}
