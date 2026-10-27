package imap

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	scopeKeyPrefix = "imap-v1:"
	scopeKeyMagic  = "mailmd-imap-scope-v1\x00"
)

// encodeScopeKey turns the mailbox name and its delimiter into a stable key
// that is safe to store in JSON. The sync state stores these keys.
func encodeScopeKey(identity, delimiter string, present bool) string {
	identity = canonicalMailbox(identity)

	var buf bytes.Buffer
	buf.WriteString(scopeKeyMagic)
	writeUvarint(&buf, uint64(len(identity)))
	buf.WriteString(identity)
	if present {
		buf.WriteByte(1)
		writeUvarint(&buf, uint64(len(delimiter)))
		buf.WriteString(delimiter)
	} else {
		buf.WriteByte(0)
	}

	return scopeKeyPrefix + base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

// decodeScopeKey is strict, so a damaged state file cannot make mailmd read
// the wrong mailbox.
func decodeScopeKey(key string) (identity, delimiter string, present bool, err error) {
	if !strings.HasPrefix(key, scopeKeyPrefix) {
		return "", "", false, malformedScopeKey("missing prefix")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, scopeKeyPrefix))
	if err != nil {
		return "", "", false, malformedScopeKey("base64 decode: %v", err)
	}
	if !bytes.HasPrefix(payload, []byte(scopeKeyMagic)) {
		return "", "", false, malformedScopeKey("wrong magic")
	}

	off := len(scopeKeyMagic)
	identityLen, n, err := readUvarint(payload[off:])
	if err != nil {
		return "", "", false, malformedScopeKey("identity length: %v", err)
	}
	off += n
	if identityLen == 0 {
		return "", "", false, malformedScopeKey("empty identity")
	}
	if identityLen > uint64(len(payload)-off) {
		return "", "", false, malformedScopeKey("truncated identity")
	}
	identityBytes := payload[off : off+int(identityLen)]
	off += int(identityLen)
	if !utf8.Valid(identityBytes) {
		return "", "", false, malformedScopeKey("invalid identity utf-8")
	}
	identity = string(identityBytes)

	// The marker byte tells a NIL delimiter apart from a real one.
	if off >= len(payload) {
		return "", "", false, malformedScopeKey("missing delimiter kind")
	}
	kind := payload[off]
	off++
	switch kind {
	case 0:
		present = false
	case 1:
		present = true
		delimiterLen, n, err := readUvarint(payload[off:])
		if err != nil {
			return "", "", false, malformedScopeKey("delimiter length: %v", err)
		}
		off += n
		if delimiterLen == 0 {
			return "", "", false, malformedScopeKey("empty delimiter")
		}
		if delimiterLen > uint64(len(payload)-off) {
			return "", "", false, malformedScopeKey("truncated delimiter")
		}
		delimiterBytes := payload[off : off+int(delimiterLen)]
		off += int(delimiterLen)
		if !utf8.Valid(delimiterBytes) {
			return "", "", false, malformedScopeKey("invalid delimiter utf-8")
		}
		delimiter = string(delimiterBytes)
	default:
		return "", "", false, malformedScopeKey("unknown delimiter kind")
	}

	if off != len(payload) {
		return "", "", false, malformedScopeKey("trailing bytes")
	}
	return identity, delimiter, present, nil
}

func writeUvarint(buf *bytes.Buffer, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	buf.Write(tmp[:n])
}

func readUvarint(data []byte) (uint64, int, error) {
	v, n := binary.Uvarint(data)
	switch {
	case n > 0:
		return v, n, nil
	case n == 0:
		return 0, 0, fmt.Errorf("truncated uvarint")
	default:
		return 0, 0, fmt.Errorf("overlong uvarint")
	}
}

func malformedScopeKey(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrScopeKeyMalformed, fmt.Sprintf(format, args...))
}
