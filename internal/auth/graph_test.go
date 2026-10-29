package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/privatefs"
)

const graphFixtureClient = "11111111-1111-1111-1111-111111111111"

func graphFixture(t *testing.T, mode string) (GraphConnectOptions, *atomic.Int64) {
	t.Helper()
	var polls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseForm()
		if strings.HasSuffix(r.URL.Path, "/devicecode") {
			if r.Form.Get("client_id") != graphFixtureClient || r.Form.Get("scope") != strings.Join(graphScopes(), " ") || r.Form.Get("client_secret") != "" {
				t.Error("device request violated public-client/scope contract")
			}
			if mode == "malformed-device" {
				_, _ = w.Write([]byte(`{"device_code":"PRIVATE-DEVICE","user_code":123}`))
				return
			}
			if mode == "device-2026" {
				// Microsoft's answer on 2026-10-29, with the device code
				// replaced and the interval cut from 5 to 1 second.
				data, err := os.ReadFile("testdata/graph-devicecode-2026-10.json")
				if err != nil {
					t.Error(err)
				}
				_, _ = w.Write(data)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "PRIVATE-DEVICE", "user_code": "PUBLIC-CODE", "verification_uri": "https://microsoft.com/devicelogin", "expires_in": 120, "interval": 1})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/token") {
			count := polls.Add(1)
			if mode == "revoked" && r.Form.Get("grant_type") == "refresh_token" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"PRIVATE-BODY PRIVATE-REFRESH"}`))
				return
			}
			if r.Form.Get("client_id") != graphFixtureClient || r.Form.Get("client_secret") != "" {
				t.Error("token request contains client secret or wrong client ID")
			}
			code := ""
			if mode == "pending" && count == 1 {
				code = "authorization_pending"
			}
			if mode == "slow" && count == 1 {
				code = "slow_down"
			}
			if mode == "denied" {
				code = "access_denied"
			}
			if mode == "expired" {
				code = "expired_token"
			}
			if mode == "consent" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"consent_required","error_description":"PRIVATE-BODY PRIVATE-DEVICE","error_codes":[65001]}`))
				return
			}
			if code != "" {
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": code, "error_description": "PRIVATE-BODY PRIVATE-DEVICE"})
				return
			}
			refresh := "PRIVATE-REFRESH"
			if mode == "missing-refresh" {
				refresh = ""
			}
			access := "PRIVATE-ACCESS"
			if r.Form.Get("grant_type") == "refresh_token" {
				access = "ROTATED-ACCESS"
				refresh = "ROTATED-REFRESH"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 3600, "scope": strings.Join(graphScopes(), " ")})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/me/mailFolders") {
			if r.Header.Get("Authorization") != "Bearer PRIVATE-ACCESS" || r.URL.Query().Get("$top") != "1" || r.URL.Query().Get("$select") != "id,displayName" {
				t.Error("incorrect read-only access probe")
			}
			if mode == "probe-denied" {
				w.WriteHeader(403)
				_, _ = w.Write([]byte(`{"error":{"code":"ErrorAccessDenied","message":"PRIVATE-BODY PRIVATE-ACCESS"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"value":[{"id":"folder","displayName":"Inbox"}]}`))
			return
		}
		http.Error(w, "PRIVATE-BODY", 404)
	}))
	t.Cleanup(server.Close)
	opts := GraphConnectOptions{ClientID: graphFixtureClient, Tenant: "consumers", ConfigRoot: t.TempDir(), ArchiveDir: "acct", HTTPClient: server.Client(), IdentityBaseURL: server.URL, GraphBaseURL: server.URL,
		HandleDeviceCode: func(_ context.Context, p DeviceCodePrompt) error {
			if p.UserCode != "PUBLIC-CODE" || p.VerificationURI != "https://microsoft.com/devicelogin" {
				t.Errorf("unsafe device output: %#v", p)
			}
			return nil
		}}
	return opts, &polls
}

