package imap

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"testing"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
)

func TestScopeKeyFrozenVectors(t *testing.T) {
	tests := []struct {
		identity    string
		delimiter   string
		present     bool
		wantPayload []byte
		wantKey     string
	}{
		{
			identity:  "INBOX",
			delimiter: "/",
			present:   true,
			wantPayload: []byte{
				109, 97, 105, 108, 109, 100, 45, 105, 109, 97, 112, 45, 115, 99, 111, 112, 101, 45, 118, 49, 0,
				5, 73, 78, 66, 79, 88, 1, 1, 47,
			},
			wantKey: "imap-v1:bWFpbG1kLWltYXAtc2NvcGUtdjEABUlOQk9YAQEv",
		},
		{
			identity:  "Work/Projects",
			delimiter: "/",
			present:   true,
			wantPayload: []byte{
				109, 97, 105, 108, 109, 100, 45, 105, 109, 97, 112, 45, 115, 99, 111, 112, 101, 45, 118, 49, 0,
				13, 87, 111, 114, 107, 47, 80, 114, 111, 106, 101, 99, 116, 115, 1, 1, 47,
			},
			wantKey: "imap-v1:bWFpbG1kLWltYXAtc2NvcGUtdjEADVdvcmsvUHJvamVjdHMBAS8",
		},
		{
			identity:  "Отчёты",
			delimiter: "/",
			present:   true,
			wantPayload: []byte{
				109, 97, 105, 108, 109, 100, 45, 105, 109, 97, 112, 45, 115, 99, 111, 112, 101, 45, 118, 49, 0,
				12, 208, 158, 209, 130, 209, 135, 209, 145, 209, 130, 209, 139, 1, 1, 47,
			},
			wantKey: "imap-v1:bWFpbG1kLWltYXAtc2NvcGUtdjEADNCe0YLRh9GR0YLRiwEBLw",
		},
		{
			identity: "Flat",
			present:  false,
			wantPayload: []byte{
				109, 97, 105, 108, 109, 100, 45, 105, 109, 97, 112, 45, 115, 99, 111, 112, 101, 45, 118, 49, 0,
				4, 70, 108, 97, 116, 0,
			},
			wantKey: "imap-v1:bWFpbG1kLWltYXAtc2NvcGUtdjEABEZsYXQA",
		},
		{
			identity:  "Foo.Bar",
			delimiter: ".",
			present:   true,
			wantPayload: []byte{
				109, 97, 105, 108, 109, 100, 45, 105, 109, 97, 112, 45, 115, 99, 111, 112, 101, 45, 118, 49, 0,
				7, 70, 111, 111, 46, 66, 97, 114, 1, 1, 46,
			},
			wantKey: "imap-v1:bWFpbG1kLWltYXAtc2NvcGUtdjEAB0Zvby5CYXIBAS4",
		},
		{
			identity:  "Foo/Bar",
			delimiter: ".",
			present:   true,
			wantPayload: []byte{
				109, 97, 105, 108, 109, 100, 45, 105, 109, 97, 112, 45, 115, 99, 111, 112, 101, 45, 118, 49, 0,
				7, 70, 111, 111, 47, 66, 97, 114, 1, 1, 46,
			},
			wantKey: "imap-v1:bWFpbG1kLWltYXAtc2NvcGUtdjEAB0Zvby9CYXIBAS4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.identity, func(t *testing.T) {
			got := encodeScopeKey(tt.identity, tt.delimiter, tt.present)
			if got != tt.wantKey {
				t.Fatalf("encodeScopeKey() = %q, want %q", got, tt.wantKey)
			}
			payload := mustPayload(t, got)
			if !bytes.Equal(payload, tt.wantPayload) {
				t.Fatalf("payload = %v, want %v", payload, tt.wantPayload)
			}

			identity, delimiter, present, err := decodeScopeKey(got)
			if err != nil {
				t.Fatalf("decodeScopeKey() error = %v", err)
			}
			wantIdentity := canonicalMailbox(tt.identity)
			if identity != wantIdentity || delimiter != tt.delimiter || present != tt.present {
				t.Fatalf("decodeScopeKey() = (%q, %q, %v), want (%q, %q, %v)", identity, delimiter, present, wantIdentity, tt.delimiter, tt.present)
			}
		})
	}

}

