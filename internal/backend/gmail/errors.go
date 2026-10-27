package gmail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
	"google.golang.org/api/googleapi"
)

// classifyAPIError turns Gmail and Google API failures into backend errors.
// It never uses response bodies or messages from the provider.
func classifyAPIError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, mdbackend.ErrReconnectRequired) {
		return err
	}
	if errors.Is(err, mdbackend.ErrRetryable) {
		return err
	}
	if errors.Is(err, mdbackend.ErrResponseTooLarge) || errors.Is(err, mdbackend.ErrMessageTooLarge) {
		return err
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		retryAfter := parseRetryAfter(apiErr.Header.Get("Retry-After"))
		if apiErr.Code == http.StatusTooManyRequests ||
			(apiErr.Code == http.StatusForbidden && hasAnyReason(apiErr, "ratelimitexceeded", "userratelimitexceeded", "quotaexceeded")) ||
			apiErr.Code >= 500 {
			return mdbackend.Retryable(sanitizedHTTPError(apiErr.Code), retryAfter)
		}
		if apiErr.Code == http.StatusUnauthorized ||
			(apiErr.Code == http.StatusForbidden && hasInsufficientScopeReason(apiErr)) {
			return fmt.Errorf("gmail: reconnect required: %w", mdbackend.ErrReconnectRequired)
		}
		if reason := firstSafeReason(apiErr); reason != "" {
			return fmt.Errorf("gmail: api request failed: %s", reason)
		}
		return sanitizedHTTPError(apiErr.Code)
	}
	return errors.New("gmail: api request failed")
}

func sanitizedHTTPError(code int) error {
	return fmt.Errorf("gmail: api request failed with HTTP status %d", code)
}

// parseRetryAfter reads a Retry-After header, in seconds or as an HTTP date.
func parseRetryAfter(raw string) time.Duration { return mdbackend.RetryAfter(raw, time.Now()) }

func hasAnyReason(apiErr *googleapi.Error, reasons ...string) bool {
	for _, got := range apiReasons(apiErr) {
		for _, want := range reasons {
			if got == want {
				return true
			}
		}
	}
	return false
}

func hasInsufficientScopeReason(apiErr *googleapi.Error) bool {
	for _, reason := range apiReasons(apiErr) {
		if strings.Contains(reason, "insufficient") &&
			(strings.Contains(reason, "permission") || strings.Contains(reason, "scope")) {
			return true
		}
	}
	return false
}

func firstSafeReason(apiErr *googleapi.Error) string {
	for _, reason := range apiReasons(apiErr) {
		if reason != "" {
			return reason
		}
	}
	return ""
}

// apiReasons returns only Google's short reason codes. Response bodies and
// messages can contain details of the request, so mailmd doesn't use them.
func apiReasons(apiErr *googleapi.Error) []string {
	if apiErr == nil {
		return nil
	}
	out := make([]string, 0, len(apiErr.Errors))
	for _, item := range apiErr.Errors {
		reason := strings.ToLower(strings.TrimSpace(item.Reason))
		if reason != "" {
			out = append(out, reason)
		}
	}
	return out
}

// isStaleHistoryError reports whether Gmail no longer has the changes since
// the saved history ID. Then the backend lists all messages again.
func isStaleHistoryError(err error) bool {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Code != http.StatusNotFound {
		return false
	}
	if len(apiErr.Errors) == 0 {
		return true
	}
	return hasAnyReason(apiErr, "notfound", "historyidnotfound", "stalehistory")
}

// isInvalidPageTokenError reports whether a page token of a list expired.
func isInvalidPageTokenError(err error) bool {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == http.StatusBadRequest &&
		(len(apiErr.Errors) == 0 || hasAnyReason(apiErr, "invalidargument", "invalidpagetoken"))
}

// isVanishedMessageError reports whether a message was deleted before mailmd
// could fetch it. That becomes a diagnostic, and the fetch goes on.
func isVanishedMessageError(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound
}
