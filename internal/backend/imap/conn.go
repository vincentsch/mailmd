package imap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/vincentsch/mailmd/internal/backend"
)

const (
	dialTimeout = 30 * time.Second
	// DefaultIdleTimeout is how long mailmd waits for the next bytes from the
	// server before it gives up on the connection.
	DefaultIdleTimeout = 2 * time.Minute
)

// errResponseTooLarge is returned by conn.Read when the server sends more
// data than the current command can need.
var errResponseTooLarge = errors.New("imap: server response is larger than expected")

// conn wraps the network connection. Every read must make progress within
// the idle timeout, so a silent server cannot hang a sync while a slow but
// working one can still send a large message. It also limits how many bytes
// one command may receive, so a broken server cannot fill memory.
//
// go-imap sets its own absolute read deadlines, which would cut off a large
// message on a slow line. conn ignores those and uses its own.
type conn struct {
	net.Conn
	idle     atomic.Int64 // time.Duration
	budget   atomic.Int64
	exceeded atomic.Bool
	timedOut atomic.Bool
}

func newConn(raw net.Conn, idle time.Duration) *conn {
	c := &conn{Conn: raw}
	c.idle.Store(int64(idle))
	c.allow(1 << 20)
	return c
}

func (c *conn) Read(p []byte) (int, error) {
	if c.budget.Load() <= 0 {
		c.exceeded.Store(true)
		return 0, errResponseTooLarge
	}
	_ = c.Conn.SetReadDeadline(time.Now().Add(time.Duration(c.idle.Load())))
	n, err := c.Conn.Read(p)
	c.budget.Add(-int64(n))
	if isTimeout(err) {
		// go-imap would go on reading after a timeout. Close the connection
		// so every later read fails at once.
		c.timedOut.Store(true)
		_ = c.Conn.Close()
	}
	return n, err
}

func (c *conn) SetReadDeadline(time.Time) error { return nil }

func (c *conn) SetDeadline(t time.Time) error { return c.Conn.SetWriteDeadline(t) }

// allow sets how many bytes the next command may receive.
func (c *conn) allow(n int64) {
	c.exceeded.Store(false)
	c.budget.Store(n)
}

// dial connects to the server and logs in.
func dial(ctx context.Context, cfg Config) (*imapclient.Client, *conn, error) {
	addr := address(cfg)
	raw, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, dialProblem(ctx, cfg, addr, err)
	}
	idle := cfg.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	// Until the login is done, the server should answer quickly.
	c := newConn(raw, min(dialTimeout, idle))

	// The steps up to the login don't take a context. Closing the
	// connection stops whichever step is waiting.
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()

	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: cfg.RootCAs}
	options := &imapclient.Options{TLSConfig: tlsConfig}
	var client *imapclient.Client
	switch cfg.TLSMode {
	case TLSImplicit:
		tlsConn := tls.Client(c, tlsConfig)
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, nil, tlsProblem(ctx, cfg, addr, err)
		}
		client = imapclient.New(tlsConn, options)
		if err = client.WaitGreeting(); err != nil {
			_ = client.Close()
			return nil, nil, greetingProblem(ctx, cfg, addr, err)
		}
	case TLSStartTLS:
		client, err = imapclient.NewStartTLS(c, options)
		if err != nil {
			_ = raw.Close()
			var imapErr *imap.Error
			if errors.As(err, &imapErr) {
				return nil, nil, &backend.Problem{
					Text: fmt.Sprintf("%s refused STARTTLS: %s", addr, backend.ServerText(imapErr.Text)),
					Hint: "Try --tls implicit, usually on port 993.",
					Err:  ErrProtocol,
				}
			}
			if isTLSError(err) {
				return nil, nil, tlsProblem(ctx, cfg, addr, err)
			}
			return nil, nil, greetingProblem(ctx, cfg, addr, err)
		}
	default:
		_ = raw.Close()
		return nil, nil, invalidConfig("TLS mode must be implicit or starttls")
	}

	if err := client.Login(cfg.Username, cfg.Password).Wait(); err != nil {
		_ = client.Close()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		var imapErr *imap.Error
		if errors.As(err, &imapErr) {
			text := backend.ServerText(imapErr.Text)
			if text == "" {
				text = string(imapErr.Type)
			}
			return nil, nil, &backend.Problem{
				Text: fmt.Sprintf("%s rejected the login for %s. The server said: %s", cfg.Host, cfg.Username, text),
				Hint: loginHint(cfg.Host, text),
				Err:  ErrAuth,
			}
		}
		return nil, nil, transportProblem(ctx, addr, err)
	}
	if err := ctx.Err(); err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	c.idle.Store(int64(idle))
	return client, c, nil
}

func loginHint(host, text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "application-specific password") || strings.Contains(lower, "app password"):
		return "This provider needs an app password instead of your normal password. Create one in your account's security settings and run connect again."
	case strings.Contains(strings.ToLower(host), "outlook") || strings.Contains(strings.ToLower(host), "office365"):
		return "Microsoft no longer accepts passwords over IMAP for most accounts. Use: mailmd connect outlook"
	}
	return "Check the username and password. Many providers need an app password for IMAP. The IMAP guide lists them: https://github.com/vincentsch/mailmd/blob/main/docs/imap.md"
}