func TestGraphRevokedRefreshIsReconnectWithoutPersistenceOrLeak(t *testing.T) {
	opts, polls := graphFixture(t, "revoked")
	connected, e := ConnectGraph(context.Background(), opts)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(connected.CredentialPath)
	loaded, e := LoadGraphToken(context.Background(), GraphLoadOptions{ConfigRoot: opts.ConfigRoot, ArchiveDir: opts.ArchiveDir, HTTPClient: opts.HTTPClient, IdentityBaseURL: opts.IdentityBaseURL})
	if e != nil {
		t.Fatal(e)
	}
	_, e = loaded.TokenSource.(*graphTokenSource).RefreshContext(context.Background())
	if !errors.Is(e, backend.ErrReconnectRequired) || strings.Contains(e.Error(), "PRIVATE-") || polls.Load() != 2 {
		t.Fatalf("revoked error %v polls %d", e, polls.Load())
	}
	after, _ := os.ReadFile(connected.CredentialPath)
	if string(before) != string(after) {
		t.Fatal("revoked grant changed credential")
	}
}

func TestGraphDeviceFlowSuccessPendingSlowAndPrivateStorage(t *testing.T) {
	for _, mode := range []string{"success", "pending", "slow"} {
		t.Run(mode, func(t *testing.T) {
			opts, polls := graphFixture(t, mode)
			result, err := ConnectGraph(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "success" && polls.Load() != 2 {
				t.Fatalf("poll sequence=%d", polls.Load())
			}
			info, err := os.Stat(result.CredentialPath)
			if err != nil || !privatefs.HasMode(info.Mode(), 0o600) {
				t.Fatalf("credential mode: %v %v", info, err)
			}
			for _, dir := range []string{filepath.Dir(result.CredentialPath), filepath.Dir(filepath.Dir(result.CredentialPath))} {
				info, err := os.Stat(dir)
				if err != nil || !privatefs.HasMode(info.Mode(), 0o700) {
					t.Fatalf("directory mode: %v %v", info, err)
				}
			}
			loaded, err := LoadGraphToken(context.Background(), GraphLoadOptions{ConfigRoot: opts.ConfigRoot, ArchiveDir: opts.ArchiveDir, HTTPClient: opts.HTTPClient, IdentityBaseURL: opts.IdentityBaseURL})
			if err != nil {
				t.Fatal(err)
			}
			token, err := loaded.TokenSource.Token()
			if err != nil || token.AccessToken != "PRIVATE-ACCESS" {
				t.Fatalf("loaded token error=%v", err)
			}
		})
	}
}

func TestGraphDeviceFailuresLeaveNoCredentialAndNoSecret(t *testing.T) {
	for _, mode := range []string{"denied", "expired", "consent", "missing-refresh", "probe-denied", "malformed-device", "canceled", "sync-failure", "rename-failure"} {
		t.Run(mode, func(t *testing.T) {
			opts, _ := graphFixture(t, mode)
			ctx := context.Background()
			if mode == "canceled" {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			}
			if mode == "sync-failure" {
				opts.files.syncFile = func(*os.File) error { return errors.New("injected file sync") }
			}
			if mode == "rename-failure" {
				opts.files.rename = func(string, string) error { return errors.New("injected rename") }
			}
			_, err := ConnectGraph(ctx, opts)
			if err == nil {
				t.Fatal("accepted failed authorization")
			}
			if mode == "consent" && !errors.Is(err, ErrAdminConsentRequired) {
				t.Fatalf("consent category=%v", err)
			}
			if mode == "missing-refresh" && !errors.Is(err, backend.ErrReconnectRequired) {
				t.Fatalf("reconnect category=%v", err)
			}
			if mode == "probe-denied" && !errors.Is(err, backend.ErrPermissionDenied) {
				t.Fatalf("permission category=%v", err)
			}
			for _, secret := range []string{"PRIVATE-ACCESS", "PRIVATE-REFRESH", "PRIVATE-DEVICE", "PRIVATE-BODY"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %s", secret)
				}
			}
			if _, err := os.Stat(filepath.Join(opts.ConfigRoot, "mailmd", opts.ArchiveDir, "graph-token.json")); !os.IsNotExist(err) {
				t.Fatal("failed connect left credential")
			}
		})
	}
}

