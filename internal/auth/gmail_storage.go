package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/mailmd/internal/privatefs"
)

const gmailTokenFilename = "gmail-token.json"

// renameCredentialFile lets tests make the rename of the login file fail.
var renameCredentialFile = os.Rename

// gmailCredentialPaths holds the paths of one Gmail login. They are built
// once, after the checks, so no later code builds them in a different way.
type gmailCredentialPaths struct {
	ConfigRoot     string
	AuthRoot       string
	ArchiveDir     string
	CredentialDir  string
	CredentialPath string
}

// gmailCredentialV1 is what mailmd saves for a Gmail account: the client
// fields and the tokens it needs to renew the access. The downloaded
// client_secret.json itself is never saved.
type gmailCredentialV1 struct {
	SchemaVersion int       `json:"schema_version"`
	ClientID      string    `json:"client_id"`
	ClientSecret  string    `json:"client_secret"`
	AuthURI       string    `json:"auth_uri"`
	TokenURI      string    `json:"token_uri"`
	Scopes        []string  `json:"scopes"`
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token"`
	TokenType     string    `json:"token_type"`
	TokenExpiry   time.Time `json:"token_expiry"`
}

// resolveGmailCredentialPaths checks the config folder and the account's
// folder name, and builds the path of the login file from them.
func resolveGmailCredentialPaths(configRoot, archiveDir string) (gmailCredentialPaths, error) {
	root := trimASCIISpace(configRoot)
	if root == "" {
		return gmailCredentialPaths{}, invalidOptions("ConfigRoot is required")
	}
	if archiveDir == "" {
		return gmailCredentialPaths{}, invalidOptions("ArchiveDir is required")
	}
	if archiveDir == "." || archiveDir == ".." || filepath.IsAbs(archiveDir) || strings.Contains(archiveDir, "/") || strings.Contains(archiveDir, "\\") {
		return gmailCredentialPaths{}, &PathError{Op: "validate", Path: archiveDir, Err: ErrUnsafePath}
	}
	if mail.SafePathSegment(archiveDir) != archiveDir {
		return gmailCredentialPaths{}, &PathError{Op: "validate", Path: archiveDir, Err: ErrUnsafePath}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return gmailCredentialPaths{}, err
	}
	authRoot := filepath.Join(absRoot, "mailmd")
	credentialDir := filepath.Join(authRoot, archiveDir)
	credentialPath := filepath.Join(credentialDir, gmailTokenFilename)
	if err := containCredentialPath(authRoot, credentialPath); err != nil {
		return gmailCredentialPaths{}, err
	}
	return gmailCredentialPaths{
		ConfigRoot:     absRoot,
		AuthRoot:       authRoot,
		ArchiveDir:     archiveDir,
		CredentialDir:  credentialDir,
		CredentialPath: credentialPath,
	}, nil
}

// containCredentialPath checks once more that the login file is inside the
// config folder. The parts of the path are already checked, so this only
// catches a mistake in that code. The folder itself is not a valid target.
func containCredentialPath(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return &PathError{Op: "contain", Path: target, Err: ErrUnsafePath}
	}
	return nil
}

// storeGmailCredential checks the folders and an existing login file, then
// writes the login. It doesn't check the login's content. The callers do
// that, and tests use this function to write broken login files.
func storeGmailCredential(ctx context.Context, paths gmailCredentialPaths, cred gmailCredentialV1) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := ensureCredentialDir(paths.AuthRoot, true); err != nil {
		return err
	}
	if err := ensureCredentialDir(paths.CredentialDir, true); err != nil {
		return err
	}
	if err := checkCredentialFile(paths.CredentialPath, false); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteCredential(ctx, paths.CredentialDir, paths.CredentialPath, data)
}

// loadGmailCredential reads and checks the login file. Its errors never
// contain the file's content.
func loadGmailCredential(ctx context.Context, paths gmailCredentialPaths) (gmailCredentialV1, error) {
	cred, err := readGmailCredential(ctx, paths)
	if err != nil {
		return gmailCredentialV1{}, err
	}
	if err := validateGmailCredential(cred); err != nil {
		return gmailCredentialV1{}, err
	}
	return cred, nil
}

// CheckSavedGmailClient reports whether the Gmail login of an account has an
// OAuth client that connect can use again, so --client-secret is not needed.
func CheckSavedGmailClient(configRoot, archiveDir string) error {
	_, err := savedGmailClient(context.Background(), configRoot, archiveDir)
	return err
}

// savedGmailClient reads the OAuth client from an account's login file, with
// the same checks of folders and permissions as a sync. It needs only the
// client fields, so it also works when the tokens are no longer valid.
func savedGmailClient(ctx context.Context, configRoot, archiveDir string) (desktopOAuthClient, error) {
	paths, err := resolveGmailCredentialPaths(configRoot, archiveDir)
	if err != nil {
		return desktopOAuthClient{}, err
	}
	cred, err := readGmailCredential(ctx, paths)
	if err != nil {
		return desktopOAuthClient{}, err
	}
	if cred.SchemaVersion != 1 || trimASCIISpace(cred.ClientID) == "" || trimASCIISpace(cred.ClientSecret) == "" {
		return desktopOAuthClient{}, oauthSetup("the login file has no complete OAuth client")
	}
	if err := validateGoogleOAuthEndpoints(cred.AuthURI, cred.TokenURI); err != nil {
		return desktopOAuthClient{}, err
	}
	return desktopOAuthClient{ClientID: cred.ClientID, ClientSecret: cred.ClientSecret, AuthURI: cred.AuthURI, TokenURI: cred.TokenURI}, nil
}

