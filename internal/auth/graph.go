package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/vincentsch/mailmd/internal/backend"
	"golang.org/x/oauth2"
)

// GraphReadonlyScope is the only Graph permission mailmd asks for.
const GraphReadonlyScope = "https://graph.microsoft.com/Mail.Read"

var ErrAdminConsentRequired = errors.New("auth: tenant consent policy requires administrator action")

type AdminConsentError struct{ Tenant string }

func (e *AdminConsentError) Error() string {
	return "auth: tenant consent policy may require an administrator to grant delegated Mail.Read"
}
func (e *AdminConsentError) Unwrap() error { return ErrAdminConsentRequired }

type DeviceCodePrompt struct {
	VerificationURI string
	UserCode        string
	Expires         time.Time
}

type GraphConnectOptions struct {
	ClientID, Tenant, ConfigRoot, ArchiveDir string
	HTTPClient                               *http.Client
	Clock                                    func() time.Time
	HandleDeviceCode                         func(context.Context, DeviceCodePrompt) error
	// Tests point these at a local server. Other hosts are refused.
	IdentityBaseURL, GraphBaseURL string
	files                         graphFileOps
}

type TokenPersistence int

const (
	PersistTokens TokenPersistence = iota
	MemoryOnlyTokens
)

type GraphLoadOptions struct {
	ConfigRoot, ArchiveDir string
	HTTPClient             *http.Client
	Clock                  func() time.Time
	Persistence            TokenPersistence
	IdentityBaseURL        string
	files                  graphFileOps
}

type GraphToken struct {
	TokenSource                                  oauth2.TokenSource
	CredentialPath, ArchiveDir, ClientID, Tenant string
	Scopes                                       []string
	Expiry                                       time.Time
	SecretValues                                 []string
}

type GraphConnectResult struct {
	GraphToken
	// SignInName is the account's name from the ID token, like
	// dana@contoso.com. It is empty when the token has no name with an @.
	SignInName string
}

func graphScopes() []string {
	return []string{"openid", "profile", "offline_access", GraphReadonlyScope}
}

// tenantDomain matches a tenant given as a domain, like contoso.onmicrosoft.com.
var tenantDomain = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)

// CheckGraphSettings checks a client ID and tenant before any network use.
func CheckGraphSettings(clientID, tenant string) error {
	if _, err := uuid.Parse(clientID); err != nil || len(clientID) != 36 {
		return invalidOptions("--client-id must be the application (client) ID of your app registration, like 12345678-1234-1234-1234-123456789abc")
	}
	switch {
	case tenant == "" || tenant == "common" || tenant == "organizations" || tenant == "consumers":
	case len(tenant) == 36:
		if _, err := uuid.Parse(tenant); err != nil {
			return invalidOptions("--tenant must be common, organizations, consumers, a tenant ID or a domain like contoso.onmicrosoft.com")
		}
	case !tenantDomain.MatchString(tenant):
		return invalidOptions("--tenant must be common, organizations, consumers, a tenant ID or a domain like contoso.onmicrosoft.com")
	}
	return nil
}

func graphConfiguration(clientID, tenant, identityBase string) (oauth2.Config, error) {
	if err := CheckGraphSettings(clientID, tenant); err != nil {
		return oauth2.Config{}, err
	}
	if tenant == "" {
		tenant = "common"
	}
	base, err := graphEndpoint(identityBase, "https://login.microsoftonline.com", false)
	if err != nil {
		return oauth2.Config{}, err
	}
	return oauth2.Config{ClientID: clientID, Scopes: graphScopes(), Endpoint: oauth2.Endpoint{
		DeviceAuthURL: base + "/" + tenant + "/oauth2/v2.0/devicecode", TokenURL: base + "/" + tenant + "/oauth2/v2.0/token", AuthStyle: oauth2.AuthStyleInParams,
	}}, nil
}