// dialProblem explains why the TCP connection failed.
func dialProblem(ctx context.Context, cfg Config, addr string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		return &backend.Problem{
			Text: fmt.Sprintf("Cannot find the server %s. The DNS lookup failed.", cfg.Host),
			Hint: "Check the host name for typos, and check that this computer is online.",
			Err:  backend.Retryable(err, 0),
		}
	case connectionRefused(err):
		return &backend.Problem{
			Text: fmt.Sprintf("%s refused the connection.", addr),
			Hint: "Check the port. Implicit TLS usually uses port 993 and STARTTLS port 143.",
			Err:  backend.Retryable(err, 0),
		}
	case isTimeout(err):
		return &backend.Problem{
			Text: fmt.Sprintf("%s did not answer within %s.", addr, dialTimeout),
			Hint: "Check the host and port, and whether a firewall blocks the connection.",
			Err:  backend.Retryable(err, 0),
		}
	}
	return &backend.Problem{
		Text: fmt.Sprintf("Cannot connect to %s: %s.", addr, cleanNetError(err)),
		Err:  backend.Retryable(err, 0),
	}
}

// tlsProblem explains a failed TLS handshake. Certificate problems are not
// retried, because they do not fix themselves.
func tlsProblem(ctx context.Context, cfg Config, addr string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	switch {
	case errors.As(err, &unknown):
		return &backend.Problem{
			Text: fmt.Sprintf("The TLS certificate of %s is not trusted. It is not signed by a known certificate authority.", cfg.Host),
			Hint: "If the certificate comes from your own certificate authority, connect again with --ca-file and the CA's certificate in a PEM file.",
			Err:  ErrTLS,
		}
	case errors.As(err, &hostname):
		return &backend.Problem{
			Text: fmt.Sprintf("The TLS certificate of %s is for a different host name.", cfg.Host),
			Hint: "Use the host name that the certificate was made for.",
			Err:  ErrTLS,
		}
	case errors.As(err, &invalid):
		return &backend.Problem{
			Text: fmt.Sprintf("The TLS certificate of %s is not valid: %s.", cfg.Host, invalidReason(invalid)),
			Err:  ErrTLS,
		}
	case errors.As(err, &record):
		return &backend.Problem{
			Text: fmt.Sprintf("%s did not answer with TLS.", addr),
			Hint: "This port may expect STARTTLS. Try --tls starttls with port 143.",
			Err:  ErrTLS,
		}
	case isTimeout(err):
		return &backend.Problem{
			Text: fmt.Sprintf("The TLS handshake with %s timed out.", addr),
			Hint: "This port may expect STARTTLS. Try --tls starttls with port 143.",
			Err:  backend.Retryable(err, 0),
		}
	}
	if isTransient(err) {
		return transportProblem(ctx, addr, err)
	}
	return &backend.Problem{
		Text: fmt.Sprintf("The TLS handshake with %s failed: %s.", addr, cleanNetError(err)),
		Err:  ErrTLS,
	}
}

func invalidReason(err x509.CertificateInvalidError) string {
	switch err.Reason {
	case x509.Expired:
		return "it has expired or is not valid yet"
	case x509.NotAuthorizedToSign:
		return "it was signed by a certificate that may not sign others"
	}
	return "it was rejected"
}

// greetingProblem explains a server that accepted the connection but never
// greeted, which usually means the TLS mode does not match the port.
func greetingProblem(ctx context.Context, cfg Config, addr string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	hint := "Check that this is an IMAP port."
	if cfg.TLSMode == TLSStartTLS {
		hint = "This port may expect implicit TLS. Try --tls implicit with port 993."
	}
	return &backend.Problem{Text: fmt.Sprintf("%s did not send an IMAP greeting.", addr), Hint: hint, Err: backend.Retryable(err, 0)}
}

// transportProblem reports a connection that broke while in use. The sync
// tries again with a new connection.
func transportProblem(ctx context.Context, addr string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	text := fmt.Sprintf("The connection to %s was lost.", addr)
	if isTimeout(err) {
		text = fmt.Sprintf("%s stopped answering.", addr)
	}
	return &backend.Problem{Text: text, Err: backend.Retryable(err, 0)}
}

func isTLSError(err error) bool {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	return errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) ||
		errors.As(err, &record) || errors.As(err, &alert)
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// isTransient reports whether a connection broke or stalled. go-imap reports
// some of these only as text, so mailmd also checks the text.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNABORTED) ||
		isTimeout(err) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, text := range []string{"connection closed", "connection reset", "broken pipe", "closed network connection", "unexpected eof", "i/o timeout"} {
		if strings.Contains(s, text) {
			return true
		}
	}
	return false
}

// cleanNetError returns the useful last part of a Go network error, such as
// "network is unreachable".
func cleanNetError(err error) string {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		err = opErr.Err
	}
	return backend.ServerText(err.Error())
}
