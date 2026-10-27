package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vincentsch/mailmd/internal/backend"
	"golang.org/x/oauth2"
)

const defaultOAuthHTTPTimeout = 30 * time.Second

// persistingTokenSource wraps oauth2's token source. When it renews the
// access token, it saves the new token in the login file before the caller
// gets it.
type persistingTokenSource struct {
	mu            sync.Mutex
	src           oauth2.TokenSource
	paths         gmailCredentialPaths
	cred          gmailCredentialV1
	configuration oauth2.Config
	client        *http.Client
	persist       bool
	// pending is a renewed token that could not be saved yet. The next call
	// tries to save it again before it asks Google for another one.
	pending *oauth2.Token
}

// gmailTokenFromCredential turns a saved login into a GmailToken. The oauth2
// token source gets a background context, because Token has no context
// parameter and the source renews the token long after this call.
func gmailTokenFromCredential(paths gmailCredentialPaths, cred gmailCredentialV1, httpClient *http.Client) (GmailToken, error) {
	if err := validateGmailCredential(cred); err != nil {
		return GmailToken{}, err
	}
	client := desktopOAuthClient{
		ClientID:     cred.ClientID,
		ClientSecret: cred.ClientSecret,
		AuthURI:      cred.AuthURI,
		TokenURI:     cred.TokenURI,
	}
	conf := gmailOAuthConfig(client, "")
	baseToken := &oauth2.Token{
		AccessToken:  cred.AccessToken,
		RefreshToken: cred.RefreshToken,
		TokenType:    cred.TokenType,
		Expiry:       cred.TokenExpiry,
	}
	ctx := oauthContext(context.Background(), httpClient)
	src := conf.TokenSource(ctx, baseToken)
	ts := &persistingTokenSource{
		src:           src,
		paths:         paths,
		cred:          cred,
		configuration: conf,
		client:        httpClient,
		persist:       true,
	}
	return GmailToken{
		TokenSource:    ts,
		CredentialPath: paths.CredentialPath,
		ArchiveDir:     paths.ArchiveDir,
		Scopes:         append([]string(nil), cred.Scopes...),
		SecretValues:   secretValues(cred.ClientSecret, cred.AccessToken, cred.RefreshToken),
	}, nil
}

// Token returns a valid access token and renews it when needed. The lock is
// held during the request to Google, so two callers never renew the token
// at the same time and write the login file over each other.
func (ts *persistingTokenSource) Token() (*oauth2.Token, error) {
	return ts.token(context.Background())
}

func (ts *persistingTokenSource) TokenContext(ctx context.Context) (*oauth2.Token, error) {
	return ts.token(ctx)
}

func (ts *persistingTokenSource) token(ctx context.Context) (*oauth2.Token, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	tok := ts.pending
	if tok == nil {
		source := ts.src
		if ctx != nil {
			base := &oauth2.Token{AccessToken: ts.cred.AccessToken, RefreshToken: ts.cred.RefreshToken, TokenType: ts.cred.TokenType, Expiry: ts.cred.TokenExpiry}
			source = ts.configuration.TokenSource(oauthContext(ctx, ts.client), base)
		}
		var err error
		tok, err = source.Token()
		if err != nil {
			return nil, classifyTokenError(err)
		}
	}
	if tok == nil {
		return nil, oauthSetup("Google sent no access token")
	}
	next, changed := credentialWithToken(ts.cred, tok)
	if changed {
		// Check the new login before it is used or saved. A token without an
		// expiry would otherwise count as one that never expires.
		if err := validateGmailCredential(next); err != nil {
			return nil, err
		}
		if ts.persist {
			storeCtx := ctx
			if storeCtx == nil {
				storeCtx = context.Background()
			}
			ts.pending = tok
			if err := storeGmailCredential(storeCtx, ts.paths, next); err != nil {
				return nil, err
			}
		}
		ts.cred = next
	}
	ts.pending = nil
	if ts.configuration.ClientID != "" {
		ts.src = ts.configuration.TokenSource(oauthContext(context.Background(), ts.client), tok)
	}
	return tok, nil
}

