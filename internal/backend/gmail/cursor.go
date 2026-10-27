package gmail

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
)

const cursorMagic = "mailmd-gmail-cursor-v1\x00"

// These limits hold a cursor well below the size that the state file allows.
const (
	maxPendingIDs     = 100000
	maxPageTokenBytes = 1 << 20
)

type cursorMode string

const (
	modeReady     cursorMode = "ready"
	modeEnumerate cursorMode = "enumerate"
	modeHistory   cursorMode = "history"
)

// cursorState is the decoded cursor. Each mode has only what it needs to
// continue, so the history ID never moves forward too early.
type cursorState struct {
	Mode        cursorMode
	Fingerprint [32]byte

	HistoryID       string
	TargetHistoryID string
	BaseHistoryID   string
	PageToken       string
	PendingIDs      []string
	// PendingDeletedIDs are deletions from history that were not reported yet.
	PendingDeletedIDs []string
}

// encodeReadyCursor makes the cursor for an account that is fully synced up
// to historyID.
func encodeReadyCursor(fp [32]byte, historyID string) mdbackend.Cursor {
	return encodeCursor(cursorState{Mode: modeReady, Fingerprint: fp, HistoryID: historyID})
}

// encodeCursor writes each string and list with its length first, so no
// value can change how the rest is read.
func encodeCursor(state cursorState) mdbackend.Cursor {
	var buf bytes.Buffer
	buf.WriteString(cursorMagic)
	writeString(&buf, accountScopeKey)
	writeBytes(&buf, state.Fingerprint[:])
	writeString(&buf, string(state.Mode))
	switch state.Mode {
	case modeReady:
		writeString(&buf, state.HistoryID)
	case modeEnumerate:
		writeString(&buf, state.TargetHistoryID)
		writeString(&buf, state.PageToken)
		writeStringList(&buf, state.PendingIDs)
	case modeHistory:
		writeString(&buf, state.BaseHistoryID)
		writeString(&buf, state.TargetHistoryID)
		writeString(&buf, state.PageToken)
		writeStringList(&buf, state.PendingIDs)
		writeStringList(&buf, state.PendingDeletedIDs)
	}
	return buf.Bytes()
}

// persistableCursor encodes a cursor and checks that it fits in the state
// file.
func persistableCursor(state cursorState) (mdbackend.Cursor, error) {
	if len(state.PageToken) > maxPageTokenBytes || len(state.PendingIDs) > maxPendingIDs || len(state.PendingDeletedIDs) > maxPendingIDs {
		return nil, mdbackend.ErrProviderStateLimit
	}
	for _, ids := range [][]string{state.PendingIDs, state.PendingDeletedIDs} {
		for _, id := range ids {
			if !validPendingID(id) {
				return nil, mdbackend.ErrProviderStateLimit
			}
		}
	}
	raw := encodeCursor(state)
	if len(raw) > mdbackend.MaxCursorBytes {
		return nil, mdbackend.ErrProviderStateLimit
	}
	return raw, nil
}

// decodeCursor checks each field before Fetch makes a Gmail API call. The
// account fingerprint is compared later, once the profile is known.
func decodeCursor(raw mdbackend.Cursor) (*cursorState, error) {
	if len(raw) > mdbackend.MaxCursorBytes {
		return nil, invalidCursor("too large")
	}
	dec := cursorDecoder{data: raw}
	if !dec.consumeMagic(cursorMagic) {
		return nil, invalidCursor("wrong magic")
	}
	scope, err := dec.readString()
	if err != nil {
		return nil, err
	}
	if scope != accountScopeKey {
		return nil, invalidCursor("wrong scope")
	}
	fpBytes, err := dec.readBytes()
	if err != nil {
		return nil, err
	}
	if len(fpBytes) != 32 {
		return nil, invalidCursor("invalid fingerprint")
	}
	var fp [32]byte
	copy(fp[:], fpBytes)
	modeRaw, err := dec.readString()
	if err != nil {
		return nil, err
	}
	state := &cursorState{Mode: cursorMode(modeRaw), Fingerprint: fp}
	switch state.Mode {
	case modeReady:
		state.HistoryID, err = dec.readHistoryID()
	case modeEnumerate:
		state.TargetHistoryID, err = dec.readHistoryID()
		if err == nil {
			state.PageToken, err = dec.readString()
		}
		if err == nil {
			state.PendingIDs, err = dec.readPendingIDs()
		}
	case modeHistory:
		state.BaseHistoryID, err = dec.readHistoryID()
		if err == nil {
			state.TargetHistoryID, err = dec.readHistoryID()
		}
		if err == nil {
			state.PageToken, err = dec.readString()
		}
		if err == nil {
			state.PendingIDs, err = dec.readPendingIDs()
		}
		if err == nil {
			state.PendingDeletedIDs, err = dec.readPendingIDs()
		}
	default:
		return nil, invalidCursor("unknown mode")
	}
	if err != nil {
		return nil, err
	}
	if len(state.PageToken) > maxPageTokenBytes {
		return nil, invalidCursor("page token too long")
	}
	if dec.remaining() != 0 {
		return nil, invalidCursor("trailing bytes")
	}
	return state, nil
}

