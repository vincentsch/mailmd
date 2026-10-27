package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend"
	imapbackend "github.com/vincentsch/mailmd/internal/backend/imap"
	"github.com/vincentsch/mailmd/internal/backend/imap/imaptest"
	"github.com/vincentsch/mailmd/internal/writer"
)

// These tests sync from the in-process IMAP server, so the IMAP backend and
// the sync work together as in a real run.

func imapOptions(root string, srv *imaptest.Server) Options {
	return Options{
		Account:         accounts.AccountConfig{Name: "work", Backend: "imap", ArchiveDir: "work", CredentialRef: "work/imap.json"},
		Root:            root,
		MaxMessageBytes: 2000,
		MaxBatchBytes:   10000,
		BatchSize:       100,
		Open: func(ctx context.Context) (backend.Backend, error) {
			return imapbackend.Open(ctx, imapbackend.Config{Host: srv.Host(), Port: srv.Port(), Username: srv.Username(), Password: srv.Password(), TLSMode: imapbackend.TLSImplicit, RootCAs: srv.CertPool()})
		},
		Clock:  func() time.Time { return testNow },
		Random: func() float64 { return 0.5 },
		Sleep:  func(context.Context, time.Duration) error { return nil },
	}
}

func rawMessage(id, subject, body string, headers ...string) []byte {
	return []byte("Message-ID: <" + id + "@example.test>\r\n" +
		"Date: Mon, 19 Oct 2026 10:00:00 +0000\r\n" +
		"From: Ann <ann@example.test>\r\n" +
		strings.Join(headers, "") +
		"Subject: " + subject + "\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
}

func inbox(messages ...imaptest.MessageSeed) imaptest.Options {
	return imaptest.Options{Folders: []imaptest.FolderSeed{{Name: "INBOX", Delimiter: "/", UIDValidity: 7, Messages: messages}}}
}

func markdownFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".md") {
			files[path] = readFile(t, path)
		}
		return nil
	})
	return files
}

func TestRaisingTheSizeLimitReplacesTheStub(t *testing.T) {
	big := rawMessage("big", "Big report", "START "+strings.Repeat("x", 4000)+" END")
	srv := imaptest.Start(t, inbox(
		imaptest.MessageSeed{UID: 1, Raw: rawMessage("a", "Small", "hello")},
		imaptest.MessageSeed{UID: 2, Raw: big},
	))
	root := t.TempDir()
	opts := imapOptions(root, srv)
	if r := mustRun(t, opts); r.New != 2 || r.Diagnostics[backend.ReasonTooLarge] != 1 {
		t.Fatalf("first sync = %+v", r)
	}
	// The same limit does not fetch the stub again.
	if r := mustRun(t, opts); r.Updated != 0 {
		t.Fatalf("second sync = %+v", r)
	}
	opts.MaxMessageBytes = 8000
	r := mustRun(t, opts)
	if r.New != 0 || r.Updated != 1 {
		t.Fatalf("sync with a higher limit = %+v", r)
	}
	files := markdownFiles(t, filepath.Join(root, "work"))
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}
	full := false
	for _, text := range files {
		if strings.Contains(text, "Big report") {
			full = strings.Contains(text, "START") && !strings.Contains(text, "omitted_bytes") && !strings.Contains(text, "saved only the headers")
		}
	}
	if !full {
		t.Fatalf("the stub was not replaced: %v", files)
	}
	index := readFile(t, filepath.Join(root, "work", IndexFile))
	if strings.Count(index, "\n") != 2 || strings.Contains(index, "omitted_bytes") {
		t.Fatalf("index:\n%s", index)
	}
	// status no longer lists the message as too large.
	status, err := Status(root, opts.Account)
	if err != nil || len(status.Problems) != 0 || status.Diagnostics[backend.ReasonTooLarge] != 0 {
		t.Fatalf("status after the refetch = %+v, %v", status, err)
	}
	if r := mustRun(t, opts); r.Updated != 0 || r.New != 0 {
		t.Fatalf("sync after the refetch = %+v", r)
	}
}

