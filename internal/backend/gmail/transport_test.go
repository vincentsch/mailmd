package gmail

import (
	"context"
	"errors"
	"fmt"
	"github.com/vincentsch/mailmd/internal/backend"
	"google.golang.org/api/googleapi"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestTruncatedLabelListIsRetried(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "200")
		fmt.Fprint(w, `{"labels":`)
	}))
	defer s.Close()
	origin, _ := url.Parse(s.URL)
	b, e := Open(context.Background(), Config{HTTPClient: &http.Client{Transport: localGmailTransport{origin}}})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	_, e = b.Folders(context.Background())
	if !errors.Is(e, backend.ErrRetryable) {
		t.Fatalf("truncated label HTTP body err=%v retryable=false", e)
	}
}
func TestHugeRetryAfterDoesNotOverflow(t *testing.T) {
	err := classifyAPIError(&googleapi.Error{Code: 429, Header: http.Header{"Retry-After": []string{"9223372036854775807"}}})
	var hint *backend.RetryableError
	if !errors.As(err, &hint) {
		t.Fatal(err)
	}
	t.Logf("integer Retry-After seconds=9223372036854775807 parsed wait=%s", hint.RetryAfter)
	if hint.RetryAfter <= 15*time.Minute || hint.RetryAfter != time.Duration(math.MaxInt64) {
		t.Fatalf("provider wait overflowed: %s", hint.RetryAfter)
	}
}

func TestStalledGmailRequestTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	origin, _ := url.Parse(srv.URL)
	b, err := Open(context.Background(), Config{HTTPClient: &http.Client{Transport: localGmailTransport{origin}}, IdleTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	start := time.Now()
	if _, err := b.Folders(context.Background()); !errors.Is(err, backend.ErrRetryable) {
		t.Fatalf("Folders() = %v, want a retryable error", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the stalled request was not stopped")
	}
}
