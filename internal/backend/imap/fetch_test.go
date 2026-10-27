package imap

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/imap/imaptest"
)

func TestFetchFirstBatchIsReadOnly(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()

	res := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX"), Limit: 30})
	if !res.Done {
		t.Fatalf("Done = false")
	}
	if validity, last := decodeTestCursor(t, srv.ScopeKey("INBOX"), res.Cursor); validity != 101 || last != 22 {
		t.Fatalf("cursor = %d/%d, want 101/22", validity, last)
	}
	// Messages that can't be read are also saved, with their headers and
	// original bytes.
	want := []string{"Plain hello", "HTML only", "Alternative ✓", "Nested mixed", "Related inline", "Signed content", "café", "Address edges", "Sender only", "Attachment set", "Missing date", "Malformed body", "", "", "After bad messages"}
	if got := messageSubjects(res); !reflect.DeepEqual(got, want) {
		t.Fatalf("subjects = %#v", got)
	}
	if got := diagnosticRefs(res.Diagnostics); !reflect.DeepEqual(got, []string{"101:13", "101:20", "101:21"}) {
		t.Fatalf("diagnostics = %#v", res.Diagnostics)
	}
	for _, msg := range res.Messages[11:14] {
		if msg.ParseError == "" || len(msg.Raw) == 0 || msg.LocalID == "" || !strings.HasPrefix(msg.Ref, "101:") {
			t.Fatalf("unreadable message = %+v", msg)
		}
	}
	for _, msg := range res.Messages {
		if !reflect.DeepEqual(msg.Folders, []string{"INBOX"}) || msg.LocalID == "" || msg.Received.IsZero() {
			t.Fatalf("message %q = folders %v local id %q received %v", msg.Subject, msg.Folders, msg.LocalID, msg.Received)
		}
	}
	debug := srv.DebugLog()
	for _, want := range []string{"EXAMINE INBOX", "UID SEARCH", "BODY.PEEK[]"} {
		if !strings.Contains(debug, want) {
			t.Fatalf("debug log is missing %q", want)
		}
	}
	if changing := regexp.MustCompile(`(?m)^T\d+ (UID )?(STORE|SELECT|EXPUNGE|COPY|MOVE|APPEND|DELETE|FETCH \S+ \(.*BODY\[)`); changing.MatchString(debug) {
		t.Fatalf("sent a command that can change the mailbox: %q", changing.FindString(debug))
	}
	refetch := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX"), Limit: 30})
	if refetch.Messages[0].Flags.Seen {
		t.Fatalf("fetching marked the first message as seen")
	}
}

// Gmail and other servers use sparse UIDs. One SEARCH must serve all
// batches, instead of one round trip per UID range.
func TestFetchSparseUIDsWithOneSearch(t *testing.T) {
	var messages []imaptest.MessageSeed
	for i := 1; i <= 7; i++ {
		uid := uint32(i * 100000)
		messages = append(messages, imaptest.MessageSeed{UID: uid, InternalDate: time.Date(2026, 1, i, 0, 0, 0, 0, time.UTC), Raw: rawTestMessage(strconv.Itoa(i), fmt.Sprintf("Sparse %d", i))})
	}
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{{Name: "INBOX", Delimiter: "/", UIDValidity: 9, Messages: messages}}})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	scope := srv.ScopeKey("INBOX")

	var subjects []string
	var cursor backend.Cursor
	for batch := 0; ; batch++ {
		res := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: scope, Cursor: cursor, Limit: 3})
		subjects = append(subjects, messageSubjects(res)...)
		cursor = res.Cursor
		if res.Done {
			if batch != 2 {
				t.Fatalf("took %d batches, want 3", batch+1)
			}
			break
		}
	}
	if len(subjects) != 7 || subjects[6] != "Sparse 7" {
		t.Fatalf("subjects = %#v", subjects)
	}
	if n := len(regexp.MustCompile(`(?m)^T\d+ UID SEARCH`).FindAllString(srv.DebugLog(), -1)); n != 1 {
		t.Fatalf("sent %d searches, want 1", n)
	}

	// Nothing is new, so there is no search and no fetch at all.
	before := srv.DebugLog()
	res := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: scope, Cursor: cursor})
	if !res.Done || len(res.Messages) != 0 {
		t.Fatalf("second sync = %#v", res)
	}
	if delta := strings.ToUpper(strings.TrimPrefix(srv.DebugLog(), before)); strings.Contains(delta, "SEARCH") || strings.Contains(delta, "UID FETCH") {
		t.Fatalf("an unchanged folder sent message commands:\n%s", delta)
	}

	// A new message arrives.
	srv.AppendMessage("INBOX", imaptest.MessageSeed{UID: 900000, Raw: rawTestMessage("new", "New one")})
	res = fetchTestScope(t, b, backend.FetchRequest{ScopeKey: scope, Cursor: cursor})
	if got := messageSubjects(res); !reflect.DeepEqual(got, []string{"New one"}) || !res.Done {
		t.Fatalf("after append = %#v", got)
	}
}

