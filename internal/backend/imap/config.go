package imap

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/vincentsch/mailmd/internal/backend"
)

var (
	ErrInvalidConfig = errors.New("imap: invalid config")
	ErrAuth          = errors.New("imap: authentication failed")
	ErrProtocol      = errors.New("imap: protocol error")
	ErrTLS           = errors.New("imap: TLS failed")

	ErrScopeKeyMalformed = fmt.Errorf("imap: malformed scope key: %w", backend.ErrInvalidCursor)
)

func validateConfig(cfg Config) error {
	switch {
	case cfg.Host == "":
		return invalidConfig("the host is missing")
	case cfg.Username == "":
		return invalidConfig("the username is missing")
	case cfg.Password == "":
		return invalidConfig("the password is missing")
	case cfg.TLSMode != TLSImplicit && cfg.TLSMode != TLSStartTLS:
		return invalidConfig("the TLS mode must be implicit or starttls")
	case cfg.Port < 0 || cfg.Port > 65535:
		return invalidConfig("the port must be between 1 and 65535")
	}
	return nil
}

func invalidConfig(msg string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, msg)
}

// DefaultPort returns the usual port for a TLS mode.
func DefaultPort(mode TLSMode) int {
	if mode == TLSStartTLS {
		return 143
	}
	return 993
}

func address(cfg Config) string {
	port := cfg.Port
	if port == 0 {
		port = DefaultPort(cfg.TLSMode)
	}
	return net.JoinHostPort(cfg.Host, strconv.Itoa(port))
}
