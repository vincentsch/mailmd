package imap

import (
	"bytes"
	"fmt"
	"math"

	"github.com/vincentsch/mailmd/internal/backend"
)

const cursorMagic = "mailmd-imap-cursor-v2\x00"

// ErrCursorMalformed means a cursor is damaged or belongs to another folder.
var ErrCursorMalformed = fmt.Errorf("imap: malformed cursor: %w", backend.ErrInvalidCursor)

// cursor is the position in one folder.
type cursor struct {
	validity uint32 // the folder's UIDVALIDITY
	last     uint32 // the last UID mailmd has handled
	// modseq, exists and uidNext describe the folder when its flags were
	// last checked. When they are the same on the next sync, nothing in the
	// folder changed and the flags check can be left out.
	modseq  uint64
	exists  uint32
	uidNext uint32
}

// encodeCursor writes a cursor. The scope key is included, so a cursor
// copied to the wrong mailbox is rejected before any network call.
func encodeCursor(scopeKey string, c cursor) backend.Cursor {
	var buf bytes.Buffer
	buf.WriteString(cursorMagic)
	writeUvarint(&buf, uint64(len(scopeKey)))
	buf.WriteString(scopeKey)
	for _, v := range []uint64{uint64(c.validity), uint64(c.last), c.modseq, uint64(c.exists), uint64(c.uidNext)} {
		writeUvarint(&buf, v)
	}
	return buf.Bytes()
}

// decodeCursor checks the whole cursor before using any part of it. A
// changed UIDVALIDITY is not an error here. Fetch starts the folder over.
func decodeCursor(scopeKey string, raw backend.Cursor) (cursor, error) {
	payload := []byte(raw)
	if !bytes.HasPrefix(payload, []byte(cursorMagic)) {
		return cursor{}, malformedCursor("wrong magic")
	}
	off := len(cursorMagic)
	scopeLen, n, err := readUvarint(payload[off:])
	if err != nil || scopeLen == 0 || scopeLen > uint64(len(payload)-off-n) {
		return cursor{}, malformedCursor("scope key")
	}
	off += n
	if string(payload[off:off+int(scopeLen)]) != scopeKey {
		return cursor{}, malformedCursor("scope mismatch")
	}
	off += int(scopeLen)
	var values [5]uint64
	for i := range values {
		v, n, err := readUvarint(payload[off:])
		if err != nil {
			return cursor{}, malformedCursor("field %d: %v", i, err)
		}
		values[i] = v
		off += n
	}
	if off != len(payload) {
		return cursor{}, malformedCursor("trailing bytes")
	}
	if values[0] == 0 || values[0] > math.MaxUint32 || values[1] > math.MaxUint32 || values[3] > math.MaxUint32 || values[4] > math.MaxUint32 {
		return cursor{}, malformedCursor("value out of range")
	}
	return cursor{validity: uint32(values[0]), last: uint32(values[1]), modseq: values[2], exists: uint32(values[3]), uidNext: uint32(values[4])}, nil
}

func malformedCursor(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCursorMalformed, fmt.Sprintf(format, args...))
}
