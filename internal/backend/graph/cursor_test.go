package graph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vincentsch/mailmd/internal/backend"
)

func TestGraphCursorLimits(t *testing.T) {
	b, server := graphTestBackend(t, func(http.ResponseWriter, *http.Request) { t.Error("cursor decode contacted provider") })
	state := cursor{Version: 1, Scope: scopeKey("f"), URL: server.URL + "/v1.0/me/mailFolders/f/messages/delta?done=1", Final: true}
	const frozen = "mailmd-graph-cursor-v1\x00" + `{"version":1,"scope":"graph-v1:AWY","url":"http://localhost/v1.0/me/mailFolders/f/messages/delta?done=1","final":true}`
	golden := state
	golden.URL = "http://localhost/v1.0/me/mailFolders/f/messages/delta?done=1"
	if string(encodeCursor(golden)) != frozen {
		t.Fatal("cursor format changed")
	}
	for i := 0; i < 50; i++ {
		state.Pending = append(state.Pending, change{ID: fmt.Sprintf("d%03d", i), Removed: true})
	}
	encoded, err := persistableCursor(state)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := b.decodeCursor(state.Scope, encoded)
	if err != nil || len(decoded.Pending) != 50 {
		t.Fatalf("round trip: %v", err)
	}
	for _, bad := range [][]byte{encoded[:len(encoded)-1], append(append([]byte(nil), encoded...), 1), bytes.Repeat([]byte{'x'}, backend.MaxCursorBytes+1)} {
		if _, err := b.decodeCursor(state.Scope, bad); !errors.Is(err, backend.ErrInvalidCursor) {
			t.Fatalf("damaged cursor accepted: %v", err)
		}
	}
	state.Pending = nil
	for i := 0; i < 400; i++ {
		state.Pending = append(state.Pending, change{ID: fmt.Sprintf("%08d", i) + strings.Repeat("a", 8184), Removed: true})
	}
	if _, err := persistableCursor(state); !errors.Is(err, backend.ErrProviderStateLimit) {
		t.Fatalf("a cursor larger than the state file allows was accepted: %v", err)
	}
	state.Pending = []change{{ID: strings.Repeat("a", 8193)}}
	if _, err := persistableCursor(state); !errors.Is(err, backend.ErrProviderStateLimit) {
		t.Fatal("overlong reference accepted")
	}
	state.Pending = make([]change, maxCursorPending+1)
	if _, err := persistableCursor(state); !errors.Is(err, backend.ErrProviderStateLimit) {
		t.Fatal("overlong list accepted")
	}
	state.Pending = nil
	state.URL = strings.Repeat("a", maxCursorURLBytes+1)
	if _, err := persistableCursor(state); !errors.Is(err, backend.ErrProviderStateLimit) {
		t.Fatal("overlong link accepted")
	}
	if _, err := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: state.Scope, Cursor: backend.Cursor("not a cursor")}); !errors.Is(err, backend.ErrInvalidCursor) {
		t.Fatal("Fetch accepted a damaged cursor")
	}
}