func TestFetchSinceSkipsOlderMessages(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{{Name: "INBOX", Delimiter: "/", UIDValidity: 1, Messages: []imaptest.MessageSeed{
		{UID: 1, InternalDate: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC), Raw: rawTestMessage("old", "Old")},
		{UID: 2, InternalDate: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Raw: rawTestMessage("new", "New")},
	}}}})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	res := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX"), Since: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)})
	if got := messageSubjects(res); !reflect.DeepEqual(got, []string{"New"}) {
		t.Fatalf("subjects = %#v", got)
	}
}

// A message the server lists but does not send must stop the folder without
// moving the cursor past it.
func TestFetchMissingBodyKeepsTheCursor(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{{Name: "INBOX", Delimiter: "/", UIDValidity: 1, Messages: []imaptest.MessageSeed{
		{UID: 1, Raw: rawTestMessage("one", "One")},
		{UID: 2, Raw: rawTestMessage("two", "Two"), OmitBody: true},
	}}}})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	res, err := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX")})
	if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "did not send its content") {
		t.Fatalf("Fetch() = %v, want a protocol error", err)
	}
	if len(res.Cursor) != 0 || len(res.Messages) != 0 {
		t.Fatalf("Fetch() returned progress on error: %#v", res)
	}
}

// A message deleted between listing and download is reported as a diagnostic.
// It is no error.
func TestFetchReportsMessagesDeletedDuringTheBatch(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{{Name: "INBOX", Delimiter: "/", UIDValidity: 1, Messages: []imaptest.MessageSeed{
		{UID: 1, Raw: rawTestMessage("one", "One")},
		{UID: 2, Raw: rawTestMessage("two", "Two")},
		{UID: 3, Raw: rawTestMessage("three", "Three")},
	}}}})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	scope := srv.ScopeKey("INBOX")
	first := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: scope, Limit: 1})
	srv.Expunge("INBOX", 2)
	second := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: scope, Cursor: first.Cursor, Limit: 5})
	if got := messageSubjects(second); !reflect.DeepEqual(got, []string{"Three"}) {
		t.Fatalf("subjects = %#v", got)
	}
	if len(second.Diagnostics) != 1 || second.Diagnostics[0].Ref != "1:2" || second.Diagnostics[0].Reason != backend.ReasonVanished {
		t.Fatalf("diagnostics = %#v", second.Diagnostics)
	}
}

// A message over the size limit is saved with its headers only. It does not
// block the folder.
func TestFetchSavesHeadersOfTooLargeMessages(t *testing.T) {
	big := rawTestMessage("big", "Big report")
	big = append(big, []byte(strings.Repeat("x", 5000))...)
	lying := uint32(10)
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{{Name: "INBOX", Delimiter: "/", UIDValidity: 1, Messages: []imaptest.MessageSeed{
		{UID: 1, Raw: big, Flags: []string{string(imap.FlagSeen)}},
		{UID: 2, Raw: rawTestMessage("small", "Small")},
		{UID: 3, Raw: append(rawTestMessage("liar", "Lying size"), []byte(strings.Repeat("y", 5000))...), ReportedSize: &lying},
	}}}})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	res := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX"), MaxMessageBytes: 2000, MaxBatchBytes: 4000})
	if !res.Done {
		t.Fatalf("Done = false")
	}
	if got := messageSubjects(res); !reflect.DeepEqual(got, []string{"Big report", "Small", "Lying size"}) {
		t.Fatalf("subjects = %#v", got)
	}
	stub := res.Messages[0]
	if stub.OmittedSize != int64(len(big)) || stub.BodyText != "" || !stub.Flags.Seen || stub.MessageID != "<big@example.test>" {
		t.Fatalf("stub = %#v", stub)
	}
	if res.Messages[2].OmittedSize == 0 {
		t.Fatalf("message with a wrong RFC822.SIZE was not saved as headers only: %#v", res.Messages[2])
	}
	var reasons []string
	for _, d := range res.Diagnostics {
		reasons = append(reasons, d.Ref+":"+string(d.Reason))
	}
	if !reflect.DeepEqual(reasons, []string{"1:1:too_large", "1:3:too_large"}) {
		t.Fatalf("diagnostics = %v", reasons)
	}
}

