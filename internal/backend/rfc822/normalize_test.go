package rfc822

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	mdmail "github.com/vincentsch/mailmd/internal/mail"
)

func TestNormalizeHeadersBodiesAddressesAndAttachments(t *testing.T) {
	msg, err := Normalize(wire(`Message-ID: <mixed@example.test>
In-Reply-To: <parent@example.test>
References: <root@example.test> <parent@example.test>
Date: Fri, 03 Jul 2026 08:05:00 +0000
From: =?UTF-8?Q?Zo=C3=AB_Sender?= <zoe@example.test>
To: Bob <bob@example.test>
Cc: Carol <carol@example.test>
Bcc: Blind <blind@example.test>
Reply-To: Replies <reply@example.test>
Subject: =?UTF-8?Q?Nested_=E2=9C=93?=
X-Duplicate: first
X-Duplicate: second
Content-Type: multipart/mixed; boundary="mixed-boundary"

--mixed-boundary
Content-Type: multipart/alternative; boundary="alt-boundary"

--alt-boundary
Content-Type: text/plain; charset=utf-8

Plain body.
--alt-boundary
Content-Type: text/html; charset=utf-8

<p>HTML <strong>body</strong>.</p>
--alt-boundary--
--mixed-boundary
Content-Type: text/plain; charset=utf-8
Content-Disposition: attachment; filename="notes.txt"

notes file
--mixed-boundary
Content-Type: image/png
Content-Disposition: inline; filename="inline.png"
Content-ID: <image-1@example.test>
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--mixed-boundary
Content-Type: application/octet-stream
Content-Disposition: attachment; filename="empty.dat"
Content-Transfer-Encoding: base64


--mixed-boundary--
`))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if msg.LocalID == "" {
		t.Fatal("LocalID is empty")
	}
	if msg.MessageID != "<mixed@example.test>" || msg.InReplyTo != "<parent@example.test>" {
		t.Fatalf("message-id fields = %q %q", msg.MessageID, msg.InReplyTo)
	}
	if !reflect.DeepEqual(msg.References, []string{"<root@example.test>", "<parent@example.test>"}) {
		t.Fatalf("references = %#v", msg.References)
	}
	if msg.From.Name != "Zoë Sender" || msg.From.Email != "zoe@example.test" {
		t.Fatalf("decoded from = %#v", msg.From)
	}
	if len(msg.To) != 1 || len(msg.Cc) != 1 || len(msg.Bcc) != 1 || len(msg.ReplyTo) != 1 {
		t.Fatalf("address lists = to %#v cc %#v bcc %#v reply %#v", msg.To, msg.Cc, msg.Bcc, msg.ReplyTo)
	}
	if msg.Subject != "Nested ✓" {
		t.Fatalf("subject = %q", msg.Subject)
	}
	if got := msg.RawHeaders["Subject"]; len(got) != 1 || got[0] == msg.Subject {
		t.Fatalf("raw subject decoded or missing: %#v", got)
	}
	if got := msg.RawHeaders["X-Duplicate"]; !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("duplicate raw headers = %#v", got)
	}
	if msg.BodyText != "Plain body." || !strings.Contains(msg.BodyHTML, "<strong>body</strong>") {
		t.Fatalf("bodies = text %q html %q", msg.BodyText, msg.BodyHTML)
	}
	if got, want := attachmentNames(msg.Attachments), []string{"notes.txt", "inline.png", "empty.dat"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attachment names = %#v, want %#v", got, want)
	}
	requireAttachment(t, msg.Attachments[0], "notes.txt", "text/plain", false, "", "notes file")
	requireAttachment(t, msg.Attachments[1], "inline.png", "image/png", true, "<image-1@example.test>", string([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}))
	empty := msg.Attachments[2]
	if empty.Data == nil || len(empty.Data) != 0 || empty.Size != 0 {
		t.Fatalf("zero-byte attachment = %#v data nil=%v len=%d", empty, empty.Data == nil, len(empty.Data))
	}
	if len(msg.Folders) != 0 || msg.Flags != (mdmail.Flags{}) {
		t.Fatalf("provider-owned fields were set: folders=%#v flags=%#v", msg.Folders, msg.Flags)
	}
	if msg.ThreadID != "<root@example.test>" {
		t.Fatalf("thread = %q, want the first References entry", msg.ThreadID)
	}
}

