package imap

import (
	"reflect"
	"strings"
	"testing"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/imap/imaptest"
	"github.com/vincentsch/mailmd/internal/mail"
)

func TestFetchParsesMIMEHeadersBodiesAddressesAndAttachments(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{TLS: imaptest.TLSImplicit})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()

	res := fetchTestScope(t, b, mdbackend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX"), Limit: 30})
	bySubject := messagesBySubject(res)

	plain := bySubject["Plain hello"]
	if plain.MessageID != "<plain-1@example.test>" || plain.InReplyTo != "<parent@example.test>" {
		t.Fatalf("plain message-id fields = %q %q", plain.MessageID, plain.InReplyTo)
	}
	if !reflect.DeepEqual(plain.References, []string{"<root@example.test>", "<parent@example.test>"}) {
		t.Fatalf("plain references = %#v", plain.References)
	}
	if plain.BodyText != "Hello from plain text.\r\n" || plain.BodyHTML != "" {
		t.Fatalf("plain bodies = text %q html %q", plain.BodyText, plain.BodyHTML)
	}
	if got := plain.RawHeaders["X-Duplicate"]; !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("duplicate raw headers = %#v", got)
	}
	if got := plain.RawHeaders["Subject"]; !reflect.DeepEqual(got, []string{"Plain hello"}) {
		t.Fatalf("raw subject = %#v", got)
	}

	html := bySubject["HTML only"]
	if html.BodyText != "" || !strings.Contains(html.BodyHTML, "<strong>HTML</strong>") {
		t.Fatalf("html-only bodies = text %q html %q", html.BodyText, html.BodyHTML)
	}
	if !html.Flags.Seen || !html.Flags.Flagged {
		t.Fatalf("html flags = %#v, want seen+flagged", html.Flags)
	}

	alt := bySubject["Alternative ✓"]
	if alt.From.Name != "Zoë Alt" || alt.From.Email != "zoe@example.test" {
		t.Fatalf("decoded from = %#v", alt.From)
	}
	if alt.RawHeaders["Subject"][0] == alt.Subject {
		t.Fatalf("raw encoded subject was decoded in RawHeaders: %#v", alt.RawHeaders["Subject"])
	}
	if alt.BodyText != "Plain alternative." || !strings.Contains(alt.BodyHTML, "Rich alternative") {
		t.Fatalf("alternative bodies = text %q html %q", alt.BodyText, alt.BodyHTML)
	}

	nested := bySubject["Nested mixed"]
	if nested.BodyText != "Nested plain body." || !strings.Contains(nested.BodyHTML, "Nested <em>HTML</em> body") {
		t.Fatalf("nested bodies = text %q html %q", nested.BodyText, nested.BodyHTML)
	}
	requireAttachment(t, nested, 0, "notes.txt", "text/plain", false, "", "notes file\n")

	related := bySubject["Related inline"]
	if !strings.Contains(related.BodyHTML, `cid:image-1@example.test`) {
		t.Fatalf("related html = %q", related.BodyHTML)
	}
	requireAttachment(t, related, 0, "inline.png", "image/png", true, "<image-1@example.test>", string([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}))

	signed := bySubject["Signed content"]
	if signed.BodyText != "Signed body." {
		t.Fatalf("signed body = %q", signed.BodyText)
	}
	requireAttachment(t, signed, 0, "smime.p7s", "application/pkcs7-signature", false, "", "signature")

	latin1 := bySubject["café"]
	if latin1.From.Name != "André" || !strings.Contains(latin1.BodyText, "café body") {
		t.Fatalf("latin1 decoded fields = from %#v body %q", latin1.From, latin1.BodyText)
	}

	address := bySubject["Address edges"]
	if address.From.Email != "first@example.test" {
		t.Fatalf("multi-from first address = %#v", address.From)
	}
	if len(address.To) != 1 || !strings.Contains(address.To[0].Name, "malformed recipient") || address.To[0].Email != "" {
		t.Fatalf("malformed recipient fallback = %#v", address.To)
	}
	if len(address.Cc) != 1 || len(address.Bcc) != 1 || len(address.ReplyTo) != 1 {
		t.Fatalf("recipient groups = cc %#v bcc %#v reply-to %#v", address.Cc, address.Bcc, address.ReplyTo)
	}

	senderOnly := bySubject["Sender only"]
	if senderOnly.From != (mail.Address{}) {
		t.Fatalf("sender-only From = %#v, want empty", senderOnly.From)
	}
	if got := senderOnly.RawHeaders["Sender"]; len(got) != 1 || !strings.Contains(got[0], "Sender Only") {
		t.Fatalf("sender raw header = %#v", got)
	}
	if !senderOnly.Flags.Answered || !senderOnly.Flags.Draft {
		t.Fatalf("sender-only flags = %#v", senderOnly.Flags)
	}

	missingDate := bySubject["Missing date"]
	if !missingDate.Date.IsZero() {
		t.Fatalf("missing-date Date = %v, want zero", missingDate.Date)
	}

	attachmentSet := bySubject["Attachment set"]
	if attachmentSet.BodyText != "Attachment body." {
		t.Fatalf("attachment body = %q", attachmentSet.BodyText)
	}
	if got, want := attachmentNames(attachmentSet), []string{"normal.bin", "café.txt", "résumé.pdf", "fallback.json", "empty.dat"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attachment names = %#v, want %#v", got, want)
	}
	if attachmentSet.Attachments[0].Size != 4 || !reflect.DeepEqual(attachmentSet.Attachments[0].Data, []byte{0, 1, 2, 3}) {
		t.Fatalf("normal attachment = %#v data %v", attachmentSet.Attachments[0], attachmentSet.Attachments[0].Data)
	}
	if attachmentSet.Attachments[3].MIMEType != "application/json" || string(attachmentSet.Attachments[3].Data) != "{}" {
		t.Fatalf("name fallback attachment = %#v data %q", attachmentSet.Attachments[3], attachmentSet.Attachments[3].Data)
	}
	empty := attachmentSet.Attachments[4]
	if empty.Data == nil || len(empty.Data) != 0 || empty.Size != 0 {
		t.Fatalf("zero-byte attachment = %#v data nil=%v len=%d", empty, empty.Data == nil, len(empty.Data))
	}

	refetch := fetchTestScope(t, b, mdbackend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX"), Limit: 30})
	refetchedAttachmentSet := messagesBySubject(refetch)["Attachment set"]
	if attachmentSet.LocalID != refetchedAttachmentSet.LocalID || !reflect.DeepEqual(attachmentNames(attachmentSet), attachmentNames(refetchedAttachmentSet)) {
		t.Fatalf("refetch changed LocalID/order: %q %#v vs %q %#v", attachmentSet.LocalID, attachmentNames(attachmentSet), refetchedAttachmentSet.LocalID, attachmentNames(refetchedAttachmentSet))
	}
}