func TestOneLargeMessageDoesNotBlockAFullBatch(t *testing.T) {
	var seeds []imaptest.MessageSeed
	for i := 1; i <= 100; i++ {
		body := "body"
		if i == 50 {
			body = strings.Repeat("x", 3000)
		}
		seeds = append(seeds, imaptest.MessageSeed{UID: uint32(i), Raw: rawMessage(fmt.Sprint(i), fmt.Sprintf("Message %d", i), body)})
	}
	srv := imaptest.Start(t, inbox(seeds...))
	opts := imapOptions(t.TempDir(), srv)
	opts.MaxBatchBytes = 1 << 20
	if r := mustRun(t, opts); r.New != 100 || r.Diagnostics[backend.ReasonTooLarge] != 1 {
		t.Fatalf("sync = %+v", r)
	}
}

func TestUnreadableMessagesAreKept(t *testing.T) {
	srv := imaptest.Start(t, inbox(
		imaptest.MessageSeed{UID: 1, Raw: []byte("Message-ID: <b64@example.test>\r\nFrom: Ann <ann@example.test>\r\nSubject: Broken\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nnot %%% base64\r\n")},
		imaptest.MessageSeed{UID: 2, Raw: rawMessage("ok", "Fine", "hello")},
	))
	root := t.TempDir()
	r := mustRun(t, imapOptions(root, srv))
	if r.New != 2 || r.Diagnostics[backend.ReasonParseFailure] != 1 {
		t.Fatalf("sync = %+v", r)
	}
	var broken string
	for path, text := range markdownFiles(t, filepath.Join(root, "work")) {
		if strings.Contains(text, "Broken") {
			broken = path
		}
	}
	if broken == "" || !strings.Contains(readFile(t, broken), "parse_error: ") {
		t.Fatalf("no file for the broken message")
	}
	if raw := readFile(t, strings.TrimSuffix(broken, ".md")+".eml"); !strings.Contains(raw, "not %%% base64") {
		t.Fatalf("original = %q", raw)
	}
}

func TestEachProblemIsReportedOnce(t *testing.T) {
	day := testNow.AddDate(0, 0, -3)
	srv := imaptest.Start(t, inbox(
		imaptest.MessageSeed{UID: 1, InternalDate: day, Raw: []byte("Message-ID: <b64@example.test>\r\nFrom: Ann <ann@example.test>\r\nSubject: Broken\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nnot %%% base64\r\n")},
		imaptest.MessageSeed{UID: 2, InternalDate: day, Raw: rawMessage("big", "Big", strings.Repeat("x", 4000))},
		imaptest.MessageSeed{UID: 3, InternalDate: testNow.AddDate(-1, 0, 0), Raw: rawMessage("old", "Old", "hello")},
	))
	root := t.TempDir()
	opts := imapOptions(root, srv)
	opts.Since = testNow.AddDate(0, -1, 0)
	if r := mustRun(t, opts); r.Diagnostics[backend.ReasonParseFailure] != 1 || r.Diagnostics[backend.ReasonTooLarge] != 1 || r.Outcome != OutcomeWarnings {
		t.Fatalf("first sync = %+v", r)
	}
	again := func(step string) {
		t.Helper()
		r := mustRun(t, opts)
		if len(r.Diagnostics) != 0 || r.Outcome != OutcomeCompleted || r.New != 0 {
			t.Fatalf("%s: sync = %+v", step, r)
		}
		status, err := Status(root, opts.Account)
		if err != nil || len(status.Problems) != 2 {
			t.Fatalf("%s: status = %+v, %v", step, status.Problems, err)
		}
	}
	// A wider --since reads the folder again and finds one older message.
	opts.Since = time.Time{}
	if r := mustRun(t, opts); len(r.Diagnostics) != 0 || r.Outcome != OutcomeCompleted || r.New != 1 {
		t.Fatalf("wider since: sync = %+v", r)
	}
	// So does a new UIDVALIDITY, which gives every message a new ref.
	srv.BumpUIDValidity("INBOX")
	again("new UIDVALIDITY")
	// And a lost .mailmd folder, which holds what the earlier syncs found.
	if err := os.Rename(filepath.Join(root, "work", ".mailmd"), filepath.Join(t.TempDir(), "old")); err != nil {
		t.Fatal(err)
	}
	again("lost .mailmd")
}