func TestNormalizeHTMLOnlyAndMalformedAddressFallback(t *testing.T) {
	msg, err := Normalize(wire(`Message-ID: <html@example.test>
Date: Fri, 03 Jul 2026 08:02:00 +0000
From: Web Sender <web@example.test>
To: "malformed recipient
Subject: HTML only
Content-Type: text/html; charset=utf-8

<p>Hello <strong>HTML</strong>.</p>
`))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if msg.BodyText != "" || !strings.Contains(msg.BodyHTML, "<strong>HTML</strong>") {
		t.Fatalf("html-only bodies = text %q html %q", msg.BodyText, msg.BodyHTML)
	}
	if len(msg.To) != 1 || !strings.Contains(msg.To[0].Name, "malformed recipient") || msg.To[0].Email != "" {
		t.Fatalf("malformed address fallback = %#v", msg.To)
	}
}

func TestNormalizeMissingMessageIDWithEnoughIdentity(t *testing.T) {
	msg, err := Normalize(wire(`Date: Fri, 03 Jul 2026 08:03:00 +0000
From: Sender <sender@example.test>
To: Recipient <recipient@example.test>
Subject: Missing Message-ID
Content-Type: text/plain; charset=utf-8

Body can still identify this message.
`))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if msg.MessageID != "" {
		t.Fatalf("MessageID = %q, want empty", msg.MessageID)
	}
	if msg.LocalID == "" {
		t.Fatal("LocalID is empty")
	}
	if msg.BodyText != "Body can still identify this message.\r\n" {
		t.Fatalf("BodyText = %q", msg.BodyText)
	}
}

func TestNormalizeErrorsAreCategorizedAndSanitized(t *testing.T) {
	if _, err := Normalize([]byte{0xff, 0x00, 0x01}); !errors.Is(err, ErrParse) {
		t.Fatalf("malformed Normalize error = %v, want ErrParse", err)
	}
	if _, err := Normalize(wire(`Content-Type: text/plain; charset=utf-8

`)); !errors.Is(err, ErrIdentity) {
		t.Fatalf("identityless Normalize error = %v, want ErrIdentity", err)
	}
	for _, err := range []error{ErrParse, ErrIdentity} {
		for _, forbidden := range []string{"secret body", "attachment bytes", "Authorization"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Fatalf("error leaked %q: %v", forbidden, err)
			}
		}
	}
}

func requireAttachment(t *testing.T, got mdmail.Attachment, filename, mimeType string, inline bool, contentID, data string) {
	t.Helper()
	if got.Filename != filename || got.MIMEType != mimeType || got.Inline != inline || got.ContentID != contentID {
		t.Fatalf("attachment = %#v, want filename=%q mime=%q inline=%v contentID=%q", got, filename, mimeType, inline, contentID)
	}
	if string(got.Data) != data || got.Size != int64(len(data)) {
		t.Fatalf("attachment data = %q size=%d, want %q size=%d", got.Data, got.Size, data, len(data))
	}
}

func attachmentNames(atts []mdmail.Attachment) []string {
	out := make([]string, len(atts))
	for i := range atts {
		out[i] = atts[i].Filename
	}
	return out
}

func wire(raw string) []byte {
	raw = strings.TrimPrefix(raw, "\n")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\n", "\r\n")
	return []byte(raw)
}

