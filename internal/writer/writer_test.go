package writer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/mail"
)

const (
	plainLocalID = "mailmd-localid-v1-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	htmlLocalID  = "mailmd-localid-v1-fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	otherLocalID = "mailmd-localid-v1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestWriteMessagePlainGoldenAndIdempotent(t *testing.T) {
	root := t.TempDir()
	loc := time.FixedZone("CEST", 2*60*60)
	msg := plainMessage(loc)

	planned, err := PlanMessage(context.Background(), Options{Root: root, Account: "Personal Mail"}, msg)
	if err != nil {
		t.Fatalf("PlanMessage: %v", err)
	}
	if !planned.Changed {
		t.Fatal("PlanMessage Changed = false, want true")
	}
	if got, want := len(planned.Files), 2; got != want {
		t.Fatalf("planned files = %d, want %d", got, want)
	}
	for _, file := range planned.Files {
		if _, err := os.Stat(file.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("PlanMessage mutated %s: %v", file.Path, err)
		}
	}

	result, err := WriteMessage(context.Background(), Options{Root: root, Account: "Personal Mail"}, msg)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if !reflect.DeepEqual(planned, result) {
		t.Fatalf("write result differs from plan\nplan: %#v\nwrite:%#v", planned, result)
	}

	checkGolden(t, "plain.golden.md", readFile(t, result.Files[0].Path))
	wantBytes := readTestdata(t, "plain.golden.md")
	for _, file := range result.Files {
		got := readFile(t, file.Path)
		if !bytes.Equal(got, wantBytes) {
			t.Fatalf("markdown bytes for %s differ from the other copies", file.RelPath)
		}
		if file.Status != WriteCreated {
			t.Fatalf("file status = %s, want created", file.Status)
		}
		if got, want := attachmentStatuses(file.Attachments), []WriteStatus{WriteCreated, WriteSkipped, WriteCreated}; !reflect.DeepEqual(got, want) {
			t.Fatalf("attachment statuses = %#v, want %#v", got, want)
		}
		if got := readFile(t, file.Attachments[0].Path); string(got) != "PDF" {
			t.Fatalf("first attachment bytes = %q", got)
		}
		if got := readFile(t, file.Attachments[2].Path); len(got) != 0 {
			t.Fatalf("zero-byte attachment len = %d, want 0", len(got))
		}
	}

	mtimes := map[string]time.Time{}
	for _, file := range result.Files {
		mtimes[file.Path] = statFile(t, file.Path).ModTime()
		for _, att := range file.Attachments {
			if att.Path != "" {
				mtimes[att.Path] = statFile(t, att.Path).ModTime()
			}
		}
	}

	again, err := WriteMessage(context.Background(), Options{Root: root, Account: "Personal Mail"}, msg)
	if err != nil {
		t.Fatalf("second WriteMessage: %v", err)
	}
	if again.Changed {
		t.Fatal("second WriteMessage Changed = true, want false")
	}
	for _, file := range again.Files {
		if file.Status != WriteUnchanged {
			t.Fatalf("second file status = %s, want unchanged", file.Status)
		}
		if got, want := attachmentStatuses(file.Attachments), []WriteStatus{WriteUnchanged, WriteSkipped, WriteUnchanged}; !reflect.DeepEqual(got, want) {
			t.Fatalf("second attachment statuses = %#v, want %#v", got, want)
		}
		if !statFile(t, file.Path).ModTime().Equal(mtimes[file.Path]) {
			t.Fatalf("unchanged markdown was rewritten: %s", file.Path)
		}
		for _, att := range file.Attachments {
			if att.Path != "" && !statFile(t, att.Path).ModTime().Equal(mtimes[att.Path]) {
				t.Fatalf("unchanged attachment was rewritten: %s", att.Path)
			}
		}
	}
}

func TestWriteMessageHTMLGolden(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{
		LocalID:   htmlLocalID,
		MessageID: "<html@example.com>",
		From:      mail.Address{Name: "HTML Sender", Email: "html@example.com"},
		Subject:   "HTML Only",
		BodyHTML:  `<h1>Title</h1><p>Hello <a href="https://example.com/a?x=1">safe</a> <a href="javascript:alert(1)">bad</a><img src="cid:x" alt=" Logo&#10; Mark "><img src="https://tracker.example/p.gif" width="1" height="1"></p><script>alert(1)</script><style>p{color:red}</style><blockquote>Quote</blockquote><ul><li>One</li><li>Two</li></ul>`,
	}
	result, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if got, want := result.Files[0].RelPath, "Acct/Unfiled/no-date/no-date-html-only--fedcba9876543210.md"; got != want {
		t.Fatalf("RelPath = %q, want %q", got, want)
	}
	got := readFile(t, result.Files[0].Path)
	checkGolden(t, "html.golden.md", got)
}

