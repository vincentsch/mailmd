package rfc822

import (
	"errors"
	"strings"
	"testing"

	mdmail "github.com/vincentsch/mailmd/internal/mail"
)

func TestMixedMessageKeepsEveryTextPartInOrder(t *testing.T) {
	msg, err := Normalize(wire(`Message-ID: <two-parts@example.test>
From: Ann <ann@example.test>
Subject: Two paragraphs
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/plain

FIRST IMPORTANT PARAGRAPH
--b
Content-Type: text/plain
Content-Disposition: inline

SECOND IMPORTANT PARAGRAPH
--b--
`))
	if err != nil {
		t.Fatal(err)
	}
	first, second := strings.Index(msg.BodyText, "FIRST"), strings.Index(msg.BodyText, "SECOND")
	if first < 0 || second < first || msg.BodyHTML != "" || len(msg.Attachments) != 0 {
		t.Fatalf("text %q html %q attachments %d", msg.BodyText, msg.BodyHTML, len(msg.Attachments))
	}
}

func TestMixedMessageWithAlternativeAndFooterKeepsBothInEachBody(t *testing.T) {
	msg, err := Normalize(wire(`Message-ID: <footer@example.test>
From: List <list@example.test>
Subject: Weekly notes
Content-Type: multipart/mixed; boundary="outer"

--outer
Content-Type: multipart/alternative; boundary="alt"

--alt
Content-Type: text/plain

The notes in text.
--alt
Content-Type: text/html

<p>The notes in <b>HTML</b>.</p>
--alt--
--outer
Content-Type: text/plain

To leave the list, write to <leave@example.test> & say so.
--outer--
`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.BodyText, "notes in text") || !strings.Contains(msg.BodyText, "To leave the list") {
		t.Fatalf("text body = %q", msg.BodyText)
	}
	if !strings.Contains(msg.BodyHTML, "<b>HTML</b>") || !strings.Contains(msg.BodyHTML, "&lt;leave@example.test&gt; &amp; say so") {
		t.Fatalf("html body = %q", msg.BodyHTML)
	}
}

func TestMixedMessageWithHTMLOnlyPartLeavesTextEmpty(t *testing.T) {
	// When one part has no text version, the text body would miss it, so
	// only the HTML body is set, with the text part escaped into it.
	msg, err := Normalize(wire(`Message-ID: <apple@example.test>
From: Ann <ann@example.test>
Subject: Photo
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/plain

Look at this:
--b
Content-Type: text/html

<p>and <i>this</i></p>
--b--
`))
	if err != nil {
		t.Fatal(err)
	}
	if msg.BodyText != "" || !strings.Contains(msg.BodyHTML, "Look at this:") || !strings.Contains(msg.BodyHTML, "<i>this</i>") {
		t.Fatalf("text %q html %q", msg.BodyText, msg.BodyHTML)
	}
}

func TestInReplyToOfOnlyUnicodeSpaceDoesNotPanic(t *testing.T) {
	msg, err := Normalize(wire("Message-ID: <self@example.test>\nFrom: a@example.test\nIn-Reply-To: \u00a0\nSubject: x\n\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	if msg.ThreadID != "<self@example.test>" {
		t.Fatalf("thread = %q", msg.ThreadID)
	}
}

func TestUnreadableMessagesKeepTheirHeadersAndBytes(t *testing.T) {
	cases := map[string]string{
		"bad base64": `Message-ID: <b64@example.test>
From: Ann <ann@example.test>
Subject: Broken attachment
Content-Type: text/plain
Content-Transfer-Encoding: base64

This is %%% not base64 at all !!!
`,
		"unterminated multipart": `Message-ID: <cut@example.test>
From: Ann <ann@example.test>
Subject: Cut off
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/plain

The start of a message that was cut o`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Normalize(wire(raw))
			if !errors.Is(err, ErrParse) {
				t.Fatalf("Normalize() = %v, want a parse failure", err)
			}
			msg := Unreadable(wire(raw), err)
			if msg.From.Email != "ann@example.test" || msg.Subject == "" || msg.MessageID == "" {
				t.Fatalf("headers = %+v", msg)
			}
			want := map[string]string{"bad base64": reasonEncoding, "unterminated multipart": reasonCutOff}[name]
			if msg.ParseError != want || string(msg.Raw) != string(wire(raw)) {
				t.Fatalf("parse error %q raw %d bytes", msg.ParseError, len(msg.Raw))
			}
			if msg.LocalID != mdmail.RawLocalID(wire(raw)) {
				t.Fatalf("local id = %q", msg.LocalID)
			}
		})
	}
}

func TestUnreadableMessagesWithTheSameHeadersGetDifferentIDs(t *testing.T) {
	a := Unreadable([]byte("Subject: same\r\n\r\nfirst"), ErrParse)
	b := Unreadable([]byte("Subject: same\r\n\r\nsecond"), ErrParse)
	if a.LocalID == b.LocalID || a.Subject != "same" {
		t.Fatalf("ids %q %q subject %q", a.LocalID, b.LocalID, a.Subject)
	}
	empty := Unreadable(nil, ErrIdentity)
	if empty.ParseError != reasonEmpty || empty.LocalID == "" {
		t.Fatalf("empty = %+v", empty)
	}
	garbage := Unreadable([]byte{0xff, 0x00, 0x01}, ErrParse)
	if garbage.LocalID == "" || garbage.ParseError == "" {
		t.Fatalf("garbage = %+v", garbage)
	}
}
