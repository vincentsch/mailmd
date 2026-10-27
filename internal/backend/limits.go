package backend

import (
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	DefaultFetchLimit            = 25
	DefaultMaxMessageBytes int64 = 64 << 20
	DefaultMaxBatchBytes   int64 = 128 << 20
)

var (
	ErrInvalidLimits    = errors.New("backend: invalid fetch limits")
	ErrMessageTooLarge  = errors.New("backend: message too large")
	ErrResponseTooLarge = errors.New("backend: metadata response too large")
)

// WithDefaults fills in the default limits and checks them before the backend
// talks to the provider. A limit can't be MaxInt64, because the readers read
// one byte more than the limit to tell an exact fit from a message that is
// too large.
func (r FetchRequest) WithDefaults() (FetchRequest, error) {
	if r.Limit < 0 || r.MaxMessageBytes < 0 || r.MaxBatchBytes < 0 ||
		r.MaxMessageBytes == math.MaxInt64 || r.MaxBatchBytes == math.MaxInt64 {
		return FetchRequest{}, ErrInvalidLimits
	}
	if r.Limit == 0 {
		r.Limit = DefaultFetchLimit
	}
	if r.MaxMessageBytes == 0 {
		r.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if r.MaxBatchBytes == 0 {
		r.MaxBatchBytes = DefaultMaxBatchBytes
	}
	if r.MaxBatchBytes < r.MaxMessageBytes {
		return FetchRequest{}, ErrInvalidLimits
	}
	return r, nil
}

// MessageTooLargeError says which message was too large and how large it
// was. Its text leaves out the scope and the ref, so it can't show provider
// data even when a ref holds some.
type MessageTooLargeError struct {
	Backend       string
	ScopeKey      string
	Ref           string
	ObservedBytes int64
	MaximumBytes  int64
}

func (e *MessageTooLargeError) Error() string {
	return fmt.Sprintf("backend: message exceeds %d byte maximum", e.MaximumBytes)
}
func (e *MessageTooLargeError) Unwrap() error { return ErrMessageTooLarge }

// ReadBounded reads at most maximum+1 bytes. When there is more than maximum,
// it returns ErrResponseTooLarge and no data, so a caller never parses an
// answer that was cut off.
func ReadBounded(r io.Reader, maximum int64) ([]byte, error) {
	if maximum < 0 || maximum == math.MaxInt64 {
		return nil, ErrInvalidLimits
	}
	data, err := io.ReadAll(io.LimitReader(r, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, ErrResponseTooLarge
	}
	return data, nil
}