func TestAttachmentDataChangeLeavesMarkdownUnchanged(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{
		LocalID:     plainLocalID,
		Subject:     "Payload",
		BodyText:    "body",
		Attachments: []mail.Attachment{{Filename: "same.bin", MIMEType: "application/octet-stream", Size: 3, Data: []byte("one")}},
	}
	first, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("initial WriteMessage: %v", err)
	}
	msg.Attachments[0].Data = []byte("two")
	second, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("second WriteMessage: %v", err)
	}
	if !second.Changed {
		t.Fatal("Changed = false, want true")
	}
	if got := second.Files[0].Status; got != WriteUnchanged {
		t.Fatalf("markdown status = %s, want unchanged", got)
	}
	if got := second.Files[0].Attachments[0].Status; got != WriteUpdated {
		t.Fatalf("attachment status = %s, want updated", got)
	}
	if !bytes.Equal(readFile(t, first.Files[0].Path), readFile(t, second.Files[0].Path)) {
		t.Fatal("markdown changed when only attachment payload changed")
	}
	if got := string(readFile(t, second.Files[0].Attachments[0].Path)); got != "two" {
		t.Fatalf("attachment bytes = %q, want two", got)
	}
}

func TestAttachmentSidecarNamesAndGaps(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{
		LocalID:  plainLocalID,
		Subject:  "Attachments",
		BodyText: "body",
		Attachments: []mail.Attachment{
			{Filename: "///", Data: []byte("a")},
			{Filename: "dup.txt", Data: []byte("b")},
			{Filename: "metadata-only"},
			{Filename: "dup.txt", Data: []byte("c")},
		},
	}
	result, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	got := []string{
		result.Files[0].Attachments[0].SavedAs,
		result.Files[0].Attachments[1].SavedAs,
		result.Files[0].Attachments[2].SavedAs,
		result.Files[0].Attachments[3].SavedAs,
	}
	want := []string{
		"no-date-attachments--0123456789abcdef.attachments/01-attachment",
		"no-date-attachments--0123456789abcdef.attachments/02-dup.txt",
		"",
		"no-date-attachments--0123456789abcdef.attachments/04-dup.txt",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SavedAs = %#v, want %#v", got, want)
	}
	if got := attachmentStatuses(result.Files[0].Attachments); !reflect.DeepEqual(got, []WriteStatus{WriteCreated, WriteCreated, WriteSkipped, WriteCreated}) {
		t.Fatalf("attachment statuses = %#v", got)
	}
	for i, wantBytes := range []string{"a", "b", "", "c"} {
		if i == 2 {
			continue
		}
		if got := string(readFile(t, result.Files[0].Attachments[i].Path)); got != wantBytes {
			t.Fatalf("attachment %d bytes = %q, want %q", i, got, wantBytes)
		}
	}
}

func TestDateKeepsTheSendersOffset(t *testing.T) {
	root := t.TempDir()
	sourceLoc := time.FixedZone("source", -7*60*60)
	msg := mail.Message{
		LocalID: plainLocalID,
		Subject: "Empty",
		Date:    time.Date(2026, 6, 30, 10, 0, 0, 500, sourceLoc),
	}
	result, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if got, want := result.Files[0].RelPath, "Acct/Unfiled/2026/2026-06-30-empty--0123456789abcdef.md"; got != want {
		t.Fatalf("RelPath = %q, want %q", got, want)
	}
	got := readFile(t, result.Files[0].Path)
	if !bytes.Contains(got, []byte("date: \"2026-06-30T10:00:00-07:00\"\n")) {
		t.Fatalf("date did not keep the sender's offset:\n%s", got)
	}
	if !bytes.HasSuffix(got, []byte("---\n\n# Empty\n\n")) {
		t.Fatalf("an empty body should leave only the title:\n%s", got)
	}
}

func TestYAMLHostileGolden(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{
		LocalID:  otherLocalID,
		From:     mail.Address{Name: "> Boss\x02\nLine", Email: "boss@example.com"},
		Subject:  "> \"YAML\":\nControl \x01",
		BodyText: "ok",
	}
	result, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	got := readFile(t, result.Files[0].Path)
	checkGolden(t, "hostile.golden.md", got)
}

