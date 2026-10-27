package graph

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/vincentsch/mailmd/internal/backend"
	"golang.org/x/oauth2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMalformedEscapeCannotHideTraversal(t *testing.T) {
	for _, suffix := range []string{"/v1.0/%25ZZ%252f%252e%252e%252f%252e%252e%252foff-api", "/v1.0/%25ZZ%255coff-api", "/v1.0/%25ZZ%2500off-api"} {
		for _, mode := range []string{"continuation", "redirect", "cursor"} {
			t.Run(suffix+"/"+mode, func(t *testing.T) {
				var origin string
				destinations := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.RequestURI, "off-api") {
						destinations++
						t.Logf("destination saw path=%q bearer=%v", r.URL.Path, r.Header.Get("Authorization") == "Bearer fixture")
						json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/f/messages/delta?done=1"})
						return
					}
					if mode == "redirect" {
						http.Redirect(w, r, origin+suffix, http.StatusTemporaryRedirect)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.nextLink": origin + suffix})
				}))
				defer server.Close()
				origin = server.URL
				b, e := Open(context.Background(), Config{BaseURL: origin + "/v1.0", HTTPClient: server.Client(), TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fixture"})})
				if e != nil {
					t.Fatal(e)
				}
				defer b.Close()
				request := backend.FetchRequest{ScopeKey: scopeKey("f"), FolderPath: "Inbox"}
				if mode == "cursor" {
					request.Cursor = encodeCursor(cursor{Version: 1, Scope: request.ScopeKey, URL: origin + suffix})
				}
				result, e := b.Fetch(context.Background(), request)
				want := ErrProtocol
				if mode == "cursor" {
					want = backend.ErrInvalidCursor
				}
				if !errors.Is(e, want) || destinations != 0 {
					t.Fatalf("malformed escape short-circuits canonical guard: mode=%s dispatched=%d done=%v err=%v", mode, destinations, result.Done, e)
				}
			})
		}
	}
}