func graphEndpoint(override, production string, api bool) (string, error) {
	if override == "" {
		return production, nil
	}
	u, err := url.Parse(override)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Host == "" || !canonicalAuthPath(u.Path) || (u.Scheme != "http" && u.Scheme != "https") {
		return "", invalidOptions("invalid test endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", invalidOptions("test endpoint must be loopback")
	}
	if !api && u.Path != "" && u.Path != "/" {
		return "", invalidOptions("identity endpoint must be an origin")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// graphAuthTransport checks each request before it goes out. It reads at most
// 1 MiB of each answer before oauth2 decodes it.
type graphAuthTransport struct {
	base  http.RoundTripper
	valid func(*http.Request) bool
}

func (t graphAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.valid(req) {
		return nil, errors.New("auth: request rejected")
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	data, err := backend.ReadBounded(response.Body, 1<<20)
	_ = response.Body.Close()
	if err != nil {
		return nil, errors.New("auth: identity response rejected")
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	return response, nil
}

// graphHTTPClient makes a client for one origin. With identityPaths, it may
// only POST to those sign-in paths. Without them, it may only GET below the
// Graph API address. It checks the first request and each redirect.
func graphHTTPClient(input *http.Client, endpoint string, identityPaths ...string) *http.Client {
	client := *oauthHTTPClient(input)
	base, parseErr := url.Parse(endpoint)
	valid := func(req *http.Request) bool {
		u := req.URL
		if parseErr != nil || base == nil || u == nil || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
			u.Scheme != base.Scheme || u.Host != base.Host || !canonicalAuthPath(u.Path) {
			return false
		}
		if len(identityPaths) > 0 {
			if req.Method != http.MethodPost || u.RawQuery != "" {
				return false
			}
			for _, p := range identityPaths {
				if u.Path == p {
					return true
				}
			}
			return false
		}
		return req.Method == http.MethodGet && strings.HasPrefix(u.Path, strings.TrimRight(base.Path, "/")+"/")
	}
	client.Transport = graphAuthTransport{base: client.Transport, valid: valid}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 || len(via) >= 10 || !valid(req) || req.Method != via[0].Method {
			return errors.New("auth: redirect rejected")
		}
		if len(identityPaths) > 0 && req.URL.Path != via[0].URL.Path {
			return errors.New("auth: redirect rejected")
		}
		return nil
	}
	return &client
}

// canonicalAuthPath rejects a path that still has percent escapes or
// backslashes after net/url decoded it once. A server could decode them again
// and find something like ../ in them.
func canonicalAuthPath(p string) bool {
	return (p == "" || path.Clean(p) == p) && !strings.ContainsAny(p, "%\\") && !strings.ContainsFunc(p, unicode.IsControl)
}

func graphIdentityHTTPClient(input *http.Client, conf oauth2.Config) *http.Client {
	token, _ := url.Parse(conf.Endpoint.TokenURL)
	device, _ := url.Parse(conf.Endpoint.DeviceAuthURL)
	return graphHTTPClient(input, token.Scheme+"://"+token.Host, token.Path, device.Path)
}

// deviceLoginHosts are the hosts of the pages where Microsoft asks for the
// sign-in code. Microsoft now sends https://login.microsoft.com/device (seen
// in October 2026). Older answers had https://microsoft.com/devicelogin.
var deviceLoginHosts = map[string]bool{
	"login.microsoft.com":       true,
	"microsoft.com":             true,
	"www.microsoft.com":         true,
	"login.microsoftonline.com": true,
}

func ConnectGraph(ctx context.Context, opts GraphConnectOptions) (GraphConnectResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return GraphConnectResult{}, ctx.Err()
	}
	if opts.HandleDeviceCode == nil {
		return GraphConnectResult{}, invalidOptions("HandleDeviceCode is required")
	}
	conf, err := graphConfiguration(opts.ClientID, opts.Tenant, opts.IdentityBaseURL)
	if err != nil {
		return GraphConnectResult{}, err
	}
	if opts.Tenant == "" {
		opts.Tenant = "common"
	}
	paths, err := graphPaths(opts.ConfigRoot, opts.ArchiveDir)
	if err != nil {
		return GraphConnectResult{}, err
	}
	base, err := graphEndpoint(opts.GraphBaseURL, "https://graph.microsoft.com/v1.0", true)
	if err != nil {
		return GraphConnectResult{}, err
	}
	client := graphIdentityHTTPClient(opts.HTTPClient, conf)
	flowCtx := oauthContext(ctx, client)
	device, err := conf.DeviceAuth(flowCtx)
	if err != nil {
		return GraphConnectResult{}, graphAuthError(ctx, opts.Tenant, err)
	}
	u, err := url.Parse(device.VerificationURI)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!deviceLoginHosts[u.Hostname()] || device.DeviceCode == "" || device.UserCode == "" || device.Expiry.IsZero() || device.Interval < 0 || device.Interval > int64((time.Duration(1<<63-1)-30*time.Second)/time.Second) {
		return GraphConnectResult{}, oauthSetup("Microsoft sent a sign-in code that mailmd can't use")
	}
	if err := opts.HandleDeviceCode(ctx, DeviceCodePrompt{VerificationURI: device.VerificationURI, UserCode: device.UserCode, Expires: device.Expiry}); err != nil {
		return GraphConnectResult{}, oauthSetup("the sign-in code could not be shown")
	}
	token, err := conf.DeviceAccessToken(flowCtx, device)
	if err != nil {
		return GraphConnectResult{}, graphAuthError(ctx, opts.Tenant, err)
	}
	if token == nil {
		return GraphConnectResult{}, oauthSetup("Microsoft sent no login")
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	scopes := graphScopes()
	if returned := tokenScope(token); returned != "" {
		scopes = strings.Fields(returned)
	}
	credential := graphCredential{Schema: "mailmd-graph-token/1", ClientID: opts.ClientID, Tenant: opts.Tenant, Scopes: scopes, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: token.TokenType, Expiry: token.Expiry}
	if err := validateGraphCredential(credential, clock()); err != nil {
		return GraphConnectResult{}, err
	}
	if !credential.Expiry.After(clock()) {
		return GraphConnectResult{}, oauthSetup("Microsoft sent a login that has already expired")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/me/mailFolders?$top=1&$select=id,displayName", nil)
	if err != nil {
		return GraphConnectResult{}, oauthSetup("mailmd could not check the access to your mail")
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err := graphHTTPClient(opts.HTTPClient, base).Do(request)
	if err != nil {
		return GraphConnectResult{}, graphAuthError(ctx, opts.Tenant, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == 401 {
			return GraphConnectResult{}, fmt.Errorf("auth: access probe rejected credentials: %w", backend.ErrReconnectRequired)
		}
		data, _ := backend.ReadBounded(response.Body, 1<<20)
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		if failure.Error.Code == "Authorization_RequestDenied" || failure.Error.Code == "ConsentRequired" {
			return GraphConnectResult{}, &AdminConsentError{Tenant: opts.Tenant}
		}
		if response.StatusCode == 403 {
			return GraphConnectResult{}, fmt.Errorf("auth: mail read access denied: %w", backend.ErrPermissionDenied)
		}
		return GraphConnectResult{}, oauthSetup("mailmd could not read your mail folders to check the access")
	}
	data, err := backend.ReadBounded(response.Body, 1<<20)
	if err != nil {
		return GraphConnectResult{}, oauthSetup("Microsoft's answer to the access check can't be read")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return GraphConnectResult{}, oauthSetup("Microsoft's answer to the access check can't be read")
	}
	var folders []json.RawMessage
	if raw, ok := probe["value"]; !ok || json.Unmarshal(raw, &folders) != nil {
		return GraphConnectResult{}, oauthSetup("Microsoft's answer to the access check can't be read")
	}
	if ctx.Err() != nil {
		return GraphConnectResult{}, ctx.Err()
	}
	if err := storeGraphCredential(ctx, paths, credential, opts.files); err != nil {
		return GraphConnectResult{}, err
	}
	loaded := newGraphToken(paths, credential, opts.HTTPClient, opts.IdentityBaseURL, clock, PersistTokens, opts.files)
	loaded.SecretValues = secretValues(token.AccessToken, token.RefreshToken, device.DeviceCode)
	return GraphConnectResult{GraphToken: loaded, SignInName: signInName(token)}, nil
}

// signInName reads the account's name from the ID token that Microsoft
// sends with the login. mailmd only shows the name, so it doesn't check the
// token's signature.
func signInName(token *oauth2.Token) string {
	raw, _ := token.Extra("id_token").(string)
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Name string `json:"preferred_username"`
	}
	if json.Unmarshal(payload, &claims) != nil || !strings.Contains(claims.Name, "@") || strings.ContainsFunc(claims.Name, unicode.IsSpace) {
		return ""
	}
	return claims.Name
}

func graphAuthError(ctx context.Context, tenant string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var failure *oauth2.RetrieveError
	if errors.As(err, &failure) {
		var body struct {
			Codes []int `json:"error_codes"`
		}
		_ = json.Unmarshal(failure.Body, &body)
		for _, code := range body.Codes {
			if code == 65001 || code == 90094 {
				return &AdminConsentError{Tenant: tenant}
			}
		}
		if failure.ErrorCode == "consent_required" {
			return &AdminConsentError{Tenant: tenant}
		}
		if failure.ErrorCode == "invalid_grant" || failure.ErrorCode == "invalid_token" || failure.ErrorCode == "invalid_scope" {
			return fmt.Errorf("auth: Microsoft credentials require reconnect: %w", backend.ErrReconnectRequired)
		}
		if reason := graphSignInProblem(failure.ErrorCode, body.Codes); reason != "" {
			return oauthSetup("%s", reason)
		}
	}
	return oauthSetup("Microsoft did not finish the sign-in")
}

// graphSignInProblem explains the sign-in errors that come from a mistake in
// the app registration or from waiting too long. The numbers are Entra's
// AADSTS error codes.
func graphSignInProblem(code string, numbers []int) string {
	switch code {
	case "authorization_declined":
		return "you declined the request on Microsoft's page"
	case "expired_token":
		return "the code expired before you finished signing in"
	}
	for _, number := range numbers {
		switch number {
		case 7000218:
			return "Microsoft asks for a client secret, so your app registration doesn't allow sign-in with a code. " +
				"In Entra, open \"Authentication\" and set \"Allow public client flows\" to \"Enabled\""
		case 700016:
			return "Microsoft can't find an app with this client ID for your account. " +
				"Check --client-id, and choose the --tenant that fits the account types of your app registration"
		case 50194:
			return "your app registration only allows accounts of its own organization. Connect with --tenant and your tenant ID"
		}
	}
	return ""
}

func graphPaths(root, archive string) (gmailCredentialPaths, error) {
	paths, err := resolveGmailCredentialPaths(root, archive)
	if err != nil {
		return paths, err
	}
	paths.CredentialPath = filepath.Join(paths.CredentialDir, "graph-token.json")
	return paths, nil
}