func TestFetchSplitsBatchesByBytes(t *testing.T) {
	var messages []imaptest.MessageSeed
	for i := 1; i <= 4; i++ {
		raw := append(rawTestMessage(strconv.Itoa(i), fmt.Sprintf("M%d", i)), []byte(strings.Repeat("z", 900))...)
		messages = append(messages, imaptest.MessageSeed{UID: uint32(i), Raw: raw})
	}
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{{Name: "INBOX", Delimiter: "/", UIDValidity: 1, Messages: messages}}})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	res := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX"), MaxMessageBytes: 1500, MaxBatchBytes: 2500})
	if got := messageSubjects(res); !reflect.DeepEqual(got, []string{"M1", "M2"}) || res.Done {
		t.Fatalf("first batch = %#v done %v", got, res.Done)
	}
	res = fetchTestScope(t, b, backend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX"), Cursor: res.Cursor, MaxMessageBytes: 1500, MaxBatchBytes: 2500})
	if got := messageSubjects(res); !reflect.DeepEqual(got, []string{"M3", "M4"}) || !res.Done {
		t.Fatalf("second batch = %#v done %v", got, res.Done)
	}
}

func TestFetchUIDValidityChangeStartsOver(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	scope := srv.ScopeKey("Work")
	first := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: scope})
	srv.BumpUIDValidity("Work")
	res := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: scope, Cursor: first.Cursor, Limit: 1})
	if got := messageSubjects(res); !reflect.DeepEqual(got, []string{"Work one"}) {
		t.Fatalf("subjects = %#v", got)
	}
	if validity, last := decodeTestCursor(t, scope, res.Cursor); validity != 202 || last != 1 {
		t.Fatalf("cursor = %d/%d, want 202/1", validity, last)
	}
}

func TestFetchFolderErrors(t *testing.T) {
	seeds := imaptest.SeedFolders()
	seeds = append(seeds,
		imaptest.FolderSeed{Name: "Locked", Delimiter: "/", UIDValidity: 3, SelectError: &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNoPerm, Text: "Not for you"}},
		imaptest.FolderSeed{Name: "Gone", Delimiter: "/", UIDValidity: 4, SelectError: &imap.Error{Type: imap.StatusResponseTypeNo, Text: "Mailbox doesn't exist: Gone"}},
		imaptest.FolderSeed{Name: "Busy", Delimiter: "/", UIDValidity: 5, SelectError: &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeUnavailable, Text: "Try later"}},
	)
	srv := imaptest.Start(t, imaptest.Options{Folders: seeds})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	fetch := func(name string) error {
		_, err := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: srv.ScopeKey(name)})
		return err
	}
	if err := fetch("Locked"); !errors.Is(err, backend.ErrPermissionDenied) || !strings.Contains(err.Error(), "Not for you") {
		t.Fatalf("Locked: %v", err)
	}
	if err := fetch("Gone"); !errors.Is(err, backend.ErrScopeNotFound) {
		t.Fatalf("Gone: %v", err)
	}
	if err := fetch("Missing"); !errors.Is(err, backend.ErrScopeNotFound) {
		t.Fatalf("Missing: %v", err)
	}
	if err := fetch("Busy"); !errors.Is(err, backend.ErrRetryable) {
		t.Fatalf("Busy: %v", err)
	}
	if err := fetch("Fault/Missing UIDVALIDITY"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("missing UIDVALIDITY: %v", err)
	}
	// The connection stays usable after a refused folder.
	if res := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: srv.ScopeKey("Flat")}); len(res.Messages) != 1 {
		t.Fatalf("Flat after errors = %#v", res)
	}
	// A cursor from another folder is rejected before any network use.
	before := srv.DebugLog()
	_, err := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: srv.ScopeKey("Work"), Cursor: encodeCursor(srv.ScopeKey("INBOX"), cursor{validity: 101, last: 1})})
	if !errors.Is(err, backend.ErrInvalidCursor) || srv.DebugLog() != before {
		t.Fatalf("wrong-scope cursor: %v", err)
	}
}