// validateFingerprint rejects a cursor that belongs to another Gmail account,
// before mailmd uses its history ID.
func (s *cursorState) validateFingerprint(fp [32]byte) error {
	if s == nil {
		return nil
	}
	if s.Fingerprint != fp {
		return invalidCursor("account fingerprint mismatch")
	}
	return nil
}

// cursorDecoder reads the cursor format. Each value has its length first, and
// the decoder checks each length against the bytes that are left.
type cursorDecoder struct {
	data []byte
	pos  int
}

func (d *cursorDecoder) consumeMagic(magic string) bool {
	if len(d.data)-d.pos < len(magic) || string(d.data[d.pos:d.pos+len(magic)]) != magic {
		return false
	}
	d.pos += len(magic)
	return true
}

func (d *cursorDecoder) readHistoryID() (string, error) {
	s, err := d.readString()
	if err != nil {
		return "", err
	}
	if !validHistoryID(s) {
		return "", invalidCursor("invalid history id")
	}
	return s, nil
}

func (d *cursorDecoder) readPendingIDs() ([]string, error) {
	count, err := d.readUvarint()
	if err != nil {
		return nil, err
	}
	// Each ID takes at least one byte, so a count above the bytes that are
	// left can't be right.
	if count > uint64(len(d.data)-d.pos) || count > maxPendingIDs {
		return nil, invalidCursor("invalid pending id list")
	}
	out := make([]string, 0, count)
	for i := uint64(0); i < count; i++ {
		id, err := d.readString()
		if err != nil {
			return nil, err
		}
		if !validPendingID(id) {
			return nil, invalidCursor("invalid pending id")
		}
		out = append(out, id)
	}
	return out, nil
}

func (d *cursorDecoder) readString() (string, error) {
	b, err := d.readBytes()
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", invalidCursor("invalid utf-8")
	}
	return string(b), nil
}

func (d *cursorDecoder) readBytes() ([]byte, error) {
	n, err := d.readUvarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(d.data)-d.pos) {
		return nil, invalidCursor("invalid length")
	}
	start := d.pos
	d.pos += int(n)
	return d.data[start:d.pos], nil
}

func (d *cursorDecoder) readUvarint() (uint64, error) {
	v, n := binary.Uvarint(d.data[d.pos:])
	if n <= 0 {
		return 0, invalidCursor("invalid length")
	}
	d.pos += n
	return v, nil
}

func (d *cursorDecoder) remaining() int {
	return len(d.data) - d.pos
}

func writeString(buf *bytes.Buffer, s string) {
	writeBytes(buf, []byte(s))
}

func writeBytes(buf *bytes.Buffer, b []byte) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(len(b)))
	buf.Write(tmp[:n])
	buf.Write(b)
}

func writeStringList(buf *bytes.Buffer, values []string) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(len(values)))
	buf.Write(tmp[:n])
	for _, value := range values {
		writeString(buf, value)
	}
}

// validHistoryID accepts only decimal history IDs that fit into the uint64
// that Google's library wants for StartHistoryId.
func validHistoryID(s string) bool {
	if s == "" {
		return false
	}
	if _, err := strconv.ParseUint(s, 10, 64); err != nil {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// validPendingID accepts the characters Gmail uses in message IDs.
func validPendingID(id string) bool {
	if len(id) == 0 || len(id) > 8192 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.+=/-", c)) {
			return false
		}
	}
	return true
}

func invalidCursor(reason string) error {
	if reason == "" {
		return mdbackend.ErrInvalidCursor
	}
	return fmt.Errorf("gmail: invalid cursor: %s: %w", reason, mdbackend.ErrInvalidCursor)
}
