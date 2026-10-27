package graph

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vincentsch/mailmd/internal/backend"
	"golang.org/x/oauth2"
)

func TestUnsafeMessagePathsAreRejected(t *testing.T) {
	for _, suffix := range []string{"/v1.0/%252e%252e/off-api", "/v1.0/..%5c/off-api", "/v1.0/%00/off-api"} {
		for _, redirect := range []bool{false, true} {
			t.Run(suffix+map[bool]string{false: "/continuation", true: "/redirect"}[redirect], func(t *testing.T) {
				calls, bearer := 0, false
				var origin string
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.RequestURI, "off-api") {
						calls++
						bearer = r.Header.Get("Authorization") == "Bearer fixture-review002"
						json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/f/messages/delta?done=1"})
						return
					}
					if redirect {
						http.Redirect(w, r, origin+suffix, http.StatusTemporaryRedirect)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.nextLink": origin + suffix})
				}))
				defer s.Close()
				origin = s.URL
				b, e := Open(context.Background(), Config{BaseURL: origin + "/v1.0", HTTPClient: s.Client(), TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fixture-review002"})})
				if e != nil {
					t.Fatal(e)
				}
				defer b.Close()
				result, e := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: scopeKey("f"), FolderPath: "Inbox"})
				if !errors.Is(e, ErrProtocol) || calls != 0 {
					t.Fatalf("unsafe path dispatched: calls=%d bearer=%v done=%v err=%v", calls, bearer, result.Done, e)
				}
			})
		}
	}
}

func TestTruncatedMessageIsRetried(t *testing.T) {
	var origin string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/$value") {
			w.Header().Set("Content-Length", "200")
			w.WriteHeader(200)
			w.Write([]byte("short"))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": "a"}}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/f/messages/delta?done=1"})
	}))
	defer s.Close()
	origin = s.URL
	b, e := Open(context.Background(), Config{BaseURL: origin + "/v1.0", HTTPClient: s.Client(), TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fixture"})})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	result, e := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: scopeKey("f"), FolderPath: "Inbox"})
	if !errors.Is(e, backend.ErrRetryable) || len(result.Cursor) > 0 {
		t.Fatalf("body disconnect classification=%v retryable=%v cursor=%d", e, errors.Is(e, backend.ErrRetryable), len(result.Cursor))
	}
}

func TestMessagePathControls(t *testing.T) {
	for _, suffix := range []string{"/v1.0/valid-probe", "/outside-api", "/v1.0evil/probe", "/v1.0/%2e%2e/off-api"} {
		for _, redirect := range []bool{false, true} {
			t.Run(suffix+map[bool]string{false: "/continuation", true: "/redirect"}[redirect], func(t *testing.T) {
				requests := 0
				var origin string
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !strings.Contains(r.RequestURI, "/me/mailFolders/") {
						requests++
						json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/f/messages/delta?done=1"})
						return
					}
					if redirect {
						http.Redirect(w, r, origin+suffix, http.StatusTemporaryRedirect)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.nextLink": origin + suffix})
				}))
				defer s.Close()
				origin = s.URL
				b, e := Open(context.Background(), Config{BaseURL: origin + "/v1.0", HTTPClient: s.Client(), TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fixture"})})
				if e != nil {
					t.Fatal(e)
				}
				defer b.Close()
				result, e := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: scopeKey("f"), FolderPath: "Inbox"})
				if suffix == "/v1.0/valid-probe" {
					if e != nil || requests != 1 || !result.Done {
						t.Fatalf("valid same API failed: requests=%d result=%+v err=%v", requests, result, e)
					}
				} else {
					if !errors.Is(e, ErrProtocol) || requests != 0 {
						t.Fatalf("invalid path control: requests=%d err=%v", requests, e)
					}
				}
			})
		}
	}
}
