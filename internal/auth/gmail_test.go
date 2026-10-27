package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/backend"
)

func TestConnectGmailLoopbackStoresPrivateCredential(t *testing.T) {
	root := t.TempDir()
	clientPath := writeOAuthClient(t, "client-secret")
	tokenEndpoint := newFakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "authorization_code" {
			t.Errorf("grant_type = %q, want authorization_code", got)
		}
		if got := r.Form.Get("code"); got != "code-1" {
			t.Errorf("code = %q, want code-1", got)
		}
		if got := r.Form.Get("code_verifier"); got == "" {
			t.Errorf("code_verifier is empty")
		}
		writeTokenJSON(w, map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         GmailReadonlyScope,
		})
	})

	var handledURL string
	result, err := ConnectGmail(context.Background(), GmailOptions{
		ConfigRoot:      root,
		ArchiveDir:      "gmail.connect-run_1",
		OAuthClientPath: clientPath,
		HTTPClient:      tokenEndpoint.Client(),
		HandleAuthURL: func(ctx context.Context, rawurl string) error {
			handledURL = rawurl
			assertAuthURL(t, rawurl)
			return nil
		},
		OpenBrowser: func(ctx context.Context, rawurl string) error {
			driveOAuthCallback(t, rawurl, "code-1", "", "")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ConnectGmail: %v", err)
	}
	if handledURL == "" {
		t.Fatal("HandleAuthURL was not called")
	}
	paths, err := resolveGmailCredentialPaths(root, "gmail.connect-run_1")
	if err != nil {
		t.Fatalf("credential paths: %v", err)
	}
	if result.CredentialPath != paths.CredentialPath {
		t.Fatalf("CredentialPath = %q, want %q", result.CredentialPath, paths.CredentialPath)
	}
	if result.ArchiveDir != "gmail.connect-run_1" {
		t.Fatalf("ArchiveDir = %q", result.ArchiveDir)
	}
	assertContainsAll(t, result.SecretValues, "client-secret", "access-1", "refresh-1")
	if result.TokenSource == nil {
		t.Fatal("TokenSource is nil")
	}
	if tokenEndpoint.Count() != 1 {
		t.Fatalf("token exchanges = %d, want 1", tokenEndpoint.Count())
	}

	stored := readStoredCredential(t, paths.CredentialPath)
	if stored.ClientID != "client-id" || stored.ClientSecret != "client-secret" {
		t.Fatalf("stored client fields = %#v", stored)
	}
	if stored.AccessToken != "access-1" || stored.RefreshToken != "refresh-1" {
		t.Fatalf("stored token fields = %#v", stored)
	}
	assertPrivateMode(t, paths.AuthRoot, 0o700)
	assertPrivateMode(t, paths.CredentialDir, 0o700)
	assertPrivateMode(t, paths.CredentialPath, 0o600)
}

func TestConnectGmailNilBrowserAndBrowserFailure(t *testing.T) {
	t.Run("nil browser uses handled URL", func(t *testing.T) {
		root := t.TempDir()
		tokenEndpoint := successfulTokenEndpoint(t, "access-nil", "refresh-nil")
		result, err := ConnectGmail(context.Background(), GmailOptions{
			ConfigRoot:      root,
			ArchiveDir:      "account.connect-run",
			OAuthClientPath: writeOAuthClient(t, "client-secret"),
			HTTPClient:      tokenEndpoint.Client(),
			HandleAuthURL: func(ctx context.Context, rawurl string) error {
				driveOAuthCallback(t, rawurl, "code-nil", "", "")
				return nil
			},
		})
		if err != nil {
			t.Fatalf("ConnectGmail: %v", err)
		}
		if result.TokenSource == nil {
			t.Fatal("TokenSource is nil")
		}
	})

	t.Run("browser failure does not abort", func(t *testing.T) {
		root := t.TempDir()
		tokenEndpoint := successfulTokenEndpoint(t, "access-browser", "refresh-browser")
		result, err := ConnectGmail(context.Background(), GmailOptions{
			ConfigRoot:      root,
			ArchiveDir:      "account.connect-run",
			OAuthClientPath: writeOAuthClient(t, "client-secret"),
			HTTPClient:      tokenEndpoint.Client(),
			HandleAuthURL: func(ctx context.Context, rawurl string) error {
				return nil
			},
			OpenBrowser: func(ctx context.Context, rawurl string) error {
				driveOAuthCallback(t, rawurl, "code-browser", "", "")
				return errors.New("browser unavailable")
			},
		})
		if err != nil {
			t.Fatalf("ConnectGmail: %v", err)
		}
		if result.TokenSource == nil {
			t.Fatal("TokenSource is nil")
		}
	})
}

func TestConnectGmailValidationAndCallbackFailures(t *testing.T) {
	t.Run("missing HandleAuthURL", func(t *testing.T) {
		_, err := ConnectGmail(context.Background(), GmailOptions{})
		if !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("ConnectGmail error = %v, want ErrInvalidOptions", err)
		}
	})

	t.Run("HandleAuthURL error aborts before browser", func(t *testing.T) {
		opened := false
		_, err := ConnectGmail(context.Background(), GmailOptions{
			ConfigRoot:      t.TempDir(),
			ArchiveDir:      "acct",
			OAuthClientPath: writeOAuthClient(t, "client-secret"),
			HandleAuthURL: func(ctx context.Context, rawurl string) error {
				return errors.New("output failed")
			},
			OpenBrowser: func(ctx context.Context, rawurl string) error {
				opened = true
				return nil
			},
		})
		if !errors.Is(err, ErrOAuthSetup) {
			t.Fatalf("ConnectGmail error = %v, want ErrOAuthSetup", err)
		}
		if opened {
			t.Fatal("OpenBrowser was called after HandleAuthURL failed")
		}
	})

	t.Run("context cancellation closes listener", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		var authURL string
		_, err := ConnectGmail(ctx, GmailOptions{
			ConfigRoot:      t.TempDir(),
			ArchiveDir:      "acct",
			OAuthClientPath: writeOAuthClient(t, "client-secret"),
			HandleAuthURL: func(ctx context.Context, rawurl string) error {
				authURL = rawurl
				return nil
			},
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ConnectGmail error = %v, want context deadline", err)
		}
		redirect := authURLParam(t, authURL, "redirect_uri")
		client := &http.Client{Timeout: 200 * time.Millisecond}
		if resp, getErr := client.Get(redirect); getErr == nil {
			_ = resp.Body.Close()
			t.Fatal("callback listener still accepted requests after cancellation")
		}
	})

	t.Run("wrong state does not complete flow", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := ConnectGmail(ctx, GmailOptions{
			ConfigRoot:      t.TempDir(),
			ArchiveDir:      "acct",
			OAuthClientPath: writeOAuthClient(t, "client-secret"),
			HandleAuthURL: func(ctx context.Context, rawurl string) error {
				return nil
			},
			OpenBrowser: func(ctx context.Context, rawurl string) error {
				driveOAuthCallback(t, rawurl, "code-wrong", "wrong-state", "")
				return nil
			},
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ConnectGmail error = %v, want context deadline", err)
		}
	})

	for _, tt := range []struct {
		name       string
		oauthErr   string
		omitCode   bool
		wantInText string
	}{
		{name: "oauth error", oauthErr: "access_denied", wantInText: "access_denied"},
		{name: "missing code", omitCode: true, wantInText: "code"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ConnectGmail(context.Background(), GmailOptions{
				ConfigRoot:      t.TempDir(),
				ArchiveDir:      "acct",
				OAuthClientPath: writeOAuthClient(t, "client-secret"),
				HandleAuthURL: func(ctx context.Context, rawurl string) error {
					return nil
				},
				OpenBrowser: func(ctx context.Context, rawurl string) error {
					code := "code"
					if tt.omitCode {
						code = ""
					}
					driveOAuthCallback(t, rawurl, code, "", tt.oauthErr)
					return nil
				},
			})
			if !errors.Is(err, ErrOAuthSetup) {
				t.Fatalf("ConnectGmail error = %v, want ErrOAuthSetup", err)
			}
			if tt.wantInText != "" && !strings.Contains(err.Error(), tt.wantInText) {
				t.Fatalf("ConnectGmail error = %v, want %q", err, tt.wantInText)
			}
		})
	}
}