func TestFetchWithoutUIDNextStillWorks(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{{Name: "INBOX", Delimiter: "/", UIDValidity: 1, OmitUIDNext: true, Messages: []imaptest.MessageSeed{{UID: 4, Raw: rawTestMessage("x", "X")}}}}})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	res := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX")})
	if got := messageSubjects(res); !reflect.DeepEqual(got, []string{"X"}) {
		t.Fatalf("subjects = %#v", got)
	}
}

func TestFetchDisplayCollisionScopesHaveIndependentCursors(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	a := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: srv.ScopeKey("Foo.Bar")})
	c := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: srv.ScopeKey("Foo/Bar")})
	if len(a.Messages) != 1 || len(c.Messages) != 1 || a.Messages[0].Folders[0] != "Foo/Bar" || c.Messages[0].Folders[0] != "Foo/Bar" {
		t.Fatalf("collision fetch = %#v / %#v", a.Messages, c.Messages)
	}
	if _, err := decodeCursor(srv.ScopeKey("Foo/Bar"), a.Cursor); err == nil {
		t.Fatalf("cursor of one scope decoded for the other")
	}
}

func TestFetchCancelInterruptsBlockedFetch(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{BlockFetch: true})
	defer srv.UnblockFetch()
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.Fetch(ctx, backend.FetchRequest{ScopeKey: srv.ScopeKey("Work")})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Fetch() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Fetch() did not return after cancel")
	}
}

func fetchTestScope(t *testing.T, b *Backend, req backend.FetchRequest) backend.FetchResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := b.Fetch(ctx, req)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	return res
}

func messageSubjects(res backend.FetchResult) []string {
	var out []string
	for _, msg := range res.Messages {
		out = append(out, msg.Subject)
	}
	return out
}

func diagnosticRefs(diags []backend.Diagnostic) []string {
	var out []string
	for _, d := range diags {
		out = append(out, d.Ref)
	}
	return out
}

func decodeTestCursor(t *testing.T, scope string, cursor backend.Cursor) (uint32, uint32) {
	t.Helper()
	pos, err := decodeCursor(scope, cursor)
	if err != nil {
		t.Fatalf("decodeCursor() error = %v", err)
	}
	return pos.validity, pos.last
}

func rawTestMessage(id, subject string) []byte {
	return []byte("Message-ID: <" + id + "@example.test>\r\n" +
		"Date: Fri, 03 Jul 2026 09:00:00 +0000\r\n" +
		"From: Sender <sender@example.test>\r\n" +
		"To: Bob <bob@example.test>\r\n" +
		"Subject: " + subject + "\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Body of " + subject + ".\r\n")
}

func TestRefreshReportsFlagsAndDeletions(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{{Name: "INBOX", Delimiter: "/", UIDValidity: 1, Messages: []imaptest.MessageSeed{
		{UID: 1, Raw: rawTestMessage("one", "One")},
		{UID: 2, Raw: rawTestMessage("two", "Two")},
		{UID: 3, Raw: rawTestMessage("three", "Three")},
	}}}})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	scope := srv.ScopeKey("INBOX")
	res := fetchTestScope(t, b, backend.FetchRequest{ScopeKey: scope})
	if res.Messages[0].Ref != "1:1" {
		t.Fatalf("ref = %q", res.Messages[0].Ref)
	}
	srv.SetFlags("INBOX", 2, string(imap.FlagSeen), string(imap.FlagFlagged))
	srv.Expunge("INBOX", 3)
	srv.AppendMessage("INBOX", imaptest.MessageSeed{UID: 4, Raw: rawTestMessage("four", "Four")})
	refreshed, err := b.Refresh(context.Background(), backend.RefreshRequest{ScopeKey: scope, Cursor: res.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Unchanged || len(refreshed.Flags) != 2 || !refreshed.Flags["1:2"].Seen || !refreshed.Flags["1:2"].Flagged || refreshed.Flags["1:1"].Seen {
		t.Fatalf("refresh = %+v", refreshed)
	}
	if _, ok := refreshed.Flags["1:4"]; ok {
		t.Fatalf("refresh reported a message after the cursor")
	}
	// The refreshed cursor still points at the same last UID.
	if _, last := decodeTestCursor(t, scope, refreshed.Cursor); last != 3 {
		t.Fatalf("last = %d", last)
	}
	if strings.Contains(srv.DebugLog(), "STORE") {
		t.Fatal("refresh changed the mailbox")
	}
}
