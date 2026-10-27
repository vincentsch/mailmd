package graph

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/vincentsch/mailmd/internal/backend"
)

const cursorMagic = "mailmd-graph-cursor-v1\x00"

func validID(id string) bool {
	if len(id) == 0 || len(id) > 8192 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '+' || c == '/' || c == '=') {
			return false
		}
	}
	return true
}

func validChange(item change) bool {
	if !validID(item.ID) || (item.Conversation != "" && !validID(item.Conversation)) {
		return false
	}
	if item.Received != "" {
		if _, err := time.Parse(time.RFC3339, item.Received); err != nil {
			return false
		}
	}
	switch item.Flag.Status {
	case "", "flagged", "notFlagged", "complete":
		return true
	}
	return false
}

func scopeKey(id string) string {
	payload := binary.AppendUvarint(nil, uint64(len(id)))
	payload = append(payload, id...)
	return "graph-v1:" + base64.RawURLEncoding.EncodeToString(payload)
}

func scopeID(key string) (string, error) {
	if !strings.HasPrefix(key, "graph-v1:") {
		return "", backend.ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "graph-v1:"))
	if err != nil {
		return "", backend.ErrInvalidCursor
	}
	length, n := binary.Uvarint(payload)
	if n <= 0 || length != uint64(len(payload)-n) {
		return "", backend.ErrInvalidCursor
	}
	id := string(payload[n:])
	if !validID(id) {
		return "", backend.ErrInvalidCursor
	}
	return id, nil
}

type change struct {
	ID           string `json:"id"`
	Conversation string `json:"conversationId,omitempty"`
	Received     string `json:"receivedDateTime,omitempty"`
	Read         bool   `json:"isRead,omitempty"`
	Draft        bool   `json:"isDraft,omitempty"`
	Flag         struct {
		Status string `json:"flagStatus,omitempty"`
	} `json:"flag,omitempty"`
	Removed bool `json:"removed,omitempty"`
}

// receivedTime is when the server received the message, or zero.
func (c change) receivedTime() time.Time {
	t, _ := time.Parse(time.RFC3339, c.Received)
	return t
}

type cursor struct {
	Version int      `json:"version"`
	Scope   string   `json:"scope"`
	URL     string   `json:"url"`
	Final   bool     `json:"final"`
	Pending []change `json:"pending,omitempty"`
}

func encodeCursor(c cursor) backend.Cursor {
	data, _ := json.Marshal(c)
	return append([]byte(cursorMagic), data...)
}

func (b *Backend) decodeCursor(scope string, data backend.Cursor) (cursor, error) {
	var state cursor
	if len(data) > backend.MaxCursorBytes || !bytes.HasPrefix(data, []byte(cursorMagic)) {
		return state, backend.ErrInvalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(data[len(cursorMagic):]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return cursor{}, backend.ErrInvalidCursor
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || state.Version != 1 || state.Scope != scope || len(state.URL) > maxCursorURLBytes || len(state.Pending) > maxCursorPending || !b.validURL(state.URL) {
		return cursor{}, backend.ErrInvalidCursor
	}
	last := ""
	for _, item := range state.Pending {
		if !validChange(item) || len(item.Received) > maxCursorTimeBytes {
			return cursor{}, backend.ErrInvalidCursor
		}
		if !validID(item.ID) || item.ID < last {
			return cursor{}, backend.ErrInvalidCursor
		}
		last = item.ID
	}
	return state, nil
}

// These limits hold a cursor within the size that the state file allows.
const (
	maxCursorPending   = 100000
	maxCursorURLBytes  = 1 << 20
	maxCursorTimeBytes = 128
)

// persistableCursor encodes a cursor and checks that it fits in the state
// file. A cursor holds at most one delta page, which mailmd asks to be small.
func persistableCursor(state cursor) (backend.Cursor, error) {
	if len(state.Pending) > maxCursorPending || len(state.URL) > maxCursorURLBytes || len(state.Scope) > 16384 {
		return nil, backend.ErrProviderStateLimit
	}
	for _, item := range state.Pending {
		if !validChange(item) || len(item.Received) > maxCursorTimeBytes {
			return nil, backend.ErrProviderStateLimit
		}
	}
	raw := encodeCursor(state)
	if len(raw) > backend.MaxCursorBytes {
		return nil, backend.ErrProviderStateLimit
	}
	return raw, nil
}