func TestConnectGmailDuplicateCallbackExchangesOnce(t *testing.T) {
	tokenEndpoint := successfulTokenEndpoint(t, "access-dup", "refresh-dup")
	_, err := ConnectGmail(context.Background(), GmailOptions{
		ConfigRoot:      t.TempDir(),
		ArchiveDir:      "acct",
		OAuthClientPath: writeOAuthClient(t, "client-secret"),
		HTTPClient:      tokenEndpoint.Client(),
		HandleAuthURL: func(ctx context.Context, rawurl string) error {
			return nil
		},
		OpenBrowser: func(ctx context.Context, rawurl string) error {
			driveOAuthCallback(t, rawurl, "code-dup", "", "")
			driveOAuthCallback(t, rawurl, "code-dup", "", "")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ConnectGmail: %v", err)
	}
	if got := tokenEndpoint.Count(); got != 1 {
		t.Fatalf("token exchanges = %d, want 1", got)
	}
}

func TestConnectGmailMissingRefreshTokenFailsWithoutCredential(t *testing.T) {
	root := t.TempDir()
	tokenEndpoint := newFakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		writeTokenJSON(w, map[string]any{
			"access_token": "access-only",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"scope":        GmailReadonlyScope,
		})
	})
	_, err := ConnectGmail(context.Background(), GmailOptions{
		ConfigRoot:      root,
		ArchiveDir:      "acct",
		OAuthClientPath: writeOAuthClient(t, "client-secret"),
		HTTPClient:      tokenEndpoint.Client(),
		HandleAuthURL: func(ctx context.Context, rawurl string) error {
			return nil
		},
		OpenBrowser: func(ctx context.Context, rawurl string) error {
			driveOAuthCallback(t, rawurl, "code", "", "")
			return nil
		},
	})
	if !errors.Is(err, ErrOAuthSetup) {
		t.Fatalf("ConnectGmail error = %v, want ErrOAuthSetup", err)
	}
	paths, pathErr := resolveGmailCredentialPaths(root, "acct")
	if pathErr != nil {
		t.Fatalf("credential paths: %v", pathErr)
	}
	if _, statErr := os.Stat(paths.CredentialPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("credential file exists after setup failure: %v", statErr)
	}
}