func TestGraphRefreshRotationDurableAndMemoryOnly(t *testing.T) {
	for _, policy := range []TokenPersistence{PersistTokens, MemoryOnlyTokens} {
		opts, polls := graphFixture(t, "success")
		connected, err := ConnectGraph(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		paths, _ := graphPaths(opts.ConfigRoot, opts.ArchiveDir)
		data, _ := os.ReadFile(connected.CredentialPath)
		var credential graphCredential
		if err := json.Unmarshal(data, &credential); err != nil {
			t.Fatal(err)
		}
		credential.Expiry = time.Unix(1, 0)
		if err := storeGraphCredential(context.Background(), paths, credential, graphFileOps{}); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(connected.CredentialPath)
		loaded, err := LoadGraphToken(context.Background(), GraphLoadOptions{ConfigRoot: opts.ConfigRoot, ArchiveDir: opts.ArchiveDir, IdentityBaseURL: opts.IdentityBaseURL, HTTPClient: opts.HTTPClient, Persistence: policy})
		if err != nil {
			t.Fatal(err)
		}
		token, err := loaded.TokenSource.Token()
		if err != nil || token.AccessToken != "ROTATED-ACCESS" || token.RefreshToken != "ROTATED-REFRESH" {
			t.Fatalf("refresh error=%v", err)
		}
		after, _ := os.ReadFile(connected.CredentialPath)
		if policy == MemoryOnlyTokens && string(before) != string(after) {
			t.Fatal("memory-only refresh wrote credentials")
		}
		if policy == PersistTokens && (!strings.Contains(string(after), "ROTATED-REFRESH") || string(before) == string(after)) {
			t.Fatal("rotated refresh token was returned before persistence")
		}
		if _, err := loaded.TokenSource.Token(); err != nil {
			t.Fatal(err)
		}
		if polls.Load() != 2 {
			t.Fatalf("refresh repeated instead of reuse: %d", polls.Load())
		}
	}
}

func TestGraphCredentialRollbackAfterDirectorySyncFailure(t *testing.T) {
	for _, existing := range []bool{false, true} {
		paths, err := graphPaths(t.TempDir(), "acct")
		if err != nil {
			t.Fatal(err)
		}
		credential := graphCredential{Schema: "mailmd-graph-token/1", ClientID: graphFixtureClient, Tenant: "consumers", Scopes: graphScopes(), AccessToken: "OLD-ACCESS", RefreshToken: "OLD-REFRESH", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
		var before []byte
		if existing {
			if err := storeGraphCredential(context.Background(), paths, credential, graphFileOps{}); err != nil {
				t.Fatal(err)
			}
			before, _ = os.ReadFile(paths.CredentialPath)
		}
		credential.AccessToken = "NEW-ACCESS"
		calls := 0
		injected := errors.New("injected post-rename directory sync")
		ops := graphFileOps{syncDir: func(path string) error {
			if path == paths.CredentialDir {
				calls++
				if calls == 2 {
					return injected
				}
			}
			return privatefs.SyncDir(path)
		}}
		if err := storeGraphCredential(context.Background(), paths, credential, ops); !errors.Is(err, injected) {
			t.Fatalf("sync failure=%v", err)
		}
		after, err := os.ReadFile(paths.CredentialPath)
		if existing && (err != nil || string(before) != string(after)) {
			t.Fatal("failed replacement changed prior credential")
		}
		if !existing && !os.IsNotExist(err) {
			t.Fatal("failed new write left a credential")
		}
	}
}

func TestGraphRefreshPersistenceFailureBlocksTokenAndRetriesPersistence(t *testing.T) {
	opts, polls := graphFixture(t, "success")
	connected, err := ConnectGraph(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := graphPaths(opts.ConfigRoot, opts.ArchiveDir)
	data, _ := os.ReadFile(connected.CredentialPath)
	var credential graphCredential
	_ = json.Unmarshal(data, &credential)
	credential.Expiry = time.Unix(1, 0)
	if err := storeGraphCredential(context.Background(), paths, credential, graphFileOps{}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(connected.CredentialPath)
	injected := errors.New("injected refresh file sync")
	calls := 0
	loaded, err := LoadGraphToken(context.Background(), GraphLoadOptions{ConfigRoot: opts.ConfigRoot, ArchiveDir: opts.ArchiveDir, IdentityBaseURL: opts.IdentityBaseURL, HTTPClient: opts.HTTPClient, files: graphFileOps{syncFile: func(file *os.File) error {
		calls++
		if calls == 1 {
			return injected
		}
		return file.Sync()
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if token, err := loaded.TokenSource.Token(); token != nil || !errors.Is(err, injected) {
		t.Fatalf("persistence failure returned token: %v, %v", token, err)
	}
	after, _ := os.ReadFile(connected.CredentialPath)
	if string(before) != string(after) {
		t.Fatal("failed refresh changed stored credential")
	}
	if token, err := loaded.TokenSource.Token(); err != nil || token.AccessToken != "ROTATED-ACCESS" {
		t.Fatalf("persistence replay: %v", err)
	}
	if polls.Load() != 2 {
		t.Fatal("reused revoked old refresh token instead of persisting pending rotation")
	}
}

func TestGraphOptionsAndStoredScopeFailuresBeforeNetwork(t *testing.T) {
	for _, opts := range []GraphConnectOptions{{ClientID: "invalid"}, {ClientID: graphFixtureClient, Tenant: "../tenant"}, {ClientID: graphFixtureClient, IdentityBaseURL: "http://evil.example"}} {
		opts.ConfigRoot = t.TempDir()
		opts.ArchiveDir = "acct"
		opts.HandleDeviceCode = func(context.Context, DeviceCodePrompt) error {
			t.Fatal("invalid options started authorization")
			return nil
		}
		if _, err := ConnectGraph(context.Background(), opts); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("invalid options=%v", err)
		}
	}
	paths, _ := graphPaths(t.TempDir(), "acct")
	credential := graphCredential{Schema: "mailmd-graph-token/1", ClientID: graphFixtureClient, Tenant: "consumers", Scopes: []string{"openid"}, AccessToken: "OLD-ACCESS", RefreshToken: "OLD-REFRESH", Expiry: time.Now().Add(time.Hour)}
	if err := storeGraphCredential(context.Background(), paths, credential, graphFileOps{}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGraphToken(context.Background(), GraphLoadOptions{ConfigRoot: paths.ConfigRoot, ArchiveDir: paths.ArchiveDir}); !errors.Is(err, backend.ErrReconnectRequired) {
		t.Fatalf("insufficient stored scope=%v", err)
	}
}

func TestGraphAcceptsTheCurrentDeviceLoginPage(t *testing.T) {
	opts, _ := graphFixture(t, "device-2026")
	var shown DeviceCodePrompt
	opts.HandleDeviceCode = func(_ context.Context, p DeviceCodePrompt) error {
		shown = p
		return nil
	}
	if _, err := ConnectGraph(context.Background(), opts); err != nil {
		t.Fatalf("connect with Microsoft's current device answer: %v", err)
	}
	if shown.VerificationURI != "https://login.microsoft.com/device" || shown.UserCode != "F7KQ2MXTB" {
		t.Fatalf("shown %#v", shown)
	}
}
