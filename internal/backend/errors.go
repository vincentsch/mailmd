package backend

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

var (
	ErrRetryable         = errors.New("backend: retryable error")
	ErrClosed            = errors.New("backend: backend closed")
	ErrInvalidCursor     = errors.New("backend: invalid cursor")
	ErrScopeNotFound     = errors.New("backend: fetch scope not found")
	ErrNotImplemented    = errors.New("backend: operation not implemented")
	ErrReconnectRequired = errors.New("backend: reconnect required")

	// ErrPermissionDenied means the provider refused access to a folder that
	// exists. Trying again doesn't help.
	ErrPermissionDenied = errors.New("backend: permission denied")
)

// Problem is an error whose text is safe and useful to show to the user. Err
// holds the category, such as ErrReconnectRequired. The category decides the
// exit code and whether mailmd tries again.
type Problem struct {
	// Text says what went wrong, as a full sentence.
	Text string
	// Hint says what the user can do next. It may be empty.
	Hint string
	Err  error
}

func (p *Problem) Error() string { return p.Text }
func (p *Problem) Unwrap() error { return p.Err }

// ServerText cleans text that a mail server sent, so it can be shown in one
// line of an error message.
func ServerText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "..."
	}
	return s
}

// RetryableError marks a failure that may go away when mailmd tries again.
// RetryAfter is how long the provider asked mailmd to wait, if it said.
type RetryableError struct {
	Cause      error
	RetryAfter time.Duration
}

func (e *RetryableError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause == nil {
		return "backend: retryable error"
	}
	return "backend: retryable error: " + e.Cause.Error()
}

func (e *RetryableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *RetryableError) Is(target error) bool {
	return target == ErrRetryable
}

// Retryable marks cause as worth another try. Don't use it for context
// errors. Return those as they are.
func Retryable(cause error, retryAfter time.Duration) *RetryableError {
	return &RetryableError{Cause: cause, RetryAfter: retryAfter}
}

// ErrCloseFailed means a backend could not close its connection. It carries
// no provider text.
var ErrCloseFailed = errors.New("backend: disconnect failed")

// Close closes b, which may be nil, and turns any failure into
// ErrCloseFailed.
func Close(b Backend) error {
	if b != nil && b.Close() != nil {
		return ErrCloseFailed
	}
	return nil
}

// IsTransientReadError reports whether a read failed because the connection
// broke or timed out. Parse errors, size limits and a canceled context don't
// count.
func IsTransientReadError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var network net.Error
	return errors.As(err, &network) && network.Timeout()
}

// RetryAfter reads a Retry-After header, given in seconds or as an HTTP date.
// A number of seconds that doesn't fit into a Duration becomes the largest
// Duration, so the math can't overflow.
func RetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	digits := raw != ""
	for _, c := range raw {
		digits = digits && c >= '0' && c <= '9'
	}
	if digits {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || seconds > uint64(math.MaxInt64/int64(time.Second)) {
			return time.Duration(math.MaxInt64)
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}

// The state file stores cursors in base64. A backend's cursor must stay under
// 4 MiB once encoded, which leaves MaxCursorBytes for the raw cursor.
const MaxEncodedCursorBytes = 4 << 20
const MaxCursorBytes = MaxEncodedCursorBytes / 4 * 3

var ErrProviderStateLimit = errors.New("backend: provider state exceeds supported limits")
