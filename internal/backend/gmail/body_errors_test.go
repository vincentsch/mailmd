package gmail

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/vincentsch/mailmd/internal/backend"
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
	for _, path := range []string{"/labels", "/messages/a?format=raw"} {
		for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, timeoutFailure{}} {
			request, _ := http.NewRequestWithContext(context.Background(), "GET", "http://127.0.0.1"+path, nil)
			_, e := (boundedTransport{bodyTransport{bodyFailure{err}, 200, 200}}).RoundTrip(request)
			e = classifyAPIError(e)
			if !errors.Is(e, backend.ErrRetryable) || strings.Contains(e.Error(), "PRIVATE-") {
				t.Fatalf("body transport: %v", e)
			}
		}
	}
	for _, status := range []int{401, 403} {
		request, _ := http.NewRequest("GET", "http://127.0.0.1/labels", nil)
		_, e := (boundedTransport{bodyTransport{bodyFailure{io.ErrUnexpectedEOF}, status, 200}}).RoundTrip(request)
		if e = classifyAPIError(e); e == nil || errors.Is(e, backend.ErrRetryable) {
			t.Fatalf("status boundary: %v", e)
		}
	}
	b, e := Open(context.Background(), Config{HTTPClient: &http.Client{Transport: bodyTransport{io.NopCloser(strings.NewReader(`{"labels":`)), 200, 0}}})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if _, e = b.Folders(context.Background()); e == nil || errors.Is(e, backend.ErrRetryable) {
		t.Fatalf("fully received invalid JSON: %v", e)
	}
}
