package gmail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
	"golang.org/x/oauth2"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

var ErrInvalidConfig = errors.New("gmail: invalid config")

// Config holds what Open needs to reach Gmail. If HTTPClient is set, it must
// already be signed in, and Open uses it instead of TokenSource. UserID
// defaults to "me".
type Config struct {
	TokenSource oauth2.TokenSource
	HTTPClient  *http.Client
	// Transport carries the requests that TokenSource signs. Tests use it to
	// talk to a fake Gmail. HTTPClient wins when both are set.
	Transport http.RoundTripper
	UserID    string
	// IdleTimeout is how long a request may wait for the next bytes. Zero
	// means backend.DefaultIdleTimeout.
	IdleTimeout time.Duration

	client gmailClient
}

// Backend reads one Gmail account through the Gmail API. It runs one call at
// a time, so two fetches never mix up their cursors.
type Backend struct {
	userID string
	client gmailClient

	ownedHTTPClient *http.Client

	stateMu sync.Mutex
	opMu    sync.Mutex
	closed  bool
}

var (
	_ mdbackend.Backend   = (*Backend)(nil)
	_ mdbackend.Addresser = (*Backend)(nil)
)

// Open checks the config and sets up the Gmail API client. It makes no
// request yet.
func Open(ctx context.Context, cfg Config) (*Backend, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	userID := cfg.UserID
	if userID == "" {
		userID = "me"
	}

	var gc gmailClient
	var owned *http.Client
	if cfg.client != nil {
		gc = cfg.client
	} else {
		httpClient := cfg.HTTPClient
		if httpClient == nil {
			if cfg.TokenSource == nil {
				return nil, fmt.Errorf("%w: missing authenticated HTTP client or token source", ErrInvalidConfig)
			}
			base := cfg.Transport
			if base == nil {
				base = http.DefaultTransport
			}
			httpClient = &http.Client{Transport: contextTokenTransport{source: cfg.TokenSource, base: base}}
			owned = httpClient
		}
		// Every request fails when Gmail sends nothing for two minutes, so
		// a stalled connection cannot hold the archive lock forever.
		boundedClient := *httpClient
		boundedClient.Transport = boundedTransport{base: mdbackend.IdleTimeout(httpClient.Transport, cfg.IdleTimeout)}
		httpClient = &boundedClient
		svc, err := gmailapi.NewService(ctx, option.WithHTTPClient(httpClient))
		if err != nil {
			return nil, fmt.Errorf("gmail: construct service: %w", err)
		}
		gc = officialClient{svc: svc}
	}

	return &Backend{
		userID:          userID,
		client:          gc,
		ownedHTTPClient: owned,
	}, nil
}

func (b *Backend) Name() string {
	return "gmail"
}

// Folders lists the Gmail labels that work like folders. Labels like UNREAD
// are left out here. They become flags when a message is fetched.
func (b *Backend) Folders(ctx context.Context) ([]mdbackend.Folder, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b == nil || b.isClosed() {
		return nil, fmt.Errorf("gmail: list folders on closed backend: %w", mdbackend.ErrClosed)
	}

	b.opMu.Lock()
	defer b.opMu.Unlock()

	if b.isClosed() {
		return nil, fmt.Errorf("gmail: list folders on closed backend: %w", mdbackend.ErrClosed)
	}
	labels, err := b.client.Labels(ctx, b.userID)
	if err != nil {
		return nil, classifyAPIError(err)
	}
	catalog := buildLabelCatalog(labels)
	return catalog.folders(), nil
}

// Address returns the email address of the Gmail account.
func (b *Backend) Address(ctx context.Context) (string, error) {
	ctx = nonNilContext(ctx)
	if b == nil || b.isClosed() {
		return "", fmt.Errorf("gmail: address on closed backend: %w", mdbackend.ErrClosed)
	}
	b.opMu.Lock()
	defer b.opMu.Unlock()
	profile, err := b.client.Profile(ctx, b.userID)
	if err != nil {
		return "", classifyAPIError(err)
	}
	return profile.EmailAddress, nil
}

// Fetch checks the request and the cursor before it calls Gmail. Then it
// either goes on listing all messages, or it reads the history of changes
// since the last sync.
func (b *Backend) Fetch(ctx context.Context, req mdbackend.FetchRequest) (mdbackend.FetchResult, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return mdbackend.FetchResult{}, err
	}
	if b == nil || b.isClosed() {
		return mdbackend.FetchResult{}, fmt.Errorf("gmail: fetch on closed backend: %w", mdbackend.ErrClosed)
	}
	req, err := req.WithDefaults()
	if err != nil {
		return mdbackend.FetchResult{}, err
	}
	ctx = context.WithValue(ctx, fetchBudgetKey{}, req)
	if err := validateFetchScope(req.ScopeKey); err != nil {
		return mdbackend.FetchResult{}, err
	}

	var state *cursorState
	if len(req.Cursor) > 0 {
		state, err = decodeCursor(req.Cursor)
		if err != nil {
			return mdbackend.FetchResult{}, err
		}
	}

	b.opMu.Lock()
	defer b.opMu.Unlock()

	if b.isClosed() {
		return mdbackend.FetchResult{}, fmt.Errorf("gmail: fetch on closed backend: %w", mdbackend.ErrClosed)
	}
	return b.fetch(ctx, req, state)
}

// Close marks the backend as closed. If Open made the HTTP client itself, Close
// also closes its idle connections.
func (b *Backend) Close() error {
	if b == nil {
		return nil
	}
	c := b.markClosed()
	if c != nil {
		c.CloseIdleConnections()
	}
	return nil
}

func (b *Backend) isClosed() bool {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return b.closed
}

func (b *Backend) markClosed() *http.Client {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return b.ownedHTTPClient
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