func TestInvalidOptionsAndRelativeRoot(t *testing.T) {
	msg := mail.Message{LocalID: plainLocalID, Subject: "s", BodyText: "body"}
	for _, opts := range []Options{
		{Root: "", Account: "acct"},
		{Root: " \t\n", Account: "acct"},
		{Root: "out", Account: ""},
		{Root: "out", Account: " \r\n"},
	} {
		_, err := PlanMessage(context.Background(), opts, msg)
		if !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("PlanMessage(%#v) error = %v, want ErrInvalidOptions", opts, err)
		}
	}

	// The working directory comes back with links resolved, like
	// /private/var on macOS.
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore chdir: %v", err)
		}
	})
	result, err := PlanMessage(context.Background(), Options{Root: "out/../out", Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("PlanMessage relative root: %v", err)
	}
	if got, want := result.Files[0].Path, filepath.Join(cwd, "out", "Acct", "Unfiled", "no-date", "no-date-s--0123456789abcdef.md"); got != want {
		t.Fatalf("absolute path = %q, want %q", got, want)
	}
}

func plainMessage(loc *time.Location) mail.Message {
	return mail.Message{
		LocalID:    plainLocalID,
		MessageID:  "<m-1@example.com>",
		InReplyTo:  "<parent@example.com>",
		References: []string{" <ref-b@example.com> ", "", "<ref-a@example.com>", "<ref-a@example.com>"},
		ThreadID:   "thread-42",
		From:       mail.Address{Name: "Alice \"A\"\nLead", Email: "alice@example.com"},
		To: []mail.Address{
			{Name: "Bob", Email: "bob@example.com"},
			{Email: "team@example.com"},
			{},
		},
		Cc:       []mail.Address{{Name: "Carol: Ops"}},
		ReplyTo:  []mail.Address{{Name: "Support", Email: "support@example.com"}},
		Subject:  "Quarterly \"Plan\":\nNext",
		Date:     time.Date(2026, 6, 30, 15, 4, 5, 123456789, loc),
		Folders:  []string{"Work/Inbox", "Work/Inbox", "/", ""},
		Flags:    mail.Flags{Seen: true, Flagged: true},
		BodyText: "Line one\r\nLine two\n\n",
		Attachments: []mail.Attachment{
			{Filename: "report.pdf", MIMEType: "application/pdf", Size: 3, Data: []byte("PDF")},
			{Filename: "remote.dat", MIMEType: "application/octet-stream", Size: 7},
			{MIMEType: "text/plain", Inline: true, ContentID: "cid-1", Data: []byte{}},
		},
	}
}

func withFolders(m mail.Message, folders ...string) mail.Message {
	m.Folders = folders
	return m
}

func attachmentStatuses(in []AttachmentResult) []WriteStatus {
	out := make([]WriteStatus, 0, len(in))
	for _, att := range in {
		out = append(out, att.Status)
	}
	return out
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	return readFile(t, filepath.Join("testdata", name))
}

// checkGolden compares got with a file in testdata. MAILMD_UPDATE_GOLDEN=1
// rewrites the file instead.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("MAILMD_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want := readFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func statFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func TestMessageFileFeatures(t *testing.T) {
	root := t.TempDir()
	newYear := time.Date(2026, 12, 31, 23, 30, 0, 0, time.FixedZone("", -5*3600))
	msg := mail.Message{
		LocalID:  plainLocalID,
		Subject:  "Happy new year",
		From:     mail.Address{Name: "Ann", Email: "ann@example.com"},
		Date:     newYear,
		Folders:  []string{"INBOX"},
		BodyText: "See the forwarded report.",
		Raw:      []byte("Subject: Happy new year\r\n\r\nSee the forwarded report.\r\n"),
		Forwarded: []mail.Message{{Subject: "Outage report", From: mail.Address{Email: "ops@example.com"}, BodyText: "The server was down for 20 minutes.",
			Attachments: []mail.Attachment{{Filename: "uptime.csv", MIMEType: "text/csv"}, {MIMEType: "image/png"}}}},
		Attachments: []mail.Attachment{
			{MIMEType: "message/rfc822", Data: []byte("Subject: Outage report\r\n\r\nThe server was down.\r\n"), Size: 40},
			{MIMEType: "text/calendar", Data: []byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"), Size: 30},
		},
	}
	result, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct", KeepRaw: true}, msg)
	if err != nil {
		t.Fatal(err)
	}
	file := result.Files[0]
	if !strings.HasPrefix(file.RelPath, "Acct/INBOX/2026/2026-12-31-happy-new-year--") {
		t.Fatalf("a mail sent on New Year's Eve in its own time zone was filed at %s", file.RelPath)
	}
	got := string(readFile(t, file.Path))
	for _, want := range []string{
		`date: "2026-12-31T23:30:00-05:00"`,
		"## Forwarded message",
		"**Attachments:** uptime.csv, attachment.png",
		"The server was down for 20 minutes.",
		"01-forwarded-message.eml",
		"02-invite.ics",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("file is missing %q:\n%s", want, got)
		}
	}
	if raw := readFile(t, strings.TrimSuffix(file.Path, ".md")+".eml"); !bytes.Equal(raw, msg.Raw) || file.RawStatus != WriteCreated {
		t.Fatalf("raw copy = %q, status %s", raw, file.RawStatus)
	}

	// Without a Date header the time the provider received the message is
	// used.
	noDate := mail.Message{LocalID: otherLocalID, Subject: "No date", Received: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)}
	result, err = WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, noDate)
	if err != nil || !strings.Contains(result.Files[0].RelPath, "/2026/2026-03-04-no-date--") {
		t.Fatalf("no date: %#v %v", result.Files, err)
	}

	// A message over the size limit says so instead of showing a body.
	stub := mail.Message{LocalID: "mailmd-localid-v1-cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Subject: "Scan", OmittedSize: 72_400_000}
	result, err = WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, stub)
	if err != nil {
		t.Fatal(err)
	}
	got = string(readFile(t, result.Files[0].Path))
	if !strings.Contains(got, "omitted_bytes: 72400000") || !strings.Contains(got, "saved only the headers of this message") || !strings.Contains(got, "The message is 72.4 MB, which is more than the size limit.") {
		t.Fatalf("stub:\n%s", got)
	}
}

