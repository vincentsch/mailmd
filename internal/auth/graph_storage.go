package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/privatefs"
	"golang.org/x/oauth2"
)

type graphCredential struct {
	Schema       string    `json:"schema"`
	ClientID     string    `json:"client_id"`
	Tenant       string    `json:"tenant"`
	Scopes       []string  `json:"scopes"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"token_expiry"`
}

type graphFileOps struct {
	syncFile func(*os.File) error
	syncDir  func(string) error
	rename   func(string, string) error
}

func (f graphFileOps) defaults() graphFileOps {
	if f.syncFile == nil {
		f.syncFile = (*os.File).Sync
	}
	if f.syncDir == nil {
		f.syncDir = privatefs.SyncDir
	}
	if f.rename == nil {
		f.rename = os.Rename
	}
	return f
}

func validateGraphCredential(credential graphCredential, now time.Time) error {
	if credential.Schema != "mailmd-graph-token/1" {
		return oauthSetup("the login file has an unknown format")
	}
	if _, err := graphConfiguration(credential.ClientID, credential.Tenant, ""); err != nil {
		return oauthSetup("the login file has an invalid client ID or tenant")
	}
	if credential.AccessToken == "" || credential.RefreshToken == "" || credential.Expiry.IsZero() ||
		(!scopeListContains(credential.Scopes, GraphReadonlyScope) && !scopeListContains(credential.Scopes, "Mail.Read")) ||
		(credential.TokenType != "" && !strings.EqualFold(credential.TokenType, "Bearer")) {
		return fmt.Errorf("auth: Microsoft credentials require reconnect: %w", backend.ErrReconnectRequired)
	}
	// An expired access token is fine here, because the refresh token renews
	// it. ConnectGraph checks the expiry of a new token itself, before it tests
	// the access and saves the login.
	return nil
}

func storeGraphCredential(ctx context.Context, paths gmailCredentialPaths, credential graphCredential, ops graphFileOps) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ops = ops.defaults()
	for _, dir := range []string{paths.AuthRoot, paths.CredentialDir} {
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return &PathError{Op: "mkdir", Path: dir, Err: err}
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				return &PathError{Op: "chmod", Path: dir, Err: err}
			}
		} else if err != nil {
			return &PathError{Op: "open", Path: dir, Err: err}
		} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privatefs.HasMode(info.Mode(), 0o700) {
			return &PathError{Op: "open", Path: dir, Err: ErrInsecurePermission}
		}
		if err := ops.syncDir(dir); err != nil {
			return &PathError{Op: "sync", Path: dir, Err: err}
		}
		if err := ops.syncDir(filepath.Dir(dir)); err != nil {
			return &PathError{Op: "sync", Path: filepath.Dir(dir), Err: err}
		}
	}
	if err := checkCredentialFile(paths.CredentialPath, false); err != nil {
		return err
	}
	data, err := json.Marshal(credential)
	if err != nil {
		return oauthSetup("the login could not be saved")
	}
	data = append(data, '\n')
	old, err := os.ReadFile(paths.CredentialPath)
	haveOld := err == nil
	if err != nil && !os.IsNotExist(err) {
		return &PathError{Op: "open", Path: paths.CredentialPath, Err: err}
	}
	writeTemp := func(data []byte) (string, error) {
		file, err := os.CreateTemp(paths.CredentialDir, ".graph-token-*")
		if err != nil {
			return "", err
		}
		name := file.Name()
		if err = file.Chmod(0o600); err == nil {
			var n int
			n, err = file.Write(data)
			if err == nil && n != len(data) {
				err = io.ErrShortWrite
			}
		}
		if err == nil {
			err = ops.syncFile(file)
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(name)
			return "", err
		}
		return name, nil
	}
	backup := ""
	if haveOld {
		backup, err = writeTemp(old)
		if err != nil {
			return &PathError{Op: "backup", Path: paths.CredentialPath, Err: err}
		}
		defer os.Remove(backup)
	}
	tmp, err := writeTemp(data)
	if err != nil {
		return &PathError{Op: "write", Path: paths.CredentialPath, Err: err}
	}
	defer os.Remove(tmp)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := ops.rename(tmp, paths.CredentialPath); err != nil {
		return &PathError{Op: "rename", Path: paths.CredentialPath, Err: err}
	}
	if err := ops.syncDir(paths.CredentialDir); err != nil {
		var restore error
		if haveOld {
			restore = ops.rename(backup, paths.CredentialPath)
		} else {
			restore = os.Remove(paths.CredentialPath)
		}
		if restore == nil {
			restore = ops.syncDir(paths.CredentialDir)
		}
		return &PathError{Op: "sync", Path: paths.CredentialDir, Err: errors.Join(err, restore)}
	}
	return nil
}

