package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// DefaultIdleTimeout is how long mailmd waits for the next bytes from a
// provider before it gives up on a request. It counts from the last bytes
// that arrived, so a large download on a slow line still finishes.
const DefaultIdleTimeout = 2 * time.Minute

// ErrStalled is returned when a provider sent nothing for the idle timeout.
// It counts as a timeout, so callers retry it.
var ErrStalled error = stalledError{}

type stalledError struct{}

func (stalledError) Error() string   { return "backend: the provider stopped answering" }
func (stalledError) Timeout() bool   { return true }
func (stalledError) Temporary() bool { return true }

// IdleTimeout wraps an HTTP transport so that a request fails when no bytes
// arrive for idle, while it waits for the answer or reads its body.
func IdleTimeout(base http.RoundTripper, idle time.Duration) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	return idleTransport{base: base, idle: idle}
}

type idleTransport struct {
	base http.RoundTripper
	idle time.Duration
}

func (t idleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	timer := time.AfterFunc(t.idle, func() { cancel(ErrStalled) })
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		timer.Stop()
		stalled := errors.Is(context.Cause(ctx), ErrStalled)
		cancel(nil)
		if stalled {
			return nil, ErrStalled
		}
		return nil, err
	}
	timer.Reset(t.idle)
	resp.Body = &idleBody{body: resp.Body, ctx: ctx, cancel: cancel, timer: timer, idle: t.idle}
	return resp, nil
}

// CloseIdleConnections passes the call on, so closing a client still
// closes its connections.
func (t idleTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

type idleBody struct {
	body   io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	timer  *time.Timer
	idle   time.Duration
	once   sync.Once
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.timer.Reset(b.idle)
	}
	if err != nil && err != io.EOF && errors.Is(context.Cause(b.ctx), ErrStalled) {
		err = ErrStalled
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.once.Do(func() {
		b.timer.Stop()
		b.cancel(nil)
	})
	return b.body.Close()
}