func TestFetchDiagnosticsAreSanitizedAndAdvance(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{TLS: imaptest.TLSImplicit})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()

	res := fetchTestScope(t, b, mdbackend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX"), Limit: 30})
	if got, want := diagnosticRefs(res.Diagnostics), []string{"101:13", "101:20", "101:21"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics = %#v, want refs %#v", res.Diagnostics, want)
	}
	if got := messageSubjects(res); got[len(got)-1] != "After bad messages" {
		t.Fatalf("fetch did not continue after bad messages, subjects = %#v", got)
	}
	_, last := decodeTestCursor(t, srv.ScopeKey("INBOX"), res.Cursor)
	if last != 22 {
		t.Fatalf("cursor last = %d, want advanced past bad UID 21 to 22", last)
	}
	for _, diag := range res.Diagnostics {
		text := strings.ToLower(diag.Ref + " " + string(diag.Reason) + " " + diag.Backend + " " + diag.ScopeKey)
		for _, forbidden := range []string{"body text that must not leak", "caf", "signature", "mailmd-password"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("diagnostic leaked %q: %#v", forbidden, diag)
			}
		}
	}
}

func TestSeededMessagesExposePinnedExpectedMetadata(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{TLS: imaptest.TLSImplicit})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()

	res := fetchTestScope(t, b, mdbackend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX"), Limit: 30})
	bySubject := messagesBySubject(res)
	for _, expected := range srv.SeededMessages("INBOX") {
		if expected.LocalID == "" {
			continue
		}
		got, ok := bySubject[expected.Subject]
		if !ok {
			t.Fatalf("expected subject %q not fetched", expected.Subject)
		}
		if got.LocalID != expected.LocalID || got.MessageID != expected.MessageID {
			t.Fatalf("expected metadata for %q = local_id %q message_id %q, got local_id %q message_id %q", expected.Subject, expected.LocalID, expected.MessageID, got.LocalID, got.MessageID)
		}
	}
}

func messagesBySubject(res mdbackend.FetchResult) map[string]mail.Message {
	out := make(map[string]mail.Message, len(res.Messages))
	for _, msg := range res.Messages {
		out[msg.Subject] = msg
	}
	return out
}

func requireAttachment(t *testing.T, msg mail.Message, index int, filename, mimeType string, inline bool, contentID, data string) {
	t.Helper()
	if len(msg.Attachments) <= index {
		t.Fatalf("%q attachments length = %d, want index %d", msg.Subject, len(msg.Attachments), index)
	}
	got := msg.Attachments[index]
	if got.Filename != filename || got.MIMEType != mimeType || got.Inline != inline || got.ContentID != contentID {
		t.Fatalf("%q attachment[%d] = %#v, want filename=%q mime=%q inline=%v contentID=%q", msg.Subject, index, got, filename, mimeType, inline, contentID)
	}
	if string(got.Data) != data || got.Size != int64(len(data)) {
		t.Fatalf("%q attachment[%d] data = %q size=%d, want %q size=%d", msg.Subject, index, got.Data, got.Size, data, len(data))
	}
}

func attachmentNames(msg mail.Message) []string {
	out := make([]string, len(msg.Attachments))
	for i := range msg.Attachments {
		out[i] = msg.Attachments[i].Filename
	}
	return out
}