func TestConnectGmailRejectsZeroExpiryAccessTokenWithoutCredential(t *testing.T) {
	root := t.TempDir()
	tokenEndpoint := newFakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		writeTokenJSON(w, map[string]any{
			"access_token":  "access-without-expiry",
			"refresh_token": "refresh-token",
			"token_type":    "Bearer",
			"scope":         GmailReadonlyScope,
		})
	})
	_, err := ConnectGmail(context.Background(), GmailOptions{
		ConfigRoot:      root,
		ArchiveDir:      "acct",
		OAuthClientPath: writeOAuthClient(t, "client-secret"),
		HTTPClient:      tokenEndpoint.Client(),
		HandleAuthURL: func(ctx context.Context, rawurl string) error {
			return nil
		},
		OpenBrowser: func(ctx context.Context, rawurl string) error {
			driveOAuthCallback(t, rawurl, "code", "", "")
			return nil
		},
	})
	if !errors.Is(err, ErrOAuthSetup) {
		t.Fatalf("ConnectGmail error = %v, want ErrOAuthSetup", err)
	}
	paths := mustCredentialPaths(t, root, "acct")
	if _, statErr := os.Stat(paths.CredentialPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("credential file exists after zero-expiry setup failure: %v", statErr)
	}
}

func TestDesktopClientJSONValidation(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "malformed", json: `{"installed":`},
		{name: "web client", json: `{"web":{"client_id":"id","client_secret":"client-secret","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token"}}`},
		{name: "service account", json: `{"type":"service_account","client_email":"x@example.com","private_key":"client-secret"}`},
		{name: "authorized user", json: `{"type":"authorized_user","client_id":"id","client_secret":"client-secret","refresh_token":"refresh"}`},
		{name: "external account", json: `{"type":"external_account","audience":"client-secret","credential_source":{"file":"refresh"}}`},
		{name: "unknown shape", json: `{"desktop":{"client_id":"id","client_secret":"client-secret"}}`},
		{name: "missing client id", json: `{"installed":{"client_secret":"client-secret","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token"}}`},
		{name: "missing client secret", json: `{"installed":{"client_id":"id","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDesktopClientJSON([]byte(tt.json))
			if !errors.Is(err, ErrInvalidOAuthClient) {
				t.Fatalf("parseDesktopClientJSON error = %v, want ErrInvalidOAuthClient", err)
			}
			assertNoLeak(t, err, "client-secret", "private_key", "refresh")
		})
	}
}

func TestProductionEndpointValidationRejectsUnsafeEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		authURI  string
		tokenURI string
	}{
		{name: "non https auth", authURI: "http://accounts.google.com/o/oauth2/auth", tokenURI: googleTokenURI},
		{name: "non google host", authURI: "https://evil.example/o/oauth2/auth", tokenURI: googleTokenURI},
		{name: "auth query", authURI: "https://accounts.google.com/o/oauth2/auth?next=https://evil.example", tokenURI: googleTokenURI},
		{name: "token mismatch", authURI: "https://accounts.google.com/o/oauth2/auth", tokenURI: "https://evil.example/token"},
		{name: "userinfo", authURI: "https://evil@accounts.google.com/o/oauth2/auth", tokenURI: googleTokenURI},
		{name: "fragment", authURI: "https://accounts.google.com/o/oauth2/auth#frag", tokenURI: googleTokenURI},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"installed":{"client_id":"id","client_secret":"client-secret","auth_uri":%q,"token_uri":%q}}`, tt.authURI, tt.tokenURI)
			_, err := parseDesktopClientJSON([]byte(body))
			if !errors.Is(err, ErrInvalidOAuthClient) {
				t.Fatalf("parseDesktopClientJSON error = %v, want ErrInvalidOAuthClient", err)
			}
			assertNoLeak(t, err, "client-secret", "evil.example")
		})
	}
}