func TestUndeclaredEightBitTextIsReadAsWindows1252(t *testing.T) {
	raw := []byte("Message-ID: <latin@example.test>\r\nFrom: J\xf6rg <j@example.test>\r\nSubject: Gr\xfc\xdfe\r\n\r\nCaf\xe9 cr\xe8me\r\n")
	msg, err := Normalize(raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "Grüße" || msg.From.Name != "Jörg" || !strings.Contains(msg.BodyText, "Café crème") {
		t.Fatalf("subject %q from %q body %q", msg.Subject, msg.From.Name, msg.BodyText)
	}
}

func TestForwardedMessageTextIsKept(t *testing.T) {
	raw := wire(`Message-ID: <fwd@example.test>
From: Bob <bob@example.test>
Subject: Fwd: Outage report
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/plain

See below.
--b
Content-Type: message/rfc822

Message-ID: <inner@example.test>
From: Ops <ops@example.test>
Subject: Outage report
Date: Mon, 19 Oct 2026 10:00:00 +0000

The server was down for 20 minutes.
--b--
`)
	msg, err := Normalize(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Forwarded) != 1 || msg.Forwarded[0].Subject != "Outage report" || !strings.Contains(msg.Forwarded[0].BodyText, "server was down") {
		t.Fatalf("forwarded = %#v", msg.Forwarded)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].MIMEType != "message/rfc822" {
		t.Fatalf("attachments = %#v", msg.Attachments)
	}
}

func TestEncodedWordsAreDecodedWhereMailersPutThem(t *testing.T) {
	// "田中太郎 <tanaka@example.jp>" and two addresses, each header as one
	// encoded word.
	from := "=?utf-8?b?55Sw5Lit5aSq6YOOIDx0YW5ha2FAZXhhbXBsZS5qcD4=?="
	to := "=?utf-8?b?QW5uYSA8YUB4LmRlPiwgQm9iIDxiQHguZGU+?="
	msg, err := Normalize(wire(`Message-ID: <encoded@example.test>
From: ` + from + `
To: ` + to + `
Cc: "=?utf-8?q?M=C3=BCller,_Hans?=" <hans@example.de>, =?utf-8?q?Zo?==?utf-8?q?=C3=AB?= <zoe@example.de>
Reply-To: =?x-unknown?q?Support?= <help@example.de>
Subject: =?utf-8*de?q?Gr=C3=BC=C3=9Fe?= =?x-unknown?q?_aus_Berlin?=
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/plain

Hallo.
--b
Content-Type: application/pdf
Content-Disposition: attachment; filename="=?x-unknown?q?Rechnung.pdf?="

pdf
--b
Content-Type: application/pdf
Content-Disposition: attachment; filename=Rechnung M` + "\xe4" + `rz.pdf

pdf
--b--
`))
	if err != nil {
		t.Fatal(err)
	}
	if msg.From != (mdmail.Address{Name: "田中太郎", Email: "tanaka@example.jp"}) {
		t.Errorf("from = %#v", msg.From)
	}
	want := []mdmail.Address{{Name: "Anna", Email: "a@x.de"}, {Name: "Bob", Email: "b@x.de"}}
	if !reflect.DeepEqual(msg.To, want) {
		t.Errorf("to = %#v", msg.To)
	}
	want = []mdmail.Address{{Name: "Müller, Hans", Email: "hans@example.de"}, {Name: "Zoë", Email: "zoe@example.de"}}
	if !reflect.DeepEqual(msg.Cc, want) {
		t.Errorf("cc = %#v", msg.Cc)
	}
	if len(msg.ReplyTo) != 1 || msg.ReplyTo[0] != (mdmail.Address{Name: "Support", Email: "help@example.de"}) {
		t.Errorf("reply-to = %#v", msg.ReplyTo)
	}
	if msg.Subject != "Grüße aus Berlin" {
		t.Errorf("subject = %q", msg.Subject)
	}
	if len(msg.Attachments) != 2 || msg.Attachments[0].Filename != "Rechnung.pdf" || msg.Attachments[1].Filename != "Rechnung März.pdf" {
		t.Errorf("attachments = %#v", msg.Attachments)
	}
}
