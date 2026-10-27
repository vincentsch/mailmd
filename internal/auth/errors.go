package auth

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidOptions     = errors.New("auth: invalid options")
	ErrInvalidOAuthClient = errors.New("auth: invalid oauth client")
	ErrCredentialNotFound = errors.New("auth: credential not found")
	ErrUnsafePath         = errors.New("auth: unsafe credential path")
	ErrInsecurePermission = errors.New("auth: insecure credential permissions")
	ErrOAuthSetup         = errors.New("auth: oauth setup failed")
)

// PathError adds the file and the operation to an auth error.
type PathError struct {
	Op   string
	Path string
	Err  error
}

func (e *PathError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("auth: %s %s: %v", e.Op, e.Path, e.Err)
}

func (e *PathError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func invalidOptions(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidOptions, fmt.Sprintf(format, args...))
}

// ErrNoClientSecret means the Gmail client file has no client secret.
// Google shows the full secret only once, when the client is created.
var ErrNoClientSecret = errors.New("the client file has no client_secret")

func invalidOAuthClient(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidOAuthClient, fmt.Sprintf(format, args...))
}

func oauthSetup(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrOAuthSetup, fmt.Sprintf(format, args...))
}