// credentialWithToken copies the saved login with the new token in it. An
// empty refresh token in the answer is ignored, because Google's answer to a
// renewal usually has no refresh token.
func credentialWithToken(cred gmailCredentialV1, tok *oauth2.Token) (gmailCredentialV1, bool) {
	changed := false
	if cred.AccessToken != tok.AccessToken {
		cred.AccessToken = tok.AccessToken
		changed = true
	}
	if tok.RefreshToken != "" && cred.RefreshToken != tok.RefreshToken {
		cred.RefreshToken = tok.RefreshToken
		changed = true
	}
	if cred.TokenType != tok.TokenType {
		cred.TokenType = tok.TokenType
		changed = true
	}
	if !cred.TokenExpiry.Equal(tok.Expiry) {
		cred.TokenExpiry = tok.Expiry
		changed = true
	}
	if scope := tokenScope(tok); scope != "" {
		scopes := strings.Fields(scope)
		if !sameStringSet(cred.Scopes, scopes) {
			cred.Scopes = scopes
			changed = true
		}
	}
	return cred, changed
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, value := range a {
		counts[value]++
	}
	for _, value := range b {
		counts[value]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

// classifyTokenError turns a revoked or invalid login into
// backend.ErrReconnectRequired. Other errors lose the response body, which
// can contain secrets.
func classifyTokenError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) {
		if retrieveRequiresReconnect(retrieve) {
			return fmt.Errorf("auth: gmail credentials require reconnect: %w", backend.ErrReconnectRequired)
		}
		return sanitizeTokenRetrieveError(retrieve)
	}
	return err
}

// sanitizeOAuthExchangeError explains a failed code exchange without the
// response body.
func sanitizeOAuthExchangeError(err error) error {
	if err == nil {
		return nil
	}
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) {
		if retrieve.ErrorCode != "" {
			return oauthSetup("Google did not accept the sign-in code (%s)", sanitizeOAuthCode(retrieve.ErrorCode))
		}
		if retrieve.Response != nil && retrieve.Response.StatusCode != 0 {
			return oauthSetup("Google answered the sign-in code with HTTP status %d", retrieve.Response.StatusCode)
		}
		return oauthSetup("Google did not accept the sign-in code")
	}
	return oauthSetup("Google did not accept the sign-in code")
}

// sanitizeTokenRetrieveError explains a failed renewal without
// RetrieveError.Body, which can contain secrets that Google sends back.
func sanitizeTokenRetrieveError(err *oauth2.RetrieveError) error {
	if err == nil {
		return errors.New("auth: Google could not renew the login")
	}
	if err.ErrorCode != "" {
		return fmt.Errorf("auth: Google could not renew the login (%s)", sanitizeOAuthCode(err.ErrorCode))
	}
	if err.Response != nil && err.Response.StatusCode != 0 {
		return fmt.Errorf("auth: Google could not renew the login (HTTP status %d)", err.Response.StatusCode)
	}
	return errors.New("auth: Google could not renew the login")
}

// retrieveRequiresReconnect reports whether the user has to connect again.
// Other errors may go away when mailmd tries again later.
func retrieveRequiresReconnect(err *oauth2.RetrieveError) bool {
	if err == nil {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(err.ErrorCode))
	if code == "invalid_grant" || code == "invalid_token" {
		return true
	}
	desc := strings.ToLower(err.ErrorDescription)
	if strings.Contains(desc, "invalid_grant") ||
		strings.Contains(desc, "invalid token") ||
		strings.Contains(desc, "refresh token") && (strings.Contains(desc, "revoked") || strings.Contains(desc, "invalid") || strings.Contains(desc, "expired")) {
		return true
	}
	if err.Response != nil && err.Response.StatusCode == http.StatusBadRequest && (strings.Contains(desc, "grant") || strings.Contains(desc, "token")) {
		return true
	}
	return false
}

// oauthContext adds the HTTP client for oauth2 to ctx. The token source gets
// a background context here, so a later renewal still works after the
// command that loaded the login has ended.
func oauthContext(ctx context.Context, client *http.Client) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, oauth2.HTTPClient, oauthHTTPClient(client))
}

func oauthHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: defaultOAuthHTTPTimeout}
}

// secretValues lists each secret once, so the command layer can hide them in
// its output without knowing the login file's format.
func secretValues(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
