package imap

import (
	"errors"
	"testing"

	"github.com/vincentsch/mailmd/internal/backend"
)

func TestCursorRoundTrip(t *testing.T) {
	scope := encodeScopeKey("INBOX", "/", true)
	for _, c := range []cursor{
		{validity: 1},
		{validity: 7, last: 9},
		{validity: 4294967295, last: 4294967295, modseq: 1 << 60, exists: 12, uidNext: 13},
	} {
		got, err := decodeCursor(scope, encodeCursor(scope, c))
		if err != nil || got != c {
			t.Fatalf("round trip of %+v = %+v, %v", c, got, err)
		}
	}
}

func TestDecodeCursorRejectsDamage(t *testing.T) {
	scope := encodeScopeKey("INBOX", "/", true)
	good := encodeCursor(scope, cursor{validity: 3, last: 4})
	for name, raw := range map[string][]byte{
		"empty":          nil,
		"wrong magic":    append([]byte("mailmd-imap-cursor-v9\x00"), good[len(cursorMagic):]...),
		"other scope":    encodeCursor(encodeScopeKey("Sent", "/", true), cursor{validity: 3}),
		"zero validity":  encodeCursor(scope, cursor{}),
		"cut off":        good[:len(good)-1],
		"trailing bytes": append(append([]byte(nil), good...), 0),
	} {
		if _, err := decodeCursor(scope, raw); !errors.Is(err, backend.ErrInvalidCursor) {
			t.Errorf("%s: decodeCursor() = %v, want ErrInvalidCursor", name, err)
		}
	}
}
