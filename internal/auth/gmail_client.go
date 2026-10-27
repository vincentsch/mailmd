package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const googleTokenURI = "https://oauth2.googleapis.com/token"

// desktopOAuthClient holds the fields of Google's client JSON that mailmd
// needs later to renew the login. mailmd never saves the downloaded file
// itself.
type desktopOAuthClient struct {
	ClientID     string
	ClientSecret string
	AuthURI      string
	TokenURI     string
}

type installedClientJSON struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURI      string `json:"auth_uri"`
	TokenURI     string `json:"token_uri"`
}

// parseDesktopClientFile reads the client JSON file that the user downloaded.
func parseDesktopClientFile(path string) (desktopOAuthClient, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return desktopOAuthClient{}, invalidOAuthClient("cannot read the client file %s: %v", path, err)
	}
	return parseDesktopClientJSON(data)
}

// CheckGmailClient reads an OAuth client file the way connect does, so
// --dry-run can check it. It returns the client ID.
func CheckGmailClient(path string) (string, error) {
	client, err := parseDesktopClientFile(path)
	return client.ClientID, err
}

// parseDesktopClientJSON accepts only a client of the type "Desktop app".
// google.ConfigFromJSON also accepts web clients, which don't work with mailmd,
// so it isn't used. A wrong client fails here, before the user sees a browser.
func parseDesktopClientJSON(data []byte) (desktopOAuthClient, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return desktopOAuthClient{}, invalidOAuthClient("the client file is not a Google OAuth client JSON file")
	}
	if _, ok := top["web"]; ok {
		return desktopOAuthClient{}, invalidOAuthClient("the client file is for a Web application, but mailmd needs an OAuth client of the type \"Desktop app\"")
	}
	raw, ok := top["installed"]
	if !ok || len(top) != 1 {
		return desktopOAuthClient{}, invalidOAuthClient("the client file is not a Desktop app OAuth client")
	}
	var installed installedClientJSON
	if err := json.Unmarshal(raw, &installed); err != nil {
		return desktopOAuthClient{}, invalidOAuthClient("the client file is not a Desktop app OAuth client")
	}
	client := desktopOAuthClient(installed)
	if trimASCIISpace(client.ClientID) == "" {
		return desktopOAuthClient{}, invalidOAuthClient("the client file has no client_id")
	}
	if trimASCIISpace(client.ClientSecret) == "" {
		return desktopOAuthClient{}, fmt.Errorf("%w: %w", ErrInvalidOAuthClient, ErrNoClientSecret)
	}
	if trimASCIISpace(client.AuthURI) == "" {
		return desktopOAuthClient{}, invalidOAuthClient("the client file has no auth_uri")
	}
	if trimASCIISpace(client.TokenURI) == "" {
		return desktopOAuthClient{}, invalidOAuthClient("the client file has no token_uri")
	}
	if err := validateGoogleOAuthEndpoints(client.AuthURI, client.TokenURI); err != nil {
		return desktopOAuthClient{}, err
	}
	return client, nil
}

// validateGoogleOAuthEndpoints rejects a client file that points the sign-in
// to a host other than Google's.
func validateGoogleOAuthEndpoints(authURI, tokenURI string) error {
	u, err := url.Parse(authURI)
	if err != nil {
		return invalidOAuthClient("the auth_uri in the client file is not a Google address")
	}
	// Any path is fine, because gmailOAuthConfig uses google.Endpoint and not
	// this address. The host is still checked, so a forged client file can't
	// send the user to another site.
	if u.Scheme != "https" || u.Host != "accounts.google.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return invalidOAuthClient("the auth_uri in the client file is not Google's sign-in address")
	}
	if tokenURI != googleTokenURI {
		return invalidOAuthClient("the token_uri in the client file is not Google's token address")
	}
	return nil
}

// gmailOAuthConfig always uses Google's current addresses, even when the
// client file names an older one that mailmd accepts.
func gmailOAuthConfig(client desktopOAuthClient, redirectURL string) oauth2.Config {
	return oauth2.Config{
		ClientID:     client.ClientID,
		ClientSecret: client.ClientSecret,
		Endpoint:     google.Endpoint,
		RedirectURL:  redirectURL,
		Scopes:       []string{GmailReadonlyScope},
	}
}
