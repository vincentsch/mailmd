package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// GmailReadonlyScope is the only Gmail scope mailmd asks for.
const GmailReadonlyScope = "https://www.googleapis.com/auth/gmail.readonly"

// BrowserOpener opens the sign-in address in a browser. It is optional.
// Without it, the user opens the address that AuthURLHandler shows.
type BrowserOpener func(ctx context.Context, url string) error

// AuthURLHandler gets the sign-in address before a browser opens.
type AuthURLHandler func(ctx context.Context, url string) error

// GmailOptions are the settings of one Gmail sign-in. The login is saved
// under ConfigRoot/mailmd/ArchiveDir. The callbacks decide what the user sees,
// because this package prints nothing.
type GmailOptions struct {
	ConfigRoot      string
	ArchiveDir      string // the account's folder name, already safe
	OAuthClientPath string // the client JSON file from Google Cloud
	// SavedClientOf is the folder name of an account whose saved client
	// mailmd uses when OAuthClientPath is empty. So connecting an account
	// again works without the downloaded file.
	SavedClientOf string
	OpenBrowser   BrowserOpener
	HandleAuthURL AuthURLHandler // required
	HTTPClient    *http.Client   // optional, for the token requests
	// ReadRedirect, when set, reads the address the user pastes after
	// signing in on another device. It runs while the local web server waits
	// for Google's answer.
	ReadRedirect func(context.Context) (string, error)
	// WaitLimit is how long ConnectGmail waits for the user to allow access.
	// Zero means no limit.
	WaitLimit time.Duration
}

// GmailLoadOptions say which saved Gmail login to load.
type GmailLoadOptions struct {
	ConfigRoot  string
	ArchiveDir  string
	HTTPClient  *http.Client
	Persistence TokenPersistence
}

// GmailToken is a loaded Gmail login. SecretValues lists the secrets it
// holds right now, so the caller can hide them in its output.
type GmailToken struct {
	TokenSource    oauth2.TokenSource
	CredentialPath string
	ArchiveDir     string
	Scopes         []string
	SecretValues   []string
}

// GmailConnectResult is the login that ConnectGmail saved.
type GmailConnectResult struct {
	GmailToken
}