func TestCredentialPathValidationAndPrivateStore(t *testing.T) {
	root := t.TempDir()
	paths, err := resolveGmailCredentialPaths(root, "gmail.connect-run_1")
	if err != nil {
		t.Fatalf("resolveGmailCredentialPaths: %v", err)
	}
	want := filepath.Join(absPath(t, root), "mailmd", "gmail.connect-run_1", gmailTokenFilename)
	if paths.CredentialPath != want {
		t.Fatalf("CredentialPath = %q, want %q", paths.CredentialPath, want)
	}

	if _, err := resolveGmailCredentialPaths(root, "account.connect-abc123"); err != nil {
		t.Fatalf("safe staging archive dir rejected: %v", err)
	}

	for _, archiveDir := range []string{
		"", ".", "..", "a/b", `a\b`, filepath.Join(root, "abs"), strings.Repeat("a", 121), "bad:name", " padded",
	} {
		t.Run("reject "+archiveDir, func(t *testing.T) {
			_, err := resolveGmailCredentialPaths(root, archiveDir)
			if err == nil {
				t.Fatalf("resolveGmailCredentialPaths(%q) succeeded, want error", archiveDir)
			}
		})
	}

	cred := validCredential()
	if err := storeGmailCredential(context.Background(), paths, cred); err != nil {
		t.Fatalf("storeGmailCredential: %v", err)
	}
	assertPrivateMode(t, paths.AuthRoot, 0o700)
	assertPrivateMode(t, paths.CredentialDir, 0o700)
	assertPrivateMode(t, paths.CredentialPath, 0o600)
}

func TestCredentialPermissionAndFilesystemSafety(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission checks are not enforced on Windows")
	}
	t.Run("broad directory permissions fail closed", func(t *testing.T) {
		root := t.TempDir()
		paths := mustCredentialPaths(t, root, "acct")
		if err := os.MkdirAll(paths.AuthRoot, 0o755); err != nil {
			t.Fatalf("mkdir auth root: %v", err)
		}
		if err := os.Chmod(paths.AuthRoot, 0o755); err != nil {
			t.Fatalf("chmod auth root: %v", err)
		}
		_, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct"})
		if !errors.Is(err, ErrInsecurePermission) {
			t.Fatalf("LoadGmailToken error = %v, want ErrInsecurePermission", err)
		}
	})

	t.Run("broad file permissions fail closed", func(t *testing.T) {
		root := t.TempDir()
		paths := mustCredentialPaths(t, root, "acct")
		writeCredentialFile(t, paths, validCredential(), 0o644)
		_, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct"})
		if !errors.Is(err, ErrInsecurePermission) {
			t.Fatalf("LoadGmailToken error = %v, want ErrInsecurePermission", err)
		}
	})

	t.Run("symlink directory fails", func(t *testing.T) {
		root := t.TempDir()
		paths := mustCredentialPaths(t, root, "acct")
		if err := os.Symlink(t.TempDir(), paths.AuthRoot); err != nil {
			t.Fatalf("symlink auth root: %v", err)
		}
		_, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct"})
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("LoadGmailToken error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("symlink credential file fails", func(t *testing.T) {
		root := t.TempDir()
		paths := mustCredentialPaths(t, root, "acct")
		if err := os.MkdirAll(paths.CredentialDir, 0o700); err != nil {
			t.Fatalf("mkdir credential dir: %v", err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "token"), paths.CredentialPath); err != nil {
			t.Fatalf("symlink credential: %v", err)
		}
		_, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct"})
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("LoadGmailToken error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("non regular credential file fails", func(t *testing.T) {
		root := t.TempDir()
		paths := mustCredentialPaths(t, root, "acct")
		if err := os.MkdirAll(paths.CredentialPath, 0o700); err != nil {
			t.Fatalf("mkdir credential path: %v", err)
		}
		_, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct"})
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("LoadGmailToken error = %v, want ErrUnsafePath", err)
		}
	})
}

func TestAtomicCredentialWriteFailureLeavesExistingCredential(t *testing.T) {
	root := t.TempDir()
	paths := mustCredentialPaths(t, root, "acct")
	oldCred := validCredential()
	oldCred.AccessToken = "old-access"
	if err := storeGmailCredential(context.Background(), paths, oldCred); err != nil {
		t.Fatalf("initial store: %v", err)
	}

	renameErr := errors.New("rename failed")
	origRename := renameCredentialFile
	renameCredentialFile = func(oldpath, newpath string) error {
		return renameErr
	}
	t.Cleanup(func() { renameCredentialFile = origRename })

	newCred := validCredential()
	newCred.AccessToken = "new-access"
	err := storeGmailCredential(context.Background(), paths, newCred)
	if !errors.Is(err, renameErr) {
		t.Fatalf("storeGmailCredential error = %v, want rename failure", err)
	}
	stored := readStoredCredential(t, paths.CredentialPath)
	if stored.AccessToken != "old-access" {
		t.Fatalf("stored access token = %q, want old-access", stored.AccessToken)
	}
	for _, entry := range readDir(t, paths.CredentialDir) {
		if strings.HasPrefix(entry.Name(), ".gmail-token-") {
			t.Fatalf("temporary credential file was not removed: %s", entry.Name())
		}
	}
}

