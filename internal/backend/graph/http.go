package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/vincentsch/mailmd/internal/backend"
	"golang.org/x/oauth2"
)

type providerError struct {
	Status int
	Code   string
	Cause  error
}

func (e *providerError) Error() string { return fmt.Sprintf("graph: HTTP %d (%s)", e.Status, e.Code) }
func (e *providerError) Unwrap() error { return e.Cause }

func safeCode(code string) string {
	switch code {
	case "ErrorAccessDenied", "Authorization_RequestDenied", "InvalidAuthenticationToken", "ErrorItemNotFound", "ErrorInvalidSyncStateData", "syncStateNotFound", "SyncStateNotFound", "InvalidDeltaToken", "TooManyRequests", "ServiceUnavailable":
		return code
	}
	return "request_failed"
}

func (b *Backend) token(ctx context.Context, refresh bool) (*oauth2.Token, error) {
	if refresh {
		if source, ok := b.tokens.(interface {
			RefreshContext(context.Context) (*oauth2.Token, error)
		}); ok {
			return source.RefreshContext(ctx)
		}
	}
	if source, ok := b.tokens.(interface {
		TokenContext(context.Context) (*oauth2.Token, error)
	}); ok {
		return source.TokenContext(ctx)
	}
	return b.tokens.Token()
}

func retryAfter(raw string, now time.Time) time.Duration { return backend.RetryAfter(raw, now) }

func (b *Backend) response(ctx context.Context, link string, maximum int64, pageSize int) ([]byte, error) {
	if !b.validURL(link) {
		return nil, ErrProtocol
	}
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		token, err := b.token(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		if token == nil || token.AccessToken == "" {
			return nil, backend.ErrReconnectRequired
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if err != nil {
			return nil, ErrProtocol
		}
		request.Header.Set("Authorization", "Bearer "+token.AccessToken)
		prefer := `IdType="ImmutableId"`
		if pageSize > 0 {
			prefer += ", odata.maxpagesize=" + strconv.Itoa(pageSize)
		}
		request.Header.Set("Prefer", prefer)
		response, err := b.client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, ErrProtocol) {
				return nil, ErrProtocol
			}
			var network net.Error
			if errors.As(err, &network) || errors.Is(err, io.EOF) {
				return nil, backend.Retryable(errors.New("graph: transport failure"), 0)
			}
			return nil, ErrProtocol
		}
		data, readErr := backend.ReadBounded(response.Body, maximum)
		if readErr == nil && response.ContentLength > int64(len(data)) {
			readErr = io.ErrUnexpectedEOF
		}
		_ = response.Body.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if response.StatusCode == 401 && attempt == 0 {
			continue
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			if readErr != nil {
				if backend.IsTransientReadError(readErr) {
					return nil, backend.Retryable(errors.New("graph: transport failure"), 0)
				}
				return nil, readErr
			}
			return data, nil
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if readErr == nil {
			_ = json.Unmarshal(data, &body)
		}
		err = &providerError{Status: response.StatusCode, Code: safeCode(body.Error.Code)}
		switch response.StatusCode {
		case 401:
			err.(*providerError).Cause = backend.ErrReconnectRequired
		case 403:
			err.(*providerError).Cause = backend.ErrPermissionDenied
		case 429, 503, 504:
			return nil, backend.Retryable(err, retryAfter(response.Header.Get("Retry-After"), b.clock()))
		}
		return nil, err
	}
	return nil, backend.ErrReconnectRequired
}

func (b *Backend) json(ctx context.Context, link string, maximum int64, pageSize int, target any) error {
	data, err := b.response(ctx, link, maximum, pageSize)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return ErrProtocol
	}
	return nil
}
