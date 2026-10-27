package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthGraphRedirectPath(t *testing.T) {
	for _, kind := range []string{"valid-api", "valid-identity", "encoded-traversal", "outside-api", "identity-origin", "double-encoded-traversal", "encoded-slash", "api-prefix", "scheme", "host", "port", "userinfo", "identity-to-graph", "identity-traversal", "identity-method"} {
		t.Run(kind, func(t *testing.T) {
			reached := false
			authReceived := false
			graphURL := ""
			deviceRedirected := false
			identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/devicecode"):
					if kind == "valid-identity" && !deviceRedirected {
						deviceRedirected = true
						w.Header().Set("Location", r.URL.Path)
						w.WriteHeader(307)
						return
					}
					if kind == "identity-to-graph" {
						w.Header().Set("Location", graphURL+"/v1.0/probe")
						w.WriteHeader(307)
						return
					}
					if kind == "identity-traversal" {
						w.Header().Set("Location", "/common/oauth2/v2.0/%2e%2e/probe")
						w.WriteHeader(307)
						return
					}
					if kind == "identity-method" {
						w.Header().Set("Location", r.URL.Path)
						w.WriteHeader(302)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"device_code": "FAKE-DEVICE", "user_code": "PUBLIC-CODE", "verification_uri": "https://microsoft.com/devicelogin", "expires_in": 120, "interval": 1})
				case strings.HasSuffix(r.URL.Path, "/token"):
					json.NewEncoder(w).Encode(map[string]any{"access_token": "FAKE-ACCESS", "refresh_token": "FAKE-REFRESH", "token_type": "Bearer", "expires_in": 3600, "scope": strings.Join(graphScopes(), " ")})
				default:
					reached = true
					authReceived = r.Header.Get("Authorization") == "Bearer FAKE-ACCESS"
					json.NewEncoder(w).Encode(map[string]any{"value": []any{}})
				}
			}))
			defer identity.Close()
			redirect := map[string]string{"valid-api": "/v1.0/valid-probe", "valid-identity": "/v1.0/valid-probe", "encoded-traversal": "/v1.0/%2e%2e/off-api", "outside-api": "/outside-api", "identity-origin": identity.URL + "/probe", "double-encoded-traversal": "/v1.0/%252e%252e/off-api", "encoded-slash": "/v1.0/%2e%2e%2foff-api", "api-prefix": "/v1.0evil/probe", "port": identity.URL + "/v1.0/probe"}[kind]
			graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1.0/me/mailFolders" {
					w.Header().Set("Location", redirect)
					w.WriteHeader(302)
					return
				}
				reached = true
				authReceived = r.Header.Get("Authorization") == "Bearer FAKE-ACCESS"
				json.NewEncoder(w).Encode(map[string]any{"value": []any{}})
			}))
			defer graph.Close()
			graphURL = graph.URL
			if kind == "scheme" {
				redirect = strings.Replace(graph.URL, "http:", "https:", 1) + "/v1.0/probe"
			}
			if kind == "host" {
				redirect = strings.Replace(graph.URL, "127.0.0.1", "localhost", 1) + "/v1.0/probe"
			}
			if kind == "userinfo" {
				redirect = strings.Replace(graph.URL, "http://", "http://fixture@", 1) + "/v1.0/probe"
			}
			root := t.TempDir()
			_, e := ConnectGraph(context.Background(), GraphConnectOptions{ClientID: graphFixtureClient, ConfigRoot: root, ArchiveDir: "fixture", HTTPClient: graph.Client(), IdentityBaseURL: identity.URL, GraphBaseURL: graph.URL + "/v1.0", HandleDeviceCode: func(context.Context, DeviceCodePrompt) error { return nil }})
			_, statErr := os.Stat(filepath.Join(root, "mailmd", "fixture", "graph-token.json"))
			t.Logf("redirect=%s reached=%t authorization=%t persisted=%t error=%v", kind, reached, authReceived, statErr == nil, e)
			if kind == "valid-api" || kind == "valid-identity" {
				if e != nil || !reached || !authReceived || statErr != nil {
					t.Fatal("valid control failed")
				}
			} else if reached || e == nil || statErr == nil {
				t.Fatal("unsafe Graph auth probe redirect was followed and credential persisted")
			}
		})
	}
}

func TestGraphAuthRequestMethods(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(`{"value":[]}`)) }))
	defer srv.Close()
	for _, item := range []struct {
		client       *http.Client
		method, path string
	}{
		{graphHTTPClient(srv.Client(), srv.URL+"/v1.0"), http.MethodPost, "/v1.0/probe"},
		{graphHTTPClient(srv.Client(), srv.URL, "/common/oauth2/v2.0/token"), http.MethodGet, "/common/oauth2/v2.0/token"},
		{graphHTTPClient(srv.Client(), srv.URL, "/common/oauth2/v2.0/token"), http.MethodPost, "/v1.0/probe"},
	} {
		request, _ := http.NewRequest(item.method, srv.URL+item.path, nil)
		if response, err := item.client.Do(request); err == nil {
			response.Body.Close()
			t.Fatal("invalid request dispatched")
		}
	}
	if calls != 0 {
		t.Fatalf("denied destination received %d requests", calls)
	}
}