func TestLoadGmailTokenMissingAndSecrets(t *testing.T) {
	root := t.TempDir()
	_, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct"})
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("LoadGmailToken missing error = %v, want ErrCredentialNotFound", err)
	}
	if errors.Is(err, backend.ErrReconnectRequired) || errors.Is(err, backend.ErrRetryable) {
		t.Fatalf("missing credential misclassified: %v", err)
	}

	paths := mustCredentialPaths(t, root, "acct")
	cred := validCredential()
	if err := storeGmailCredential(context.Background(), paths, cred); err != nil {
		t.Fatalf("storeGmailCredential: %v", err)
	}
	token, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct"})
	if err != nil {
		t.Fatalf("LoadGmailToken: %v", err)
	}
	if token.TokenSource == nil {
		t.Fatal("TokenSource is nil")
	}
	assertContainsAll(t, token.SecretValues, cred.ClientSecret, cred.AccessToken, cred.RefreshToken)
}

func TestLoadGmailTokenRejectsStoredAccessTokenWithZeroExpiry(t *testing.T) {
	root := t.TempDir()
	paths := mustCredentialPaths(t, root, "acct")
	cred := validCredential()
	cred.TokenExpiry = time.Time{}
	if err := storeGmailCredential(context.Background(), paths, cred); err != nil {
		t.Fatalf("storeGmailCredential: %v", err)
	}
	_, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct"})
	if !errors.Is(err, ErrOAuthSetup) {
		t.Fatalf("LoadGmailToken error = %v, want ErrOAuthSetup", err)
	}
}

func TestLoadedTokenRefreshesAndPersists(t *testing.T) {
	t.Run("empty access token refreshes on first use", func(t *testing.T) {
		root := t.TempDir()
		paths := mustCredentialPaths(t, root, "acct")
		cred := validCredential()
		cred.AccessToken = ""
		cred.TokenExpiry = time.Time{}
		if err := storeGmailCredential(context.Background(), paths, cred); err != nil {
			t.Fatalf("storeGmailCredential: %v", err)
		}
		tokenEndpoint := successfulTokenEndpoint(t, "fresh-access", "")
		loaded, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct", HTTPClient: tokenEndpoint.Client()})
		if err != nil {
			t.Fatalf("LoadGmailToken: %v", err)
		}
		tok, err := loaded.TokenSource.Token()
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok.AccessToken != "fresh-access" {
			t.Fatalf("AccessToken = %q, want fresh-access", tok.AccessToken)
		}
	})

	t.Run("expired token persists replacement refresh token", func(t *testing.T) {
		root := t.TempDir()
		paths := mustCredentialPaths(t, root, "acct")
		cred := validCredential()
		cred.AccessToken = "old-access"
		cred.RefreshToken = "old-refresh"
		cred.TokenExpiry = time.Now().Add(-time.Hour)
		if err := storeGmailCredential(context.Background(), paths, cred); err != nil {
			t.Fatalf("storeGmailCredential: %v", err)
		}
		tokenEndpoint := successfulTokenEndpoint(t, "new-access", "new-refresh")
		loaded, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct", HTTPClient: tokenEndpoint.Client()})
		if err != nil {
			t.Fatalf("LoadGmailToken: %v", err)
		}
		tok, err := loaded.TokenSource.Token()
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok.AccessToken != "new-access" || tok.RefreshToken != "new-refresh" {
			t.Fatalf("refreshed token = %#v", tok)
		}
		stored := readStoredCredential(t, paths.CredentialPath)
		if stored.AccessToken != "new-access" || stored.RefreshToken != "new-refresh" {
			t.Fatalf("stored refreshed credential = %#v", stored)
		}
		assertPrivateMode(t, paths.CredentialPath, 0o600)
	})
}

func TestRefreshRejectsZeroExpiryAccessTokenWithoutPersisting(t *testing.T) {
	root := t.TempDir()
	paths := mustCredentialPaths(t, root, "acct")
	cred := validCredential()
	cred.AccessToken = "old-access"
	cred.RefreshToken = "old-refresh"
	cred.TokenExpiry = time.Now().Add(-time.Hour)
	if err := storeGmailCredential(context.Background(), paths, cred); err != nil {
		t.Fatalf("storeGmailCredential: %v", err)
	}
	tokenEndpoint := newFakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		writeTokenJSON(w, map[string]any{
			"access_token": "bad-access-without-expiry",
			"token_type":   "Bearer",
			"scope":        GmailReadonlyScope,
		})
	})
	loaded, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct", HTTPClient: tokenEndpoint.Client()})
	if err != nil {
		t.Fatalf("LoadGmailToken: %v", err)
	}
	_, err = loaded.TokenSource.Token()
	if !errors.Is(err, ErrOAuthSetup) {
		t.Fatalf("Token error = %v, want ErrOAuthSetup", err)
	}
	stored := readStoredCredential(t, paths.CredentialPath)
	if stored.AccessToken != "old-access" || stored.RefreshToken != "old-refresh" {
		t.Fatalf("stored credential changed after invalid refresh: %#v", stored)
	}
	if !stored.TokenExpiry.Equal(cred.TokenExpiry) {
		t.Fatalf("stored expiry changed after invalid refresh: %v want %v", stored.TokenExpiry, cred.TokenExpiry)
	}
}

