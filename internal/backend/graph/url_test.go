package graph

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vincentsch/mailmd/internal/backend"
	"golang.org/x/oauth2"
)

func TestContinuationLinksAreCheckedBeforeUse(t *testing.T) {
	for _, suffix := range []string{"/v1.0/%252e%252e/off-api", "/v1.0/%25252e%25252e/off-api", "/v1.0/..%5c/off-api", "/v1.0/%00/off-api"} {
		t.Run(suffix, func(t *testing.T) {
			calls := 0
			var origin string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.nextLink": origin + suffix})
			}))
			defer server.Close()
			origin = server.URL
			input := server.Client()
			redirect := input.CheckRedirect
			b, err := Open(context.Background(), Config{HTTPClient: input, BaseURL: origin + "/v1.0", TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "AUTH_HEADER_SENTINEL"})})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			cursor := encodeCursor(structCursor(scopeKey("f"), origin+suffix))
			result, err := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: scopeKey("f"), Cursor: cursor})
			if !errors.Is(err, backend.ErrInvalidCursor) || calls != 0 || len(result.Cursor) != 0 {
				t.Fatalf("local cursor dispatched: %#v %v calls=%d", result, err, calls)
			}
			if _, err := b.Folders(context.Background()); !errors.Is(err, ErrProtocol) || calls != 1 {
				t.Fatalf("folder continuation: %v calls=%d", err, calls)
			}
			if (redirect == nil) != (input.CheckRedirect == nil) {
				t.Fatal("input client redirect policy changed")
			}
		})
	}
}
func structCursor(scope, link string) cursor { return cursor{Version: 1, Scope: scope, URL: link} }

func TestEscapedIDsAndOpaqueQueries(t *testing.T) {
	id := "a//b+/="
	folderID := "f/+//="
	var origin string
	mime := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/$value") {
			mime++
			if !strings.Contains(r.URL.EscapedPath(), url.PathEscape(id)) {
				t.Error("opaque message ID rewritten")
			}
			w.Write([]byte("Message-ID: <escaped@example.test>\r\nFrom: fixture@example.test\r\nContent-Type: text/plain\r\n\r\nBODY_SENTINEL"))
			return
		}
		if !strings.Contains(r.URL.EscapedPath(), url.PathEscape(folderID)) {
			t.Error("opaque folder ID rewritten")
		}
		json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": id}}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/" + url.PathEscape(folderID) + "/messages/delta?$deltatoken=%252e%252e%2f%00"})
	}))
	defer server.Close()
	origin = server.URL
	b, err := Open(context.Background(), Config{BaseURL: origin + "/v1.0", HTTPClient: server.Client(), TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fixture"})})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	result, err := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: scopeKey(folderID), FolderPath: "Inbox"})
	if err != nil || !result.Done || len(result.Messages) != 1 || mime != 1 {
		t.Fatalf("legitimate escaped IDs: %#v %v", result, err)
	}
	if _, err := b.decodeCursor(scopeKey(folderID), result.Cursor); err != nil {
		t.Fatalf("opaque query cursor: %v", err)
	}
}