func TestScopeKeyStabilityAndInboxCanonicalization(t *testing.T) {
	inbox := encodeScopeKey("INBOX", "/", true)
	for _, name := range []string{"inbox", "InBox", "INBOX"} {
		if got := encodeScopeKey(name, "/", true); got != inbox {
			t.Fatalf("encodeScopeKey(%q) = %q, want %q", name, got, inbox)
		}
	}
	if got := encodeScopeKey("Work", "/", true); got != encodeScopeKey("Work", "/", true) {
		t.Fatalf("same mailbox produced different keys: %q", got)
	}
}

func TestScopeKeyDisplayCollisionStaysDistinct(t *testing.T) {
	a := encodeScopeKey("Foo.Bar", ".", true)
	b := encodeScopeKey("Foo/Bar", ".", true)
	if a == b {
		t.Fatalf("collision pair produced same key %q", a)
	}
}

func TestDecodeScopeKeyRejectsMalformedInputs(t *testing.T) {
	valid := rawScopeKey(frozenPayload("INBOX", "/", true))
	trailing := append(frozenPayload("INBOX", "/", true), 0)

	tests := []struct {
		name string
		key  string
	}{
		{name: "bad prefix", key: "imap-v2:abc"},
		{name: "bad base64", key: "imap-v1:@@"},
		{name: "wrong magic", key: rawScopeKey([]byte("bad"))},
		{name: "trailing bytes", key: rawScopeKey(trailing)},
		{name: "bad delimiter kind", key: rawScopeKey(append(frozenPayload("A", "", false)[:len(scopeKeyMagic)+2], 2))},
		{name: "truncated identity", key: rawScopeKey(append([]byte(scopeKeyMagic), 2, 'A'))},
		{name: "overlong uvarint", key: rawScopeKey(append([]byte(scopeKeyMagic), bytes.Repeat([]byte{0x80}, binary.MaxVarintLen64+1)...))},
		{name: "invalid identity utf8", key: rawScopeKey(append([]byte(scopeKeyMagic), 1, 0xff, 0))},
		{name: "empty identity", key: rawScopeKey(append([]byte(scopeKeyMagic), 0, 0))},
		{name: "missing delimiter length", key: rawScopeKey(append([]byte(scopeKeyMagic), 1, 'A', 1))},
		{name: "truncated delimiter", key: rawScopeKey(append([]byte(scopeKeyMagic), 1, 'A', 1, 2, '.'))},
		{name: "invalid delimiter utf8", key: rawScopeKey(append([]byte(scopeKeyMagic), 1, 'A', 1, 1, 0xff))},
	}

	if _, _, _, err := decodeScopeKey(valid); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := decodeScopeKey(tt.key)
			if !errors.Is(err, ErrScopeKeyMalformed) {
				t.Fatalf("decodeScopeKey() error = %v, want ErrScopeKeyMalformed", err)
			}
			if !errors.Is(err, mdbackend.ErrInvalidCursor) {
				t.Fatalf("decodeScopeKey() error = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func mustPayload(t *testing.T, key string) []byte {
	t.Helper()
	payload, err := base64.RawURLEncoding.DecodeString(key[len(scopeKeyPrefix):])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload
}

func rawScopeKey(payload []byte) string {
	return scopeKeyPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

func frozenPayload(identity, delimiter string, present bool) []byte {
	identity = canonicalMailbox(identity)
	var buf bytes.Buffer
	buf.WriteString("mailmd-imap-scope-v1\x00")
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(len(identity)))
	buf.Write(tmp[:n])
	buf.WriteString(identity)
	if present {
		buf.WriteByte(1)
		n = binary.PutUvarint(tmp[:], uint64(len(delimiter)))
		buf.Write(tmp[:n])
		buf.WriteString(delimiter)
	} else {
		buf.WriteByte(0)
	}
	return buf.Bytes()
}