func TestInvalidRefreshRequiresReconnectAndDoesNotLeakSecrets(t *testing.T) {
	root := t.TempDir()
	paths := mustCredentialPaths(t, root, "acct")
	cred := validCredential()
	cred.ClientSecret = "client-secret-leak"
	cred.AccessToken = "access-secret-leak"
	cred.RefreshToken = "refresh-secret-leak"
	cred.TokenExpiry = time.Now().Add(-time.Hour)
	if err := storeGmailCredential(context.Background(), paths, cred); err != nil {
		t.Fatalf("storeGmailCredential: %v", err)
	}
	tokenEndpoint := newFakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"refresh token revoked body-secret-leak"}`)
	})
	loaded, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct", HTTPClient: tokenEndpoint.Client()})
	if err != nil {
		t.Fatalf("LoadGmailToken: %v", err)
	}
	_, err = loaded.TokenSource.Token()
	if !errors.Is(err, backend.ErrReconnectRequired) {
		t.Fatalf("Token error = %v, want ErrReconnectRequired", err)
	}
	assertNoLeak(t, err, "client-secret-leak", "access-secret-leak", "refresh-secret-leak", "body-secret-leak")
}

func TestNonReconnectRefreshErrorDoesNotLeakRetrieveBody(t *testing.T) {
	root := t.TempDir()
	paths := mustCredentialPaths(t, root, "acct")
	cred := validCredential()
	cred.ClientSecret = "client-secret-leak"
	cred.AccessToken = "access-secret-leak"
	cred.RefreshToken = "refresh-secret-leak"
	cred.TokenExpiry = time.Now().Add(-time.Hour)
	if err := storeGmailCredential(context.Background(), paths, cred); err != nil {
		t.Fatalf("storeGmailCredential: %v", err)
	}
	tokenEndpoint := newFakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate_limited","error_description":"body-secret-leak"}`)
	})
	loaded, err := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct", HTTPClient: tokenEndpoint.Client()})
	if err != nil {
		t.Fatalf("LoadGmailToken: %v", err)
	}
	_, err = loaded.TokenSource.Token()
	if err == nil {
		t.Fatal("Token error = nil, want sanitized refresh error")
	}
	if errors.Is(err, backend.ErrReconnectRequired) {
		t.Fatalf("Token error = %v, did not want ErrReconnectRequired", err)
	}
	assertNoLeak(t, err, "client-secret-leak", "access-secret-leak", "refresh-secret-leak", "body-secret-leak")
	if !strings.Contains(err.Error(), "rate_limited") {
		t.Fatalf("Token error = %v, want sanitized OAuth error code", err)
	}
}

type fakeTokenEndpoint struct {
	server *httptest.Server
	client *http.Client
	mu     sync.Mutex
	count  int
}

func newFakeTokenEndpoint(t *testing.T, handler http.HandlerFunc) *fakeTokenEndpoint {
	t.Helper()
	endpoint := &fakeTokenEndpoint{}
	endpoint.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint.mu.Lock()
		endpoint.count++
		endpoint.mu.Unlock()
		handler(w, r)
	}))
	target, err := url.Parse(endpoint.server.URL)
	if err != nil {
		t.Fatalf("parse token server URL: %v", err)
	}
	endpoint.client = &http.Client{
		Transport: rewriteTokenTransport{target: target, base: http.DefaultTransport},
		Timeout:   2 * time.Second,
	}
	t.Cleanup(endpoint.server.Close)
	return endpoint
}

func successfulTokenEndpoint(t *testing.T, accessToken, refreshToken string) *fakeTokenEndpoint {
	t.Helper()
	return newFakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "authorization_code" && got != "refresh_token" {
			t.Errorf("grant_type = %q", got)
		}
		body := map[string]any{
			"access_token": accessToken,
			"token_type":   "Bearer",
			"expires_in":   3600,
			"scope":        GmailReadonlyScope,
		}
		if refreshToken != "" {
			body["refresh_token"] = refreshToken
		}
		writeTokenJSON(w, body)
	})
}

func (e *fakeTokenEndpoint) Client() *http.Client {
	return e.client
}

func (e *fakeTokenEndpoint) Count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.count
}

type rewriteTokenTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt rewriteTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "oauth2.googleapis.com" || req.URL.Path != "/token" {
		return nil, fmt.Errorf("unexpected outbound request to %s", req.URL.Redacted())
	}
	clone := req.Clone(req.Context())
	u := *clone.URL
	u.Scheme = rt.target.Scheme
	u.Host = rt.target.Host
	u.Path = rt.target.Path
	u.RawPath = rt.target.RawPath
	clone.URL = &u
	clone.Host = rt.target.Host
	return rt.base.RoundTrip(clone)
}

func writeTokenJSON(w http.ResponseWriter, value map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeOAuthClient(t *testing.T, secret string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "client_secret.json")
	body := fmt.Sprintf(`{
  "installed": {
    "client_id": "client-id",
    "client_secret": %q,
    "auth_uri": "https://accounts.google.com/o/oauth2/auth",
    "token_uri": "https://oauth2.googleapis.com/token",
    "redirect_uris": ["http://localhost"]
  }
}`, secret)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write OAuth client: %v", err)
	}
	return path
}

func assertAuthURL(t *testing.T, rawurl string) {
	t.Helper()
	u, err := url.Parse(rawurl)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	q := u.Query()
	if u.Scheme != "https" || u.Host != "accounts.google.com" {
		t.Fatalf("auth URL endpoint = %s://%s", u.Scheme, u.Host)
	}
	if got := q.Get("scope"); got != GmailReadonlyScope {
		t.Fatalf("scope = %q, want %q", got, GmailReadonlyScope)
	}
	if got := q.Get("access_type"); got != "offline" {
		t.Fatalf("access_type = %q, want offline", got)
	}
	if got := q.Get("prompt"); got != "consent" {
		t.Fatalf("prompt = %q, want consent", got)
	}
	for _, key := range []string{"state", "code_challenge", "redirect_uri"} {
		if q.Get(key) == "" {
			t.Fatalf("%s is empty in auth URL", key)
		}
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", got)
	}
}

func driveOAuthCallback(t *testing.T, rawAuthURL, code, stateOverride, oauthErr string) {
	t.Helper()
	redirect := authURLParam(t, rawAuthURL, "redirect_uri")
	state := authURLParam(t, rawAuthURL, "state")
	if stateOverride != "" {
		state = stateOverride
	}
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatalf("parse redirect URI: %v", err)
	}
	q := u.Query()
	q.Set("state", state)
	if code != "" {
		q.Set("code", code)
	}
	if oauthErr != "" {
		q.Set("error", oauthErr)
	}
	u.RawQuery = q.Encode()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func authURLParam(t *testing.T, rawurl, key string) string {
	t.Helper()
	u, err := url.Parse(rawurl)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	value := u.Query().Get(key)
	if value == "" {
		t.Fatalf("auth URL param %q is empty", key)
	}
	return value
}

func mustCredentialPaths(t *testing.T, root, archiveDir string) gmailCredentialPaths {
	t.Helper()
	paths, err := resolveGmailCredentialPaths(root, archiveDir)
	if err != nil {
		t.Fatalf("resolveGmailCredentialPaths: %v", err)
	}
	return paths
}

func validCredential() gmailCredentialV1 {
	return gmailCredentialV1{
		SchemaVersion: 1,
		ClientID:      "client-id",
		ClientSecret:  "client-secret",
		AuthURI:       "https://accounts.google.com/o/oauth2/auth",
		TokenURI:      googleTokenURI,
		Scopes:        []string{GmailReadonlyScope},
		AccessToken:   "access-token",
		RefreshToken:  "refresh-token",
		TokenType:     "Bearer",
		TokenExpiry:   time.Now().Add(time.Hour).UTC(),
	}
}

func writeCredentialFile(t *testing.T, paths gmailCredentialPaths, cred gmailCredentialV1, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(paths.CredentialDir, 0o700); err != nil {
		t.Fatalf("mkdir credential dir: %v", err)
	}
	data, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	if err := os.WriteFile(paths.CredentialPath, append(data, '\n'), mode); err != nil {
		t.Fatalf("write credential: %v", err)
	}
	if err := os.Chmod(paths.CredentialPath, mode); err != nil {
		t.Fatalf("chmod credential: %v", err)
	}
}

func readStoredCredential(t *testing.T, path string) gmailCredentialV1 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credential: %v", err)
	}
	var cred gmailCredentialV1
	if err := json.Unmarshal(data, &cred); err != nil {
		t.Fatalf("unmarshal credential: %v", err)
	}
	return cred
}

func assertPrivateMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func assertContainsAll(t *testing.T, got []string, want ...string) {
	t.Helper()
	set := map[string]struct{}{}
	for _, value := range got {
		set[value] = struct{}{}
	}
	for _, value := range want {
		if _, ok := set[value]; !ok {
			t.Fatalf("values %#v missing %q", got, value)
		}
	}
}

func assertNoLeak(t *testing.T, err error, values ...string) {
	t.Helper()
	if err == nil {
		return
	}
	text := err.Error()
	for _, value := range values {
		if value != "" && strings.Contains(text, value) {
			t.Fatalf("error leaked %q: %v", value, err)
		}
	}
}

