package cli

import (
	"errors"
	"fmt"
	"strings"

	mdsync "github.com/vincentsch/mailmd/internal/sync"
	rg "github.com/vincentsch/rungrad"
	"github.com/vincentsch/rungrad/output"
)

// userError is an error with a hint about what to do next.
type userError struct {
	code int
	info mdsync.ErrorInfo
}

func (e *userError) Error() string { return e.info.Message }
func (e *userError) ExitCode() int { return e.code }

func newUserError(code int, message, hint string) error {
	return &userError{code: code, info: mdsync.ErrorInfo{Category: "usage", Message: message, Hint: hint}}
}

// exitCode maps an error category to the exit code.
func exitCode(category string) int {
	switch category {
	case mdsync.CategoryAuth, mdsync.CategoryCredential:
		return rg.ExitAuth
	case mdsync.CategoryConsent, mdsync.CategoryPermission:
		return rg.ExitForbidden
	case "usage", mdsync.CategoryUnknownFolder:
		return rg.ExitUsage
	}
	return rg.ExitAPI
}

func classifyError(c rg.ErrorContext) int {
	var u *userError
	if errors.As(c.Err, &u) {
		return u.code
	}
	return c.DefaultExitCode
}

// renderError prints "Error: what happened" and, on the next line, what to
// do about it. With --json the same goes to stderr as JSON.
func renderError(c rg.ErrorContext) error {
	info := mdsync.ErrorInfo{Category: "error", Message: c.Err.Error()}
	var u *userError
	if errors.As(c.Err, &u) {
		info = u.info
	}
	if c.MachineOutput {
		data, err := output.StableJSON(map[string]any{"error": info, "exit_code": c.ExitCode})
		if err != nil {
			return err
		}
		_, err = c.Stderr.Write(append(c.RedactJSON(data), '\n'))
		return err
	}
	message := strings.TrimSpace(info.Message)
	// A message of several lines, like a "Did you mean" list of commands,
	// ends in a name that must stay as it is.
	if message != "" && !strings.Contains(message, "\n") && !strings.HasSuffix(message, ".") && !strings.HasSuffix(message, "?") {
		message += "."
	}
	text := "Error: " + message + "\n"
	if info.Hint != "" {
		text += info.Hint + "\n"
	}
	_, err := fmt.Fprint(c.Stderr, c.RedactString(text))
	return err
}
