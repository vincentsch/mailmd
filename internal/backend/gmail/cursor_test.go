package gmail

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
)

func TestCursorFormat(t *testing.T) {
	var fp [32]byte
	for i := range fp {
		fp[i] = byte(i)
	}
	want := "6d61696c6d642d676d61696c2d637572736f722d76310010676d61696c2d76313a6163636f756e7420000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f05726561647903313233"
	if got := hex.EncodeToString(encodeReadyCursor(fp, "123")); got != want {
		t.Fatalf("ready cursor = %s, want %s", got, want)
	}
	for _, state := range []cursorState{
		{Mode: modeReady, Fingerprint: fp, HistoryID: "123"},
		{Mode: modeEnumerate, Fingerprint: fp, TargetHistoryID: "200", PageToken: "pt", PendingIDs: []string{"m1", "m2"}},
		{Mode: modeHistory, Fingerprint: fp, BaseHistoryID: "100", TargetHistoryID: "200", PageToken: "hp", PendingIDs: []string{"m3"}, PendingDeletedIDs: []string{"d1"}},
	} {
		got, err := decodeCursor(encodeCursor(state))
		if err != nil || !reflect.DeepEqual(*got, state) {
			t.Fatalf("round trip of %+v = %+v, %v", state, got, err)
		}
	}
	if _, err := persistableCursor(cursorState{Mode: modeEnumerate, Fingerprint: fp, TargetHistoryID: "1", PendingIDs: make([]string, maxPendingIDs+1)}); !errors.Is(err, mdbackend.ErrProviderStateLimit) {
		t.Fatalf("too many pending ids: %v", err)
	}
}

func TestCursorRejectsMalformedInputBeforeProviderCalls(t *testing.T) {
	var fp [32]byte
	for i := range fp {
		fp[i] = byte(i)
	}
	tests := []struct {
		name   string
		cursor mdbackend.Cursor
	}{
		{name: "wrong magic", cursor: []byte("nope")},
		{name: "empty scope", cursor: rawCursor("", fp[:], "ready", "123")},
		{name: "wrong scope", cursor: rawCursor("other", fp[:], "ready", "123")},
		{name: "empty fingerprint", cursor: rawCursor(accountScopeKey, nil, "ready", "123")},
		{name: "bad fingerprint length", cursor: rawCursor(accountScopeKey, []byte{1}, "ready", "123")},
		{name: "unknown mode", cursor: rawCursor(accountScopeKey, fp[:], "unknown", "123")},
		{name: "invalid utf8", cursor: rawCursorWithModeBytes(accountScopeKey, fp[:], []byte{0xff}, "123")},
		{name: "trailing", cursor: append(encodeReadyCursor(fp, "123"), 'x')},
		{name: "empty history", cursor: rawCursor(accountScopeKey, fp[:], "ready", "")},
		{name: "nondecimal history", cursor: rawCursor(accountScopeKey, fp[:], "ready", "abc")},
		{name: "empty pending id", cursor: rawCursor(accountScopeKey, fp[:], "enumerate", "200", "", []string{""})},
		{name: "malformed pending list", cursor: malformedPendingCursor(fp)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeClient()
			b := openFakeBackend(t, fake)
			_, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: tt.cursor})
			if !errors.Is(err, mdbackend.ErrInvalidCursor) {
				t.Fatalf("Fetch cursor error = %v, want ErrInvalidCursor", err)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("malformed cursor made provider calls: %#v", fake.calls)
			}
		})
	}
}

func TestWrongAccountCursorOnlyFetchesProfile(t *testing.T) {
	var fp [32]byte
	cursor := encodeReadyCursor(fp, "10")
	fake := newFakeClient()
	b := openFakeBackend(t, fake)
	_, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: cursor})
	if !errors.Is(err, mdbackend.ErrInvalidCursor) {
		t.Fatalf("Fetch wrong account cursor error = %v", err)
	}
	if got, want := strings.Join(fake.calls, ","), "profile"; got != want {
		t.Fatalf("provider calls = %s, want %s", got, want)
	}
}

func rawCursor(scope string, fp []byte, mode string, fields ...any) mdbackend.Cursor {
	return rawCursorWithModeBytes(scope, fp, []byte(mode), fields...)
}

func rawCursorWithModeBytes(scope string, fp []byte, mode []byte, fields ...any) mdbackend.Cursor {
	var buf bytes.Buffer
	buf.WriteString(cursorMagic)
	writeString(&buf, scope)
	writeBytes(&buf, fp)
	writeBytes(&buf, mode)
	for _, field := range fields {
		switch v := field.(type) {
		case string:
			writeString(&buf, v)
		case []string:
			writeStringList(&buf, v)
		}
	}
	return buf.Bytes()
}

func malformedPendingCursor(fp [32]byte) mdbackend.Cursor {
	var buf bytes.Buffer
	buf.WriteString(cursorMagic)
	writeString(&buf, accountScopeKey)
	writeBytes(&buf, fp[:])
	writeString(&buf, string(modeEnumerate))
	writeString(&buf, "200")
	writeString(&buf, "")
	buf.WriteByte(2)
	writeString(&buf, "only-one")
	return buf.Bytes()
}