func TestRenumberedFolderDoesNotMixUpMessages(t *testing.T) {
	srv := imaptest.Start(t, inbox(imaptest.MessageSeed{UID: 1, Raw: rawMessage("one", "First", "one")}))
	root := t.TempDir()
	opts := imapOptions(root, srv)
	mustRun(t, opts)
	// The server renumbers INBOX, and UID 1 is now another message.
	srv.Expunge("INBOX", 1)
	srv.BumpUIDValidity("INBOX")
	srv.AppendMessage("INBOX", imaptest.MessageSeed{UID: 1, Raw: rawMessage("two", "Second", "two"), Flags: []string{string(imap.FlagSeen)}})
	if r := mustRun(t, opts); r.New != 1 || r.Deleted != 1 {
		t.Fatalf("sync after renumbering = %+v", r)
	}
	for _, text := range markdownFiles(t, filepath.Join(root, "work")) {
		first := strings.Contains(text, "# First")
		if first && (strings.Contains(text, `"seen"`) || !strings.Contains(text, "deleted_on_server: ")) {
			t.Fatalf("the old message took the new one's state:\n%s", text)
		}
		if !first && (!strings.Contains(text, `"seen"`) || strings.Contains(text, "deleted_on_server")) {
			t.Fatalf("new message:\n%s", text)
		}
	}
}

func TestDamagedIndexLineIsRepaired(t *testing.T) {
	srv := imaptest.Start(t, inbox(imaptest.MessageSeed{UID: 1, Raw: rawMessage("one", "First", "one")}))
	root := t.TempDir()
	opts := imapOptions(root, srv)
	mustRun(t, opts)
	index := filepath.Join(root, "work", IndexFile)
	// A crash cut the last line short.
	data := readFile(t, index)
	if err := os.WriteFile(index, []byte(data[:len(data)-10]), 0o600); err != nil {
		t.Fatal(err)
	}
	srv.AppendMessage("INBOX", imaptest.MessageSeed{UID: 2, Raw: rawMessage("two", "Second", "two")})
	if r := mustRun(t, opts); r.New != 1 {
		t.Fatalf("sync = %+v", r)
	}
	entries, damaged, err := readIndex(filepath.Join(root, "work"))
	if err != nil || damaged || len(entries) != 2 {
		t.Fatalf("index = %v damaged=%v err=%v\n%s", entries, damaged, err, readFile(t, index))
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Ref, "7:") {
			t.Fatalf("entry lost its ref: %+v", e)
		}
	}
}

func TestRebuiltIndexKeepsRefsAndDeletions(t *testing.T) {
	srv := imaptest.Start(t, inbox(
		imaptest.MessageSeed{UID: 1, Raw: rawMessage("one", "First", "one")},
		imaptest.MessageSeed{UID: 2, Raw: rawMessage("two", "Second", "two")},
	))
	root := t.TempDir()
	opts := imapOptions(root, srv)
	mustRun(t, opts)
	srv.Expunge("INBOX", 2)
	if r := mustRun(t, opts); r.Deleted != 1 {
		t.Fatalf("sync = %+v", r)
	}
	dir := filepath.Join(root, "work")
	before := readFile(t, filepath.Join(dir, IndexFile))
	if err := os.Remove(filepath.Join(dir, IndexFile)); err != nil {
		t.Fatal(err)
	}
	srv.SetFlags("INBOX", 1, string(imap.FlagSeen))
	if r := mustRun(t, opts); r.Updated != 1 || r.Deleted != 0 {
		t.Fatalf("sync after the rebuild = %+v", r)
	}
	after := readFile(t, filepath.Join(dir, IndexFile))
	if !strings.Contains(after, `"gone":true`) || strings.Count(after, `"ref":"7:`) != 2 || before == after {
		t.Fatalf("index before:\n%s\nafter:\n%s", before, after)
	}
}

func TestIndexMustBeARegularFileInTheArchive(t *testing.T) {
	srv := imaptest.Start(t, inbox(imaptest.MessageSeed{UID: 1, Raw: rawMessage("one", "First", "one")}))
	root := t.TempDir()
	opts := imapOptions(root, srv)
	mustRun(t, opts)
	index := filepath.Join(root, "work", IndexFile)
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, index); err != nil {
		t.Skip("symbolic links are not available:", err)
	}
	srv.AppendMessage("INBOX", imaptest.MessageSeed{UID: 2, Raw: rawMessage("two", "Second", "two")})
	if _, err := Run(context.Background(), opts); Category(err) != CategoryLocal {
		t.Fatalf("Run() = %v, want a local error", err)
	}
	if data := readFile(t, outside); data != "" {
		t.Fatalf("mail data went outside the archive: %q", data)
	}
}

