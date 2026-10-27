package mail

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const frozenLocalID = "mailmd-localid-v1-01217598f06b5c9f0c40901e2a61e0fd61dc048e887808b46f9c3f71050ee02c"

func frozenMessage() Message {
	return Message{
		MessageID: "<Frozen-ID@example.com>",
		Date:      time.Date(2026, 6, 29, 18, 13, 42, 0, time.UTC),
		From:      Address{Name: "Alice", Email: "Alice@Example.com"},
		To:        []Address{{Email: "bob@example.com"}},
		Subject:   "Hello, World!",
		BodyText:  "Hello\nbody\n",
	}
}

func TestDeriveLocalID_Frozen(t *testing.T) {
	msg := frozenMessage()
	got, err := DeriveLocalID(msg)
	if err != nil {
		t.Fatalf("DeriveLocalID() error = %v", err)
	}
	if got != frozenLocalID {
		t.Fatalf("DeriveLocalID() = %q, want %q", got, frozenLocalID)
	}
	for i := 0; i < 5; i++ {
		next, err := DeriveLocalID(msg)
		if err != nil {
			t.Fatalf("DeriveLocalID repeat error = %v", err)
		}
		if next != got {
			t.Fatalf("DeriveLocalID repeat = %q, want %q", next, got)
		}
	}

	rebuilt := rebuildFrozenSerialization()
	sum := sha256.Sum256(rebuilt)
	if want := localIDPrefix + hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("DeriveLocalID() = %q, independently rebuilt want %q", got, want)
	}
	if len(rebuilt) != 360 {
		t.Fatalf("rebuilt serialization length = %d, want 360", len(rebuilt))
	}

	msg.MessageID = ""
	withoutMessageID, err := DeriveLocalID(msg)
	if err != nil {
		t.Fatalf("DeriveLocalID without MessageID error = %v", err)
	}
	if withoutMessageID == "" || withoutMessageID == got {
		t.Fatalf("DeriveLocalID without MessageID = %q, original %q", withoutMessageID, got)
	}
	if second, _ := DeriveLocalID(msg); second != withoutMessageID {
		t.Fatalf("DeriveLocalID without MessageID nondeterministic: %q vs %q", second, withoutMessageID)
	}
}

func rebuildFrozenSerialization() []byte {
	body := sha256.Sum256([]byte("Hello\nbody"))
	fields := []struct {
		name  string
		value string
	}{
		{"version", "mailmd-localid-v1"},
		{"message_id", "frozen-id@example.com"},
		{"date", "2026-06-29T18:13:42Z"},
		{"from_email", "alice@example.com"},
		{"to_emails", "bob@example.com"},
		{"cc_emails", ""},
		{"bcc_emails", ""},
		{"reply_to_emails", ""},
		{"subject", "Hello, World!"},
		{"in_reply_to", ""},
		{"references", ""},
		{"attachments_signature", ""},
		{"body_sha256", hex.EncodeToString(body[:])},
	}

	var buf bytes.Buffer
	for _, field := range fields {
		buf.WriteString(field.name)
		buf.WriteByte(0)
		buf.WriteString(strconv.Itoa(len(field.value)))
		buf.WriteByte(0)
		buf.WriteString(field.value)
		buf.WriteByte(0)
	}
	return buf.Bytes()
}

func TestDeriveLocalID_RecipientsAndAttachmentsChangeID(t *testing.T) {
	base := frozenMessage()
	baseID := mustLocalID(t, base)

	cases := []struct {
		name   string
		mutate func(Message) Message
	}{
		{name: "to", mutate: func(m Message) Message {
			m.To = []Address{{Email: "carol@example.com"}}
			return m
		}},
		{name: "cc", mutate: func(m Message) Message {
			m.Cc = []Address{{Email: "carol@example.com"}}
			return m
		}},
		{name: "bcc", mutate: func(m Message) Message {
			m.Bcc = []Address{{Email: "carol@example.com"}}
			return m
		}},
		{name: "reply-to", mutate: func(m Message) Message {
			m.ReplyTo = []Address{{Email: "reply@example.com"}}
			return m
		}},
		{name: "attachment filename", mutate: func(m Message) Message {
			m.Attachments = []Attachment{{Filename: "a.pdf"}}
			return m
		}},
		{name: "attachment mime", mutate: func(m Message) Message {
			m.Attachments = []Attachment{{MIMEType: "application/pdf"}}
			return m
		}},
		{name: "attachment size", mutate: func(m Message) Message {
			m.Attachments = []Attachment{{Size: 1}}
			return m
		}},
		{name: "attachment content id", mutate: func(m Message) Message {
			m.Attachments = []Attachment{{ContentID: "cid@example.com"}}
			return m
		}},
		{name: "attachment inline", mutate: func(m Message) Message {
			m.Attachments = []Attachment{{Inline: true}}
			return m
		}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustLocalID(t, tt.mutate(base)); got == baseID {
				t.Fatalf("mutating %s did not change id %q", tt.name, got)
			}
		})
	}

	withDataA := base
	withDataA.Attachments = []Attachment{{Filename: "a.pdf", Data: []byte("a")}}
	withDataB := base
	withDataB.Attachments = []Attachment{{Filename: "a.pdf", Data: []byte("b")}}
	if a, b := mustLocalID(t, withDataA), mustLocalID(t, withDataB); a != b {
		t.Fatalf("changing only attachment Data changed id: %q vs %q", a, b)
	}
}