func absPath(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	return abs
}

func readDir(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("readdir %s: %v", path, err)
	}
	return entries
}

func TestWebClientFileGetsAClearMessage(t *testing.T) {
	_, err := parseDesktopClientJSON([]byte(`{"web":{"client_id":"id","client_secret":"s"}}`))
	if err == nil || !strings.Contains(err.Error(), "Web application") || !strings.Contains(err.Error(), "Desktop app") {
		t.Fatalf("error = %v", err)
	}
	_, err = CheckGmailClient(filepath.Join(t.TempDir(), "missing.json"))
	if !errors.Is(err, ErrInvalidOAuthClient) || !strings.Contains(err.Error(), "cannot read the client file") {
		t.Fatalf("missing file: %v", err)
	}
}

// On a computer without a browser the user pastes the address the browser
// ended up on.
func TestConnectGmailWithPastedAddress(t *testing.T) {
	root := t.TempDir()
	tokenEndpoint := newFakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		writeTokenJSON(w, map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "token_type": "Bearer", "expires_in": 3600, "scope": GmailReadonlyScope})
	})
	pasted := make(chan string, 1)
	_, err := ConnectGmail(context.Background(), GmailOptions{
		ConfigRoot:      root,
		ArchiveDir:      "gmail",
		OAuthClientPath: writeOAuthClient(t, "client-secret"),
		HTTPClient:      tokenEndpoint.Client(),
		HandleAuthURL: func(ctx context.Context, rawurl string) error {
			u, _ := url.Parse(rawurl)
			pasted <- "http://127.0.0.1:1/?state=" + url.QueryEscape(u.Query().Get("state")) + "&code=pasted-code&scope=x"
			return nil
		},
		ReadRedirect: func(ctx context.Context) (string, error) { return <-pasted + "\n", nil },
	})
	if err != nil {
		t.Fatalf("ConnectGmail: %v", err)
	}
	_, err = ConnectGmail(context.Background(), GmailOptions{
		ConfigRoot:      root,
		ArchiveDir:      "gmail",
		OAuthClientPath: writeOAuthClient(t, "client-secret"),
		HTTPClient:      tokenEndpoint.Client(),
		HandleAuthURL:   func(ctx context.Context, rawurl string) error { return nil },
		ReadRedirect:    func(ctx context.Context) (string, error) { return "http://127.0.0.1:1/?state=wrong&code=x", nil },
	})
	if !errors.Is(err, ErrOAuthSetup) || !strings.Contains(err.Error(), "does not belong to this sign-in") {
		t.Fatalf("wrong state: %v", err)
	}
}

func TestGraphSettingsAcceptTenantDomains(t *testing.T) {
	id := "12345678-1234-1234-1234-123456789abc"
	for _, tenant := range []string{"", "common", "organizations", "consumers", "contoso.onmicrosoft.com", "example.com", id} {
		if err := CheckGraphSettings(id, tenant); err != nil {
			t.Errorf("tenant %q: %v", tenant, err)
		}
	}
	for _, tenant := range []string{"contoso", "bad tenant", "../x", "http://x"} {
		if err := CheckGraphSettings(id, tenant); err == nil {
			t.Errorf("tenant %q was accepted", tenant)
		}
	}
	if err := CheckGraphSettings("not-a-guid", ""); err == nil || !strings.Contains(err.Error(), "--client-id") {
		t.Fatalf("client id: %v", err)
	}
}

func TestSavedGmailClientIsReadWithTheSameChecks(t *testing.T) {
	root := t.TempDir()
	if err := CheckSavedGmailClient(root, "personal"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("no login file: %v", err)
	}
	paths := mustCredentialPaths(t, root, "personal")
	if err := ensureCredentialDir(paths.AuthRoot, true); err != nil {
		t.Fatal(err)
	}
	// The tokens don't matter for a new sign-in, only the client does.
	cred := validCredential()
	cred.RefreshToken, cred.AccessToken = "", ""
	writeCredentialFile(t, paths, cred, 0o600)
	client, err := savedGmailClient(context.Background(), root, "personal")
	if err != nil || client.ClientID != "client-id" || client.ClientSecret != "client-secret" {
		t.Fatalf("saved client = %+v, %v", client, err)
	}
	cred.ClientSecret = ""
	writeCredentialFile(t, paths, cred, 0o600)
	if err := CheckSavedGmailClient(root, "personal"); !errors.Is(err, ErrOAuthSetup) {
		t.Fatalf("no client secret: %v", err)
	}
	if runtime.GOOS != "windows" {
		writeCredentialFile(t, paths, validCredential(), 0o644)
		if err := CheckSavedGmailClient(root, "personal"); !errors.Is(err, ErrInsecurePermission) {
			t.Fatalf("readable by others: %v", err)
		}
	}
}