func TestThreadsJoinAcrossShortenedReferences(t *testing.T) {
	ref := func(ids ...string) string {
		for i := range ids {
			ids[i] = "<" + ids[i] + "@example.test>"
		}
		return strings.Join(ids, " ")
	}
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{
		// Sent sorts before INBOX here, so replies come before the
		// messages they answer.
		{Name: "Gesendet", Delimiter: "/", UIDValidity: 3, Messages: []imaptest.MessageSeed{
			{UID: 1, Raw: rawMessage("t1", "Re: Plan", "1", "References: "+ref("t0")+"\r\n")},
			{UID: 2, Raw: rawMessage("t3", "Re: Plan", "3", "In-Reply-To: "+ref("t2")+"\r\n")},
			{UID: 3, Raw: rawMessage("t5", "Re: Plan", "5", "References: "+ref("t3", "t4")+"\r\n")},
		}},
		{Name: "INBOX", Delimiter: "/", UIDValidity: 4, Messages: []imaptest.MessageSeed{
			{UID: 1, Raw: rawMessage("t0", "Plan", "0")},
			{UID: 2, Raw: rawMessage("t2", "Re: Plan", "2", "References: "+ref("t0", "t1")+"\r\n")},
			{UID: 3, Raw: rawMessage("t4", "Re: Plan", "4", "References: "+ref("t2", "t3")+"\r\n")},
		}},
	}})
	root := t.TempDir()
	mustRun(t, imapOptions(root, srv))
	entries, _, err := readIndex(filepath.Join(root, "work"))
	if err != nil || len(entries) != 6 {
		t.Fatalf("entries = %d, %v", len(entries), err)
	}
	for _, e := range entries {
		if e.Thread != "<t0@example.test>" {
			t.Fatalf("%s is in thread %q", e.MessageID, e.Thread)
		}
		text := readFile(t, filepath.Join(root, "work", filepath.FromSlash(e.Path)))
		if !strings.Contains(text, `thread_id: "<t0@example.test>"`) {
			t.Fatalf("file:\n%s", text)
		}
	}
	// A reply without threading headers gets its own thread, also with the
	// same subject. A subject like "Invoice" would put unrelated mail into
	// one thread.
	srv.AppendMessage("INBOX", imaptest.MessageSeed{UID: 4, Raw: rawMessage("t6", "AW: Plan", "6")})
	mustRun(t, imapOptions(root, srv))
	entries, _, _ = readIndex(filepath.Join(root, "work"))
	for _, e := range entries {
		if e.MessageID == "<t6@example.test>" && e.Thread != "<t6@example.test>" {
			t.Fatalf("reply without headers is in thread %q", e.Thread)
		}
	}
	_ = writer.IndexEntry{}
}

func TestFoldersThatDifferOnlyInCaseStayApart(t *testing.T) {
	same := rawMessage("same", "Same message", "hello")
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{
		{Name: "Work", Delimiter: "/", UIDValidity: 1, Messages: []imaptest.MessageSeed{{UID: 1, Raw: same}}},
		{Name: "work", Delimiter: "/", UIDValidity: 2, Messages: []imaptest.MessageSeed{{UID: 1, Raw: same}}},
		{Name: "A:B", Delimiter: "/", UIDValidity: 3, Messages: []imaptest.MessageSeed{{UID: 1, Raw: same}}},
		{Name: "A?B", Delimiter: "/", UIDValidity: 4, Messages: []imaptest.MessageSeed{{UID: 1, Raw: same}}},
	}})
	root := t.TempDir()
	opts := imapOptions(root, srv)
	if r := mustRun(t, opts); r.New != 4 {
		t.Fatalf("first sync = %+v", r)
	}
	entries, _, _ := readIndex(filepath.Join(root, "work"))
	dirs := map[string]bool{}
	for _, e := range entries {
		dir := strings.ToLower(e.Path[:strings.Index(e.Path, "/")])
		if dirs[dir] {
			t.Fatalf("two folders share %q: %v", dir, entries)
		}
		dirs[dir] = true
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %v", entries)
	}
	if r := mustRun(t, opts); r.New != 0 || r.Updated != 0 {
		t.Fatalf("second sync = %+v", r)
	}
}
