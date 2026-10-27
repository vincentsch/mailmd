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

func TestMalformedRemovalAnnotationCannotAdvanceDelta(t *testing.T) {
	for _, annotation := range []string{`false`, `"deleted"`, `[]`, `{"reason":7}`} {
		t.Run(annotation, func(t *testing.T) {
			var origin string
			mime := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/$value") {
					mime++
					t.Error("malformed delta accessed MIME")
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": "a", "@removed": json.RawMessage(annotation)}}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/f/messages/delta?done=1"})
			}))
			defer server.Close()
			origin = server.URL
			b, e := Open(context.Background(), Config{BaseURL: origin + "/v1.0", HTTPClient: server.Client(), TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fixture"})})
			if e != nil {
				t.Fatal(e)
			}
			defer b.Close()
			result, e := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: scopeKey("f"), FolderPath: "Inbox"})
			if !errors.Is(e, ErrProtocol) || len(result.Cursor) != 0 || mime != 0 {
				t.Fatalf("malformed marker %#v %v", result, e)
			}
		})
	}
}
