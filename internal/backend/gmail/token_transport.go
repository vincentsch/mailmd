package gmail

import (
	"context"
	"github.com/vincentsch/mailmd/internal/backend"
	"net/http"

	"golang.org/x/oauth2"
)

// contextTokenTransport signs each request with a token from source. When the
// source has TokenContext, a canceled request also stops a token refresh. The
// auth package's sources save a renewed login before they return it, except in
// a dry run, where they only hold it in memory.
type contextTokenTransport struct {
	source oauth2.TokenSource
	base   http.RoundTripper
}

func (t contextTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var token *oauth2.Token
	var err error
	if source, ok := t.source.(interface {
		TokenContext(context.Context) (*oauth2.Token, error)
	}); ok {
		token, err = source.TokenContext(req.Context())
	} else {
		token, err = t.source.Token()
	}
	if err != nil {
		return nil, err
	}
	if token == nil || token.AccessToken == "" {
		return nil, backend.ErrReconnectRequired
	}
	copy := req.Clone(req.Context())
	token.SetAuthHeader(copy)
	return t.base.RoundTrip(copy)
}
func (t contextTokenTransport) CloseIdleConnections() {
	if transport, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}