func TestUnreadableMessageKeepsItsHeadersAndOriginal(t *testing.T) {
	root := t.TempDir()
	raw := []byte("Subject: Broken\r\nFrom: ann@example.com\r\nContent-Transfer-Encoding: base64\r\n\r\n%%% not base64\r\n")
	msg := mail.Message{
		LocalID:    mail.RawLocalID(raw),
		Subject:    "Broken",
		From:       mail.Address{Email: "ann@example.com"},
		Folders:    []string{"INBOX"},
		Ref:        "7:42",
		ParseError: "a part has a broken transfer encoding",
		Raw:        raw,
	}
	// --keep-raw is off, and the original is still saved.
	result, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	file := result.Files[0]
	if got := readFile(t, file.RawPath); !bytes.Equal(got, raw) {
		t.Fatalf("raw copy = %q", got)
	}
	got := string(readFile(t, file.Path))
	rawName := filepath.Base(file.RawPath)
	for _, want := range []string{
		`parse_error: "a part has a broken transfer encoding"`,
		`ref: "7:42"`,
		"_mailmd could not read this message, because a part has a broken transfer encoding. The original message is saved as [" + rawName + "](" + rawName + ")._",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("file is missing %q:\n%s", want, got)
		}
	}
	entry, ok := ReadIndexEntry(file.Path, "INBOX/x.md")
	if !ok || entry.Ref != "7:42" || entry.Subject != "Broken" {
		t.Fatalf("entry = %+v %v", entry, ok)
	}
}

func TestDeletionAndFlagsAreWrittenIntoTheFile(t *testing.T) {
	root := t.TempDir()
	msg := plainMessage(time.UTC)
	msg.Ref = "1:5"
	msg.OmittedSize = 99
	result, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	path := result.Files[0].Path
	if err := MarkDeleted(context.Background(), path, "2026-10-26", nil); err != nil {
		t.Fatal(err)
	}
	if err := UpdateFlags(context.Background(), path, []string{"seen"}, nil); err != nil {
		t.Fatal(err)
	}
	got := string(readFile(t, path))
	flags, deleted, attachments := strings.Index(got, "flags: "), strings.Index(got, `deleted_on_server: "2026-10-26"`), strings.Index(got, "omitted_bytes: ")
	if flags < 0 || deleted < flags || attachments < deleted {
		t.Fatalf("frontmatter order:\n%s", got)
	}
	entry, ok := ReadIndexEntry(path, "x.md")
	if !ok || !entry.Gone || entry.Ref != "1:5" || entry.Omitted != 99 || len(entry.Flags) != 1 {
		t.Fatalf("entry = %+v", entry)
	}
	if id, ok := LocalIDOf(path); !ok || id != msg.LocalID {
		t.Fatalf("local id = %q", id)
	}
	// Back on the server: the field goes away again.
	if err := MarkDeleted(context.Background(), path, "", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readFile(t, path)), "deleted_on_server") {
		t.Fatal("deleted_on_server was not removed")
	}
}

func TestEmptyBodyHasNoDoubleBlankLine(t *testing.T) {
	msg := mail.Message{LocalID: plainLocalID, Subject: "Invite", Attachments: []mail.Attachment{{MIMEType: "text/calendar", Data: []byte("BEGIN:VCALENDAR\r\n"), Size: 17}}}
	result, err := WriteMessage(context.Background(), Options{Root: t.TempDir(), Account: "Acct"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, result.Files[0].Path)); strings.Contains(got, "\n\n\n") || !strings.Contains(got, "# Invite\n\n## Attachments\n") {
		t.Fatalf("file:\n%q", got)
	}
}