func TestDeriveLocalID_ExcludedFields(t *testing.T) {
	base := frozenMessage()
	baseID := mustLocalID(t, base)

	changed := base
	changed.LocalID = "caller-supplied"
	changed.ThreadID = "thread"
	changed.Folders = []string{"Inbox", "Archive"}
	changed.Flags = Flags{Seen: true, Flagged: true, Answered: true, Draft: true}
	changed.RawHeaders = map[string][]string{"Message-Id": {"other"}}
	changed.From.Name = "Different Sender"
	changed.To = []Address{{Name: "Different Recipient", Email: "bob@example.com"}}
	if got := mustLocalID(t, changed); got != baseID {
		t.Fatalf("excluded fields changed id: got %q, want %q", got, baseID)
	}
}

func TestDeriveLocalID_HTMLOnlyBodySource(t *testing.T) {
	base := Message{BodyHTML: "<p>Hello</p>\r\n"}
	id := mustLocalID(t, base)

	same := Message{BodyHTML: "<p>Hello</p>"}
	if got := mustLocalID(t, same); got != id {
		t.Fatalf("trailing CRLF changed HTML-only id: got %q, want %q", got, id)
	}

	withText := base
	withText.BodyText = "Hello"
	if got := mustLocalID(t, withText); got == id {
		t.Fatalf("BodyText did not take precedence over BodyHTML")
	}

	changedRawHTML := base
	changedRawHTML.BodyHTML = "<p>Hello</p><script>alert(1)</script>"
	if got := mustLocalID(t, changedRawHTML); got == id {
		t.Fatalf("changing normalized raw HTML did not change id")
	}
}

func TestDeriveLocalID_InsufficientIdentity(t *testing.T) {
	tests := []Message{
		{},
		{MessageID: "<>", Subject: " \t\r\n", BodyText: "\r\n\r"},
		{Attachments: []Attachment{{}}},
		{References: []string{"<>", " \n "}},
	}
	for _, msg := range tests {
		if _, err := DeriveLocalID(msg); !errors.Is(err, ErrInsufficientIdentity) {
			t.Fatalf("DeriveLocalID(%#v) error = %v, want ErrInsufficientIdentity", msg, err)
		}
	}

	if _, err := DeriveLocalID(Message{BodyText: " \t "}); err != nil {
		t.Fatalf("DeriveLocalID spaces-only body error = %v", err)
	}
}

func TestDeriveLocalID_AttachmentNormalization(t *testing.T) {
	base := frozenMessage()
	a := base
	a.Attachments = []Attachment{{Filename: " Report\r\n Final.pdf ", MIMEType: "Application/PDF ", Size: 42, ContentID: "<cid@x>"}}
	b := base
	b.Attachments = []Attachment{{Filename: "Report Final.pdf", MIMEType: "application/pdf", Size: 42, ContentID: "cid@x"}, {}}
	if aid, bid := mustLocalID(t, a), mustLocalID(t, b); aid != bid {
		t.Fatalf("normalized attachment metadata ids differ: %q vs %q", aid, bid)
	}
}

func TestEnsureLocalID(t *testing.T) {
	msg := frozenMessage()
	filled, err := EnsureLocalID(msg)
	if err != nil {
		t.Fatalf("EnsureLocalID() error = %v", err)
	}
	if filled.LocalID != mustLocalID(t, msg) {
		t.Fatalf("EnsureLocalID() LocalID = %q", filled.LocalID)
	}

	msg.LocalID = "bogus"
	preserved, err := EnsureLocalID(msg)
	if err != nil {
		t.Fatalf("EnsureLocalID preset error = %v", err)
	}
	if preserved.LocalID != "bogus" || !reflect.DeepEqual(preserved, msg) {
		t.Fatalf("EnsureLocalID preset = %#v, want unchanged %#v", preserved, msg)
	}

	empty := Message{}
	if returned, err := EnsureLocalID(empty); !errors.Is(err, ErrInsufficientIdentity) || !reflect.DeepEqual(returned, empty) {
		t.Fatalf("EnsureLocalID empty = (%#v, %v), want unchanged ErrInsufficientIdentity", returned, err)
	}

	withSlices := frozenMessage()
	withSlices.To = []Address{{Email: "a@example.com"}}
	copied, err := EnsureLocalID(withSlices)
	if err != nil {
		t.Fatalf("EnsureLocalID with slices error = %v", err)
	}
	copied.To[0].Email = "mutated@example.com"
	if withSlices.To[0].Email != "mutated@example.com" {
		t.Fatalf("EnsureLocalID deep-copied slices; want shallow copy")
	}
}

func TestShortID(t *testing.T) {
	if got := ShortID(frozenLocalID); got != "01217598f06b5c9f" {
		t.Fatalf("ShortID(frozen) = %q", got)
	}
	if got := ShortID(frozenLocalID); len(got) != 16 || !isLowerHex16(got) {
		t.Fatalf("ShortID(frozen) = %q, want 16 lowercase hex", got)
	}

	fallback := ShortID("legacy")
	sum := sha256.Sum256([]byte("legacy"))
	if want := hex.EncodeToString(sum[:])[:16]; fallback != want {
		t.Fatalf("ShortID fallback = %q, want %q", fallback, want)
	}
	for _, s := range []string{
		"mailmd-localid-v1-short",
		"mailmd-localid-v1-0123456789ABCDEf",
		"",
		"garbage",
	} {
		got := ShortID(s)
		if len(got) != 16 || strings.ContainsAny(got, "ghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			t.Fatalf("ShortID(%q) = %q, want lowercase hex", s, got)
		}
	}
}

func mustLocalID(t *testing.T, m Message) string {
	t.Helper()
	id, err := DeriveLocalID(m)
	if err != nil {
		t.Fatalf("DeriveLocalID(%#v) error = %v", m, err)
	}
	return id
}
