package imap

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/vincentsch/mailmd/internal/backend"
)

// TLSMode says how the connection to the server is encrypted.
type TLSMode string

const (
	TLSImplicit TLSMode = "implicit"
	TLSStartTLS TLSMode = "starttls"
)

// Config holds the settings for Open.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	TLSMode  TLSMode
	RootCAs  *x509.CertPool
	// IdleTimeout is how long a read may wait for the next bytes. Zero means
	// DefaultIdleTimeout.
	IdleTimeout time.Duration
	// Log receives details about protocol problems, for --verbose. It never
	// receives message content or the password.
	Log io.Writer
}

// Backend is a read-only IMAP backend. It opens mailboxes with EXAMINE and
// fetches with BODY.PEEK, so it never changes flags or messages.
type Backend struct {
	// mu lets one command run at a time, because an IMAP connection has only
	// one selected mailbox.
	mu sync.Mutex
	// stateMu protects closed. terminate takes it without taking mu.
	stateMu sync.Mutex
	closed  bool
	client  *imapclient.Client
	conn    *conn
	host    string
	log     io.Writer
	pending map[string]*pendingUIDs
}

var _ backend.Backend = (*Backend)(nil)

// Open connects to the server and logs in.
func Open(ctx context.Context, cfg Config) (*Backend, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, c, err := dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Backend{client: client, conn: c, host: cfg.Host, log: cfg.Log, pending: map[string]*pendingUIDs{}}, nil
}

// Name returns the backend kind.
func (b *Backend) Name() string { return "imap" }

// Folders lists the mailboxes.
func (b *Backend) Folders(ctx context.Context) ([]backend.Folder, error) {
	unlock, err := b.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	b.conn.allow(32 << 20)
	list, err := b.client.List("", "*", nil).Collect()
	if err != nil {
		return nil, b.commandError(ctx, "list the folders", err)
	}
	return foldersFromList(list), nil
}

// Close drops the connection. It is safe to call more than once.
func (b *Backend) Close() error {
	if b == nil {
		return nil
	}
	b.terminate()
	return nil
}

// begin takes the command lock and arranges for the connection to close
// when ctx ends, because go-imap commands do not take a context.
func (b *Backend) begin(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.isClosed() {
		b.mu.Unlock()
		return nil, fmt.Errorf("imap: backend is closed: %w", backend.ErrClosed)
	}
	stop := context.AfterFunc(ctx, b.terminate)
	return func() {
		stop()
		b.mu.Unlock()
	}, nil
}

func (b *Backend) isClosed() bool {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return b.closed
}

// terminate closes the connection without waiting for the command lock, so
// it can interrupt a command that is waiting for the server.
func (b *Backend) terminate() {
	b.stateMu.Lock()
	if b.closed {
		b.stateMu.Unlock()
		return
	}
	b.closed = true
	b.stateMu.Unlock()
	if b.client != nil {
		_ = b.client.Close()
	}
}

// commandError turns a failed command into an error the sync layer can act
// on. what describes the command, as in "list the folders".
func (b *Backend) commandError(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if b.conn.exceeded.Load() {
		b.terminate()
		return &backend.Problem{
			Text: fmt.Sprintf("%s sent more data than expected when asked to %s.", b.host, what),
			Err:  ErrProtocol,
		}
	}
	var imapErr *imap.Error
	if errors.As(err, &imapErr) {
		text := backend.ServerText(imapErr.Text)
		switch imapErr.Code {
		case imap.ResponseCodeNoPerm:
			return &backend.Problem{
				Text: fmt.Sprintf("%s did not allow mailmd to %s: %s", b.host, what, text),
				Err:  backend.ErrPermissionDenied,
			}
		case imap.ResponseCodeUnavailable, imap.ResponseCodeInUse, imap.ResponseCodeLimit, imap.ResponseCodeServerBug:
			return &backend.Problem{
				Text: fmt.Sprintf("%s could not %s right now: %s", b.host, what, text),
				Err:  backend.Retryable(err, 0),
			}
		}
		return &backend.Problem{
			Text: fmt.Sprintf("%s refused to %s: %s", b.host, what, text),
			Err:  ErrProtocol,
		}
	}
	if b.isClosed() {
		return fmt.Errorf("imap: backend is closed: %w", backend.ErrClosed)
	}
	if b.conn.timedOut.Load() {
		b.terminate()
		return &backend.Problem{Text: fmt.Sprintf("%s stopped answering.", b.host), Err: backend.Retryable(err, 0)}
	}
	if isTransient(err) {
		b.terminate()
		return transportProblem(ctx, b.host, err)
	}
	// Anything else is an answer go-imap could not read. The connection is
	// out of step after that, so it is closed.
	b.logf("%s: cannot read the server's answer when asked to %s: %v", b.host, what, err)
	b.terminate()
	return &backend.Problem{
		Text: fmt.Sprintf("Cannot read the answer of %s when asked to %s.", b.host, what),
		Hint: "Run the command again with --verbose to see the details.",
		Err:  ErrProtocol,
	}
}

func (b *Backend) logf(format string, args ...any) {
	if b.log != nil {
		fmt.Fprintf(b.log, "imap: "+format+"\n", args...)
	}
}

// canonicalMailbox treats INBOX case-insensitively, as RFC 3501 requires,
// and leaves every other name as it is.
func canonicalMailbox(name string) string {
	if strings.EqualFold(name, "INBOX") {
		return "INBOX"
	}
	return name
}