// readGmailCredential reads the login file after checking its folders and
// permissions. It doesn't check the content.
func readGmailCredential(ctx context.Context, paths gmailCredentialPaths) (gmailCredentialV1, error) {
	if err := checkContext(ctx); err != nil {
		return gmailCredentialV1{}, err
	}
	if err := ensureCredentialDir(paths.AuthRoot, false); err != nil {
		return gmailCredentialV1{}, err
	}
	if err := ensureCredentialDir(paths.CredentialDir, false); err != nil {
		return gmailCredentialV1{}, err
	}
	if err := checkCredentialFile(paths.CredentialPath, true); err != nil {
		return gmailCredentialV1{}, err
	}
	data, err := os.ReadFile(paths.CredentialPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return gmailCredentialV1{}, &PathError{Op: "open", Path: paths.CredentialPath, Err: ErrCredentialNotFound}
		}
		return gmailCredentialV1{}, &PathError{Op: "open", Path: paths.CredentialPath, Err: err}
	}
	var cred gmailCredentialV1
	if err := json.Unmarshal(data, &cred); err != nil {
		return gmailCredentialV1{}, oauthSetup("the login file is not valid JSON")
	}
	return cred, nil
}

// validateGmailCredential checks a saved login before mailmd uses it. The
// login needs the client fields and a refresh token. It may only ask for read
// access, and only Google's addresses are allowed.
func validateGmailCredential(cred gmailCredentialV1) error {
	if cred.SchemaVersion != 1 {
		return oauthSetup("the login file has an unknown format")
	}
	if trimASCIISpace(cred.ClientID) == "" {
		return oauthSetup("the login file has no client_id")
	}
	if trimASCIISpace(cred.ClientSecret) == "" {
		return oauthSetup("the login file has no client_secret")
	}
	if trimASCIISpace(cred.AuthURI) == "" {
		return oauthSetup("the login file has no auth_uri")
	}
	if trimASCIISpace(cred.TokenURI) == "" {
		return oauthSetup("the login file has no token_uri")
	}
	if trimASCIISpace(cred.RefreshToken) == "" {
		return oauthSetup("the login file has no refresh_token")
	}
	if !scopeListContains(cred.Scopes, GmailReadonlyScope) {
		return oauthSetup("the login file has no read access to Gmail")
	}
	if cred.AccessToken != "" && cred.TokenExpiry.IsZero() {
		return oauthSetup("the login file has no expiry for its access token")
	}
	return validateGoogleOAuthEndpoints(cred.AuthURI, cred.TokenURI)
}

// ensureCredentialDir creates a private folder for the login, or checks an
// existing one. That folder must be a real folder, not a symbolic link. On
// Linux and macOS, other users must not be able to read it.
func ensureCredentialDir(path string, create bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !create {
				return &PathError{Op: "open", Path: path, Err: ErrCredentialNotFound}
			}
			if err := os.MkdirAll(path, 0o700); err != nil {
				return &PathError{Op: "mkdir", Path: path, Err: err}
			}
			if err := os.Chmod(path, 0o700); err != nil {
				return &PathError{Op: "chmod", Path: path, Err: err}
			}
			info, err = os.Lstat(path)
			if err != nil {
				return &PathError{Op: "open", Path: path, Err: err}
			}
		} else {
			return &PathError{Op: "open", Path: path, Err: err}
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &PathError{Op: "open", Path: path, Err: ErrUnsafePath}
	}
	if !info.IsDir() {
		return &PathError{Op: "open", Path: path, Err: ErrUnsafePath}
	}
	if insecureUnixPerm(info.Mode()) {
		return &PathError{Op: "open", Path: path, Err: fmt.Errorf("%w: %s must be 0700", ErrInsecurePermission, path)}
	}
	return nil
}

// checkCredentialFile checks an existing login file before mailmd reads or
// replaces it. A missing file is fine only when required is false, because
// the caller is about to create it.
func checkCredentialFile(path string, required bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if required {
				return &PathError{Op: "open", Path: path, Err: ErrCredentialNotFound}
			}
			return nil
		}
		return &PathError{Op: "open", Path: path, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &PathError{Op: "open", Path: path, Err: ErrUnsafePath}
	}
	if !info.Mode().IsRegular() {
		return &PathError{Op: "open", Path: path, Err: ErrUnsafePath}
	}
	if insecureUnixPerm(info.Mode()) {
		return &PathError{Op: "open", Path: path, Err: fmt.Errorf("%w: %s must be 0600", ErrInsecurePermission, path)}
	}
	return nil
}

func insecureUnixPerm(mode os.FileMode) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return mode.Perm()&0o077 != 0
}

// atomicWriteCredential replaces the login file with privatefs.Replace. If
// that fails, the old login file stays as it was.
func atomicWriteCredential(ctx context.Context, dir, target string, data []byte) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := privatefs.Replace([]privatefs.Replacement{{Path: target, Data: data}}, privatefs.Hooks{Rename: renameCredentialFile}); err != nil {
		return &PathError{Op: "write", Path: target, Err: err}
	}
	return nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