func LoadGraphToken(ctx context.Context, opts GraphLoadOptions) (GraphToken, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return GraphToken{}, ctx.Err()
	}
	if opts.Persistence != PersistTokens && opts.Persistence != MemoryOnlyTokens {
		return GraphToken{}, invalidOptions("invalid token persistence policy")
	}
	paths, err := graphPaths(opts.ConfigRoot, opts.ArchiveDir)
	if err != nil {
		return GraphToken{}, err
	}
	for _, dir := range []string{paths.AuthRoot, paths.CredentialDir} {
		if err := ensureCredentialDir(dir, false); err != nil {
			return GraphToken{}, err
		}
	}
	if err := checkCredentialFile(paths.CredentialPath, true); err != nil {
		return GraphToken{}, err
	}
	data, err := os.ReadFile(paths.CredentialPath)
	if err != nil {
		return GraphToken{}, &PathError{Op: "open", Path: paths.CredentialPath, Err: err}
	}
	var credential graphCredential
	if err := json.Unmarshal(data, &credential); err != nil {
		return GraphToken{}, oauthSetup("the login file is not valid JSON")
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	if err := validateGraphCredential(credential, clock()); err != nil {
		return GraphToken{}, err
	}
	if _, err := graphConfiguration(credential.ClientID, credential.Tenant, opts.IdentityBaseURL); err != nil {
		return GraphToken{}, err
	}
	return newGraphToken(paths, credential, opts.HTTPClient, opts.IdentityBaseURL, clock, opts.Persistence, opts.files), nil
}

type graphTokenSource struct {
	mu            sync.Mutex
	paths         gmailCredentialPaths
	credential    graphCredential
	configuration oauth2.Config
	client        *http.Client
	clock         func() time.Time
	persistence   TokenPersistence
	files         graphFileOps
	pending       *oauth2.Token
}

func newGraphToken(paths gmailCredentialPaths, credential graphCredential, client *http.Client, identityBase string, clock func() time.Time, policy TokenPersistence, files graphFileOps) GraphToken {
	conf, _ := graphConfiguration(credential.ClientID, credential.Tenant, identityBase)
	return GraphToken{TokenSource: &graphTokenSource{paths: paths, credential: credential, configuration: conf, client: graphIdentityHTTPClient(client, conf), clock: clock, persistence: policy, files: files},
		CredentialPath: paths.CredentialPath, ArchiveDir: paths.ArchiveDir, ClientID: credential.ClientID, Tenant: credential.Tenant, Scopes: append([]string(nil), credential.Scopes...), Expiry: credential.Expiry, SecretValues: secretValues(credential.AccessToken, credential.RefreshToken)}
}

func (ts *graphTokenSource) Token() (*oauth2.Token, error) {
	return ts.TokenContext(context.Background())
}
func (ts *graphTokenSource) TokenContext(ctx context.Context) (*oauth2.Token, error) {
	return ts.token(ctx, false)
}
func (ts *graphTokenSource) RefreshContext(ctx context.Context) (*oauth2.Token, error) {
	return ts.token(ctx, true)
}
func (ts *graphTokenSource) token(ctx context.Context, force bool) (*oauth2.Token, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	token := ts.pending
	if token == nil {
		expiry := ts.credential.Expiry
		if force || !expiry.After(ts.clock().Add(10*time.Second)) {
			expiry = time.Unix(1, 0)
		}
		base := &oauth2.Token{AccessToken: ts.credential.AccessToken, RefreshToken: ts.credential.RefreshToken, TokenType: ts.credential.TokenType, Expiry: expiry}
		var err error
		token, err = ts.configuration.TokenSource(oauthContext(ctx, ts.client), base).Token()
		if err != nil {
			return nil, graphAuthError(ctx, ts.credential.Tenant, err)
		}
	}
	if token == nil || token.AccessToken == "" || token.Expiry.IsZero() || !token.Expiry.After(ts.clock()) {
		return nil, fmt.Errorf("auth: refreshed Microsoft token is invalid: %w", backend.ErrReconnectRequired)
	}
	next := ts.credential
	next.AccessToken = token.AccessToken
	next.TokenType = token.TokenType
	next.Expiry = token.Expiry
	if token.RefreshToken != "" {
		next.RefreshToken = token.RefreshToken
	}
	if scope := tokenScope(token); scope != "" {
		next.Scopes = strings.Fields(scope)
	}
	if err := validateGraphCredential(next, ts.clock()); err != nil {
		return nil, err
	}
	changed := next.AccessToken != ts.credential.AccessToken || next.RefreshToken != ts.credential.RefreshToken || !next.Expiry.Equal(ts.credential.Expiry) || !sameStringSet(next.Scopes, ts.credential.Scopes) || next.TokenType != ts.credential.TokenType
	if changed && ts.persistence == PersistTokens {
		ts.pending = token
		if err := storeGraphCredential(ctx, ts.paths, next, ts.files); err != nil {
			return nil, err
		}
	}
	ts.pending = nil
	ts.credential = next
	return token, nil
}
