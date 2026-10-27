package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
	"google.golang.org/api/googleapi"
)

type fetchBudgetKey struct{}

func requestBudget(ctx context.Context) mdbackend.FetchRequest {
	if request, ok := ctx.Value(fetchBudgetKey{}).(mdbackend.FetchRequest); ok {
		return request
	}
	request, _ := (mdbackend.FetchRequest{}).WithDefaults()
	return request
}

// boundedTransport reads each answer in full and refuses one that is too
// large, before Google's library parses the JSON. A raw message comes as
// base64url, so its answer may be as large as the encoded size limit plus
// room for the other fields. The decoded size is checked later, before the
// MIME parsing.
type boundedTransport struct{ base http.RoundTripper }

func (t boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		if mdbackend.IsTransientReadError(err) {
			return nil, mdbackend.Retryable(errors.New("gmail: transport failure"), 0)
		}
		return nil, err
	}
	budget := requestBudget(req.Context())
	maximum := budget.MaxBatchBytes
	message := req.URL.Query().Get("format") == "raw" && strings.Contains(req.URL.Path, "/messages/")
	if message {
		// A huge --max-message-bytes would overflow the math, so use the
		// largest limit instead.
		if budget.MaxMessageBytes > (math.MaxInt64-budget.MaxBatchBytes-4)/4*3 {
			maximum = math.MaxInt64 - 1
		} else {
			maximum = (budget.MaxMessageBytes+2)/3*4 + budget.MaxBatchBytes
		}
	}
	data, readErr := mdbackend.ReadBounded(response.Body, maximum)
	if readErr == nil && response.ContentLength > int64(len(data)) {
		readErr = io.ErrUnexpectedEOF
	}
	_ = response.Body.Close()
	if readErr != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		if response.StatusCode >= 400 {
			return nil, &googleapi.Error{Code: response.StatusCode, Header: response.Header}
		}
		if mdbackend.IsTransientReadError(readErr) {
			return nil, mdbackend.Retryable(errors.New("gmail: transport failure"), 0)
		}
		if message && errors.Is(readErr, mdbackend.ErrResponseTooLarge) {
			return nil, &mdbackend.MessageTooLargeError{Backend: "gmail", ScopeKey: accountScopeKey, MaximumBytes: budget.MaxMessageBytes}
		}
		return nil, readErr
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	return response, nil
}

func rawMessageBytes(raw string, maximum int64) int64 {
	encoding := base64.RawURLEncoding
	if strings.HasSuffix(raw, "=") {
		encoding = base64.URLEncoding
	}
	n, err := io.Copy(io.Discard, io.LimitReader(base64.NewDecoder(encoding, strings.NewReader(raw)), maximum+1))
	if err != nil {
		// Base64 that doesn't decode still counts against the batch, with its
		// encoded size.
		return int64(len(raw))
	}
	return n
}
