package backend

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRetryableErrorContract(t *testing.T) {
	cause := errors.New("temporary failure")
	err := Retryable(cause, 2*time.Second)

	if !errors.Is(err, ErrRetryable) {
		t.Fatalf("errors.Is(Retryable, ErrRetryable) = false")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(Retryable, cause) = false")
	}
	var re *RetryableError
	if !errors.As(err, &re) {
		t.Fatalf("errors.As(Retryable, *RetryableError) = false")
	}
	if re.RetryAfter != 2*time.Second {
		t.Fatalf("RetryAfter = %v, want 2s", re.RetryAfter)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("retryable sample cause unexpectedly classified as context.Canceled")
	}

	unspecified := Retryable(nil, 0)
	if !errors.Is(unspecified, ErrRetryable) {
		t.Fatalf("errors.Is(Retryable(nil), ErrRetryable) = false")
	}
	if unspecified.RetryAfter != 0 {
		t.Fatalf("RetryAfter = %v, want 0", unspecified.RetryAfter)
	}
}

func TestContextErrorsAreNotRetryable(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, ErrRetryable) {
			t.Fatalf("%v classified as retryable", err)
		}
	}
}

func TestSentinelWrapping(t *testing.T) {
	for _, sentinel := range []error{
		ErrClosed,
		ErrInvalidCursor,
		ErrScopeNotFound,
		ErrPermissionDenied,
		ErrNotImplemented,
		ErrReconnectRequired,
	} {
		err := fmt.Errorf("wrapped: %w", sentinel)
		if !errors.Is(err, sentinel) {
			t.Fatalf("errors.Is(%v, %v) = false", err, sentinel)
		}
	}
}

func TestReconnectRequiredIsNotRetryableOrContext(t *testing.T) {
	err := fmt.Errorf("credentials revoked: %w", ErrReconnectRequired)
	if !errors.Is(err, ErrReconnectRequired) {
		t.Fatalf("errors.Is(reconnect, ErrReconnectRequired) = false")
	}
	if errors.Is(err, ErrRetryable) {
		t.Fatalf("reconnect required classified as retryable")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("reconnect required classified as context.Canceled")
	}
}