// ConnectGmail signs the user in to Google and saves the Gmail login for the
// account in opts.ArchiveDir.
func ConnectGmail(ctx context.Context, opts GmailOptions) (GmailConnectResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.HandleAuthURL == nil {
		return GmailConnectResult{}, invalidOptions("HandleAuthURL is required")
	}
	clientPath := trimASCIISpace(opts.OAuthClientPath)
	if clientPath == "" && opts.SavedClientOf == "" {
		return GmailConnectResult{}, invalidOptions("OAuthClientPath or SavedClientOf is required")
	}
	paths, err := resolveGmailCredentialPaths(opts.ConfigRoot, opts.ArchiveDir)
	if err != nil {
		return GmailConnectResult{}, err
	}
	var client desktopOAuthClient
	if clientPath != "" {
		client, err = parseDesktopClientFile(clientPath)
	} else {
		client, err = savedGmailClient(ctx, opts.ConfigRoot, opts.SavedClientOf)
	}
	if err != nil {
		return GmailConnectResult{}, err
	}

	// Check the options and the client file first, so a mistake there fails
	// before the user sees a browser.
	flow, err := startGmailLoopback(ctx, client)
	if err != nil {
		return GmailConnectResult{}, err
	}
	defer flow.Close()

	// The caller shows the address before the browser opens. If the browser
	// doesn't open, the user can still open the address by hand.
	if err := opts.HandleAuthURL(ctx, flow.AuthURL); err != nil {
		return GmailConnectResult{}, oauthSetup("the sign-in address could not be shown")
	}
	if opts.OpenBrowser != nil {
		_ = opts.OpenBrowser(ctx, flow.AuthURL)
	}
	if opts.ReadRedirect != nil {
		go func() {
			if raw, err := opts.ReadRedirect(ctx); err == nil && strings.TrimSpace(raw) != "" {
				flow.Paste(raw)
			}
		}()
	}

	waitCtx := ctx
	if opts.WaitLimit > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, opts.WaitLimit)
		defer cancel()
	}
	code, err := flow.Wait(waitCtx)
	if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return GmailConnectResult{}, oauthSetup("nobody allowed access in the browser within %s", waitText(opts.WaitLimit))
	}
	if err != nil {
		return GmailConnectResult{}, err
	}
	// The code exchange stops when ctx ends. The token source made below uses
	// its own context, because it renews the login later, long after connect
	// has finished.
	tokenCtx := oauthContext(ctx, opts.HTTPClient)
	tok, err := flow.Config.Exchange(tokenCtx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return GmailConnectResult{}, ctxErr
		}
		return GmailConnectResult{}, sanitizeOAuthExchangeError(err)
	}
	if tok.RefreshToken == "" {
		return GmailConnectResult{}, oauthSetup("Google sent no refresh token. If this happens again, first remove your app's access at https://myaccount.google.com/connections")
	}
	if scope := tokenScope(tok); scope != "" && !scopeListContains(strings.Fields(scope), GmailReadonlyScope) {
		return GmailConnectResult{}, oauthSetup("Google did not give read access to Gmail. Allow it when Google asks")
	}

	cred := gmailCredentialV1{
		SchemaVersion: 1,
		ClientID:      client.ClientID,
		ClientSecret:  client.ClientSecret,
		AuthURI:       client.AuthURI,
		TokenURI:      client.TokenURI,
		Scopes:        []string{GmailReadonlyScope},
		AccessToken:   tok.AccessToken,
		RefreshToken:  tok.RefreshToken,
		TokenType:     tok.TokenType,
		TokenExpiry:   tok.Expiry,
	}
	// Check before writing. oauth2 treats a token without expiry as one that
	// never expires, and such a login would never be renewed.
	if err := validateGmailCredential(cred); err != nil {
		return GmailConnectResult{}, err
	}
	if err := storeGmailCredential(ctx, paths, cred); err != nil {
		return GmailConnectResult{}, err
	}
	gmailToken, err := gmailTokenFromCredential(paths, cred, opts.HTTPClient)
	if err != nil {
		return GmailConnectResult{}, err
	}
	return GmailConnectResult{GmailToken: gmailToken}, nil
}

// waitText writes a wait limit like "10 minutes".
func waitText(d time.Duration) string {
	if d < time.Minute {
		return d.String()
	}
	if d < 2*time.Minute {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}

// LoadGmailToken loads a saved Gmail login. Its token source renews the
// access token when it runs out.
func LoadGmailToken(ctx context.Context, opts GmailLoadOptions) (GmailToken, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return GmailToken{}, err
	}
	if opts.Persistence != PersistTokens && opts.Persistence != MemoryOnlyTokens {
		return GmailToken{}, invalidOptions("invalid token persistence policy")
	}
	paths, err := resolveGmailCredentialPaths(opts.ConfigRoot, opts.ArchiveDir)
	if err != nil {
		return GmailToken{}, err
	}
	cred, err := loadGmailCredential(ctx, paths)
	if err != nil {
		return GmailToken{}, err
	}
	loaded, err := gmailTokenFromCredential(paths, cred, opts.HTTPClient)
	if err == nil && opts.Persistence == MemoryOnlyTokens {
		loaded.TokenSource.(*persistingTokenSource).persist = false
	}
	return loaded, err
}

func tokenScope(tok *oauth2.Token) string {
	if tok == nil {
		return ""
	}
	value := tok.Extra("scope")
	scope, _ := value.(string)
	return scope
}

func scopeListContains(scopes []string, scope string) bool {
	for _, candidate := range scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

func trimASCIISpace(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			return true
		default:
			return false
		}
	})
}
