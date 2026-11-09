package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/auth"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/imap"
	"github.com/vincentsch/mailmd/internal/backend/mock"
	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/mailmd/internal/privatefs"
	"github.com/vincentsch/mailmd/internal/writer"
)

var testNow = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func testAccount() accounts.AccountConfig {
	return accounts.AccountConfig{Name: "work", Backend: "mock", ArchiveDir: "work", CredentialRef: "work/mock.json"}
}

func testMessage(i int, folder string) mail.Message {
	return mail.Message{
		MessageID: fmt.Sprintf("<%d@fixture.invalid>", i),
		Subject:   fmt.Sprintf("Message %d", i),
		BodyText:  "BODY_SENTINEL",
		Date:      time.Date(2026, 1, 1+i%28, 9, 0, 0, 0, time.UTC),
		From:      mail.Address{Name: "Sender", Email: "sender@fixture.invalid"},
		Folders:   []string{folder},
	}
}

// testService builds a mock provider with n messages in INBOX and one in
// each of the other given folders.
func testService(n int, folders ...backend.Folder) *mock.Service {
	s := &mock.Service{Items: map[string][]mock.Item{}}
	s.FolderRecords = append([]backend.Folder{{Path: "INBOX", ScopeKey: "inbox", Fetchable: true, Role: backend.RoleInbox}}, folders...)
	for i := 0; i < n; i++ {
		m := testMessage(i, "INBOX")
		s.Items["inbox"] = append(s.Items["inbox"], mock.Item{Message: &m, RawBytes: 100})
	}
	for j, f := range folders {
		m := testMessage(1000+j, f.Path)
		s.Items[f.ScopeKey] = append(s.Items[f.ScopeKey], mock.Item{Message: &m, RawBytes: 100})
	}
	return s
}

func testOptions(root string, s *mock.Service) Options {
	return Options{
		Account:         testAccount(),
		Root:            root,
		MaxMessageBytes: 1000,
		MaxBatchBytes:   5000,
		BatchSize:       4,
		Open:            func(ctx context.Context) (backend.Backend, error) { return s.Open(ctx) },
		Clock:           func() time.Time { return testNow },
		Random:          func() float64 { return 0.5 },
		Sleep:           func(context.Context, time.Duration) error { return nil },
	}
}

func mustRun(t *testing.T, opts Options) Result {
	t.Helper()
	r, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return r
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFirstIncrementalAndUnchangedSync(t *testing.T) {
	root := t.TempDir()
	s := testService(10)
	opts := testOptions(root, s)
	r := mustRun(t, opts)
	if r.New != 10 || r.Outcome != OutcomeCompleted || r.Folders[0].New != 10 {
		t.Fatalf("first sync = %+v", r)
	}
	dir := filepath.Join(root, "work")
	index := readFile(t, filepath.Join(dir, IndexFile))
	if strings.Count(index, "\n") != 10 || !strings.Contains(index, `"folder":"INBOX"`) {
		t.Fatalf("index = %s", index)
	}
	stateBefore, _ := os.Stat(filepath.Join(dir, ".mailmd", "state.json"))
	indexBefore, _ := os.Stat(filepath.Join(dir, IndexFile))

	// Nothing new, so no file in the archive changes and git shows nothing.
	time.Sleep(10 * time.Millisecond)
	r = mustRun(t, opts)
	if r.New != 0 || r.Unchanged != 0 {
		t.Fatalf("second sync = %+v", r)
	}
	stateAfter, _ := os.Stat(filepath.Join(dir, ".mailmd", "state.json"))
	indexAfter, _ := os.Stat(filepath.Join(dir, IndexFile))
	if !stateAfter.ModTime().Equal(stateBefore.ModTime()) || !indexAfter.ModTime().Equal(indexBefore.ModTime()) {
		t.Fatalf("an unchanged sync rewrote the state or index")
	}

	m := testMessage(50, "INBOX")
	s.Items["inbox"] = append(s.Items["inbox"], mock.Item{Message: &m, RawBytes: 100})
	r = mustRun(t, opts)
	if r.New != 1 {
		t.Fatalf("incremental sync = %+v", r)
	}
	if lines := strings.Count(readFile(t, filepath.Join(dir, IndexFile)), "\n"); lines != 11 {
		t.Fatalf("index has %d lines, want 11", lines)
	}
	assertNoContent(t, r)
}

func assertNoContent(t *testing.T, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if strings.Contains(string(data), "BODY_SENTINEL") {
		t.Fatalf("result leaked message content: %s", data)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	opts := testOptions(root, testService(5))
	opts.DryRun = true
	r := mustRun(t, opts)
	if r.New != 5 || !r.DryRun {
		t.Fatalf("dry run = %+v", r)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("dry run wrote %v", entries)
	}
}

func TestInterruptedSyncKeepsWorkAndContinues(t *testing.T) {
	root := t.TempDir()
	s := testService(10)
	opts := testOptions(root, s)
	ctx, cancel := context.WithCancel(context.Background())
	opts.Progress = func(p Progress) {
		if p.Done >= 4 {
			cancel()
		}
	}
	r, err := Run(ctx, opts)
	if r.Outcome != OutcomeInterrupted || Category(err) != CategoryCanceled || r.New != 4 {
		t.Fatalf("interrupted run = %+v, %v", r, err)
	}
	opts.Progress = nil
	r = mustRun(t, opts)
	if r.New != 6 || r.Unchanged != 0 {
		t.Fatalf("resumed run = %+v", r)
	}
}

func TestRetriesTransientFailures(t *testing.T) {
	root := t.TempDir()
	s := testService(3)
	s.OpenErrors = []error{backend.Retryable(errors.New("down"), 0)}
	s.FetchErrors = []error{backend.Retryable(errors.New("reset"), 0)}
	r := mustRun(t, testOptions(root, s))
	if r.New != 3 || r.Retries != 2 {
		t.Fatalf("result = %+v", r)
	}

	s = testService(3)
	s.OpenErrors = []error{backend.Retryable(errors.New("1"), 0), backend.Retryable(errors.New("2"), 0), backend.Retryable(errors.New("3"), 0), backend.Retryable(errors.New("4"), 0), backend.Retryable(errors.New("5"), 0)}
	_, err := Run(context.Background(), testOptions(t.TempDir(), s))
	if Category(err) != CategoryNetwork || !errors.Is(err, ErrRetryExhausted) {
		t.Fatalf("after five failures: %v", err)
	}

	s = testService(3)
	s.OpenErrors = []error{fmt.Errorf("imap: login: %w", imap.ErrAuth)}
	_, err = Run(context.Background(), testOptions(t.TempDir(), s))
	var runErr *RunError
	if Category(err) != CategoryAuth || !errors.As(err, &runErr) || !strings.Contains(runErr.Info.Hint, "mailmd connect mock --account work") {
		t.Fatalf("login failure: %v %+v", err, runErr)
	}
}

func TestOneFailingFolderDoesNotStopTheOthers(t *testing.T) {
	root := t.TempDir()
	s := testService(2, backend.Folder{Path: "Locked", ScopeKey: "locked", Fetchable: true}, backend.Folder{Path: "Work", ScopeKey: "work", Fetchable: true})
	opts := testOptions(root, s)
	open := opts.Open
	opts.Open = func(ctx context.Context) (backend.Backend, error) {
		b, err := open(ctx)
		return &lockedFolder{Backend: b}, err
	}
	r := mustRun(t, opts)
	if r.Outcome != OutcomeFolderErrors || r.New != 3 {
		t.Fatalf("result = %+v", r)
	}
	for _, f := range r.Folders {
		if f.Path == "Locked" && (f.Status != "failed" || !strings.Contains(f.Reason, "did not allow")) {
			t.Fatalf("Locked = %+v", f)
		}
	}
}

type lockedFolder struct{ backend.Backend }

func (b *lockedFolder) Fetch(ctx context.Context, req backend.FetchRequest) (backend.FetchResult, error) {
	if req.ScopeKey == "locked" {
		return backend.FetchResult{}, fmt.Errorf("imap: open Locked: %w", backend.ErrPermissionDenied)
	}
	return b.Backend.Fetch(ctx, req)
}

func TestMissingFolderKeepsFilesAndCursor(t *testing.T) {
	root := t.TempDir()
	s := testService(1, backend.Folder{Path: "Old", ScopeKey: "old", Fetchable: true})
	opts := testOptions(root, s)
	mustRun(t, opts)
	s.FolderRecords = s.FolderRecords[:1]
	r := mustRun(t, opts)
	if r.Outcome != OutcomeWarnings {
		t.Fatalf("outcome = %s", r.Outcome)
	}
	state, _, _ := LoadState(filepath.Join(root, "work"), "mock")
	if f := state.folder("old"); f == nil || !f.Missing || f.Cursor == "" {
		t.Fatalf("state = %+v", state)
	}
	status, err := Status(root, testAccount())
	if err != nil || status.Messages != 2 || !reflect.DeepEqual(status.Missing, []string{"Old"}) {
		t.Fatalf("status = %+v, %v", status, err)
	}
	// The next sync doesn't warn again, but status still lists the folder.
	if r = mustRun(t, opts); r.Outcome != OutcomeCompleted || statuses(r)["Old"] != "" {
		t.Fatalf("third sync = %+v", r)
	}
	if status, _ = Status(root, testAccount()); !reflect.DeepEqual(status.Missing, []string{"Old"}) {
		t.Fatalf("status = %+v", status)
	}
}

// gmailLike builds a mock account where one scope holds every label, like
// the Gmail API.
func gmailLike(labels ...string) *mock.Service {
	s := &mock.Service{Items: map[string][]mock.Item{}}
	for _, path := range labels {
		role := backend.RoleNone
		if path == "Archive" {
			role = backend.RoleArchive
		}
		s.FolderRecords = append(s.FolderRecords, backend.Folder{Path: path, ScopeKey: "account", Fetchable: true, Role: role})
	}
	return s
}

func TestChangedFilterFetchesASharedScopeAgain(t *testing.T) {
	s := gmailLike("Archive", "Inbox", "Work")
	for i, labels := range [][]string{{"Inbox"}, {"Work"}, nil} {
		m := testMessage(i, "Archive")
		m.Ref, m.Labels = fmt.Sprint("m", i), labels
		if len(labels) > 0 {
			m.Folders = labels[:1]
		}
		s.Items["account"] = append(s.Items["account"], mock.Item{Message: &m, RawBytes: 10, PreserveFolders: true})
	}
	root := t.TempDir()
	opts := testOptions(root, s)
	opts.Filter = Filter{Folders: []string{"Work"}}
	// Mail without labels is also filtered.
	if r := mustRun(t, opts); r.New != 1 || r.Excluded != 2 {
		t.Fatalf("--folder Work = %+v", r)
	}
	// Without the filter, the messages it left out are fetched now.
	opts.Filter = Filter{}
	if r := mustRun(t, opts); r.New != 2 || r.Unchanged != 1 {
		t.Fatalf("without a filter = %+v", r)
	}
	if r := mustRun(t, opts); r.New != 0 || r.Unchanged != 0 {
		t.Fatalf("again = %+v", r)
	}
}

func TestDeletionIsMatchedInItsOwnFolder(t *testing.T) {
	// In Outlook, a message's ID stays the same when it moves. So the old
	// folder reports a deletion of an ID that the new folder also has.
	s := testService(0, backend.Folder{Path: "Archive", ScopeKey: "archive", Fetchable: true})
	moved := testMessage(7, "INBOX")
	moved.Ref = "AAMk7"
	s.Items["inbox"] = []mock.Item{{Message: &moved, RawBytes: 10}}
	s.Items["archive"][0].Message.Ref = "AAMk7"
	*s.Items["archive"][0].Message = moved
	root := t.TempDir()
	opts := testOptions(root, s)
	if r := mustRun(t, opts); r.New != 2 {
		t.Fatalf("first sync = %+v", r)
	}
	s.Items["inbox"] = append(s.Items["inbox"], mock.Item{Diagnostic: &backend.Diagnostic{Ref: "AAMk7", Reason: backend.ReasonRemoteRemoved}})
	if r := mustRun(t, opts); r.Deleted != 1 {
		t.Fatalf("second sync = %+v", r)
	}
	entries, _, _ := readIndex(filepath.Join(root, "work"))
	for _, e := range entries {
		if e.Gone != (e.Folder == "INBOX") {
			t.Fatalf("entry %+v", e)
		}
		text := readFile(t, filepath.Join(root, "work", filepath.FromSlash(e.Path)))
		if strings.Contains(text, `deleted_on_server: "2026-10-13"`) != e.Gone {
			t.Fatalf("file:\n%s", text)
		}
	}
}

func TestDefaultExclusionsAndFolderFilters(t *testing.T) {
	folders := []backend.Folder{
		{Path: "Spam", ScopeKey: "spam", Fetchable: true, Role: backend.RoleJunk},
		{Path: "Trash", ScopeKey: "trash", Fetchable: true, Role: backend.RoleTrash},
		{Path: "Virtual/All", ScopeKey: "all", Fetchable: true, Role: backend.RoleAll},
		{Path: "Work", ScopeKey: "work", Fetchable: true},
		{Path: "Work/Clients", ScopeKey: "clients", Fetchable: true},
	}
	r := mustRun(t, testOptions(t.TempDir(), testService(1, folders...)))
	if got := statuses(r); got["Spam"] != "excluded" || got["Trash"] != "excluded" || got["Virtual/All"] != "excluded" || got["Work/Clients"] != "synced" {
		t.Fatalf("default = %v", got)
	}
	opts := testOptions(t.TempDir(), testService(1, folders...))
	opts.Filter = Filter{Exclude: []string{"work"}}
	if got := statuses(mustRun(t, opts)); got["Work"] != "excluded" || got["Work/Clients"] != "excluded" || got["INBOX"] != "synced" {
		t.Fatalf("--exclude work = %v", got)
	}
	opts = testOptions(t.TempDir(), testService(1, folders...))
	opts.Filter = Filter{Folders: []string{"spam", "Work/*"}}
	if got := statuses(mustRun(t, opts)); got["Spam"] != "synced" || got["Work"] != "excluded" || got["Work/Clients"] != "synced" || got["INBOX"] != "excluded" {
		t.Fatalf("--folder spam --folder 'Work/*' = %v", got)
	}
	// --include adds a folder to the default ones, and --exclude wins.
	opts = testOptions(t.TempDir(), testService(1, folders...))
	opts.Filter = Filter{Include: []string{"spam"}, Exclude: []string{"Work/*"}}
	if got := statuses(mustRun(t, opts)); got["Spam"] != "synced" || got["Trash"] != "excluded" || got["INBOX"] != "synced" || got["Work"] != "synced" || got["Work/Clients"] != "excluded" || got["Virtual/All"] != "excluded" {
		t.Fatalf("--include spam --exclude 'Work/*' = %v", got)
	}
	// With --folder, a sync copies the named folders and the included ones.
	opts = testOptions(t.TempDir(), testService(1, folders...))
	opts.Filter = Filter{Folders: []string{"Work"}, Include: []string{"Trash"}}
	if got := statuses(mustRun(t, opts)); got["Work"] != "synced" || got["Work/Clients"] != "synced" || got["Trash"] != "synced" || got["INBOX"] != "excluded" || got["Spam"] != "excluded" {
		t.Fatalf("--folder Work --include Trash = %v", got)
	}
	// '*' names every folder, so the default exclusions don't apply.
	opts = testOptions(t.TempDir(), testService(1, folders...))
	opts.Filter = Filter{Folders: []string{"*"}, Exclude: []string{"trash"}}
	if got := statuses(mustRun(t, opts)); got["Spam"] != "synced" || got["Virtual/All"] != "synced" || got["Work/Clients"] != "synced" || got["INBOX"] != "synced" || got["Trash"] != "excluded" {
		t.Fatalf("--folder '*' --exclude trash = %v", got)
	}
}

func TestIncludeKeepsSpamOfAMessageWithLabels(t *testing.T) {
	roles := map[string]backend.Role{"INBOX": backend.RoleInbox, "Spam": backend.RoleJunk, "Trash": backend.RoleTrash}
	if f := (Filter{}); f.KeepMessage([]string{"Spam"}, roles) || !f.SkipsJunk(roles) {
		t.Fatal("the default filter keeps spam")
	}
	if f := (Filter{Include: []string{"spam"}}); !f.KeepMessage([]string{"Spam"}, roles) || f.KeepMessage([]string{"Trash"}, roles) || !f.KeepMessage([]string{"INBOX"}, roles) || f.SkipsJunk(roles) {
		t.Fatal("--include spam doesn't keep spam, or keeps trash")
	}
	if f := (Filter{Include: []string{"INBOX"}}); !f.SkipsJunk(roles) {
		t.Fatal("--include INBOX asks the provider for spam")
	}
	if f := (Filter{Folders: []string{"INBOX"}, Include: []string{"Spam"}}); !f.KeepMessage([]string{"Spam"}, roles) || f.KeepMessage([]string{"Trash"}, roles) {
		t.Fatal("--folder INBOX --include Spam")
	}
	// A changed --include starts a shared scope over, like other filters.
	if (Filter{}).fingerprint() == (Filter{Include: []string{"Spam"}}).fingerprint() {
		t.Fatal("--include doesn't change the fingerprint")
	}
	err := Filter{Include: []string{"Junkk"}}.CheckMatches([]backend.Folder{{Path: "INBOX", Fetchable: true}})
	if info := Describe(err, testAccount()); info.Message != `--include "Junkk" matches no folder of work.` {
		t.Fatalf("a typo in --include: %+v", info)
	}
}

func statuses(r Result) map[string]string {
	out := map[string]string{}
	for _, f := range r.Folders {
		out[f.Path] = f.Status
	}
	return out
}

func TestGmailOverIMAPSyncsAllMailOnly(t *testing.T) {
	folders := []backend.Folder{
		{Path: "[Gmail]/All Mail", ScopeKey: "all", Fetchable: true, Role: backend.RoleAll},
		{Path: "[Gmail]/Sent Mail", ScopeKey: "sent", Fetchable: true, Role: backend.RoleSent},
		{Path: "[Gmail]/Spam", ScopeKey: "spam", Fetchable: true, Role: backend.RoleJunk},
		{Path: "Work", ScopeKey: "work", Fetchable: true},
	}
	r := mustRun(t, testOptions(t.TempDir(), testService(1, folders...)))
	got := statuses(r)
	if got["[Gmail]/All Mail"] != "synced" || got["INBOX"] != "excluded" || got["[Gmail]/Sent Mail"] != "excluded" || got["Work"] != "excluded" || got["[Gmail]/Spam"] != "excluded" {
		t.Fatalf("default = %v", got)
	}
	if r.New != 1 {
		t.Fatalf("new = %d, want only the All Mail message", r.New)
	}
	opts := testOptions(t.TempDir(), testService(1, folders...))
	opts.Filter = Filter{Folders: []string{"INBOX", "Work"}}
	if got := statuses(mustRun(t, opts)); got["[Gmail]/All Mail"] != "excluded" || got["INBOX"] != "synced" || got["Work"] != "synced" {
		t.Fatalf("--folder INBOX --folder Work = %v", got)
	}
}

func TestFolderPatternThatMatchesNothingIsAnError(t *testing.T) {
	opts := testOptions(t.TempDir(), testService(1, backend.Folder{Path: "Sent", ScopeKey: "sent", Fetchable: true}))
	opts.Filter = Filter{Folders: []string{"INBOX", "Inbx"}}
	_, err := Run(context.Background(), opts)
	var unknown *UnknownFolderError
	if !errors.As(err, &unknown) || unknown.Pattern != "Inbx" || !reflect.DeepEqual(unknown.Folders, []string{"INBOX", "Sent"}) {
		t.Fatalf("Run() = %v", err)
	}
	info := Describe(err, testAccount())
	if info.Category != CategoryUnknownFolder || !strings.Contains(info.Message, `"Inbx"`) || !strings.Contains(info.Hint, "INBOX") {
		t.Fatalf("info = %+v", info)
	}
}

func TestMessagesWithSeveralFoldersAreFiltered(t *testing.T) {
	s := &mock.Service{Items: map[string][]mock.Item{}}
	for _, path := range []string{"INBOX", "Promotions", "Spam"} {
		role := backend.RoleNone
		if path == "Spam" {
			role = backend.RoleJunk
		}
		s.FolderRecords = append(s.FolderRecords, backend.Folder{Path: path, ScopeKey: "account", Fetchable: true, Role: role})
	}
	for i, labels := range [][]string{{"INBOX"}, {"INBOX", "Promotions"}, {"Spam", "INBOX"}} {
		m := testMessage(i, "")
		m.Folders = labels
		s.Items["account"] = append(s.Items["account"], mock.Item{Message: &m, RawBytes: 10, PreserveFolders: true})
	}
	opts := testOptions(t.TempDir(), s)
	opts.Filter = Filter{Exclude: []string{"Promotions"}}
	r := mustRun(t, opts)
	if r.New != 1 || r.Excluded != 2 || r.Folders[0].Path != "All mail" {
		t.Fatalf("result = %+v", r)
	}
}

func TestRelabeledMessageStaysInItsFolder(t *testing.T) {
	s := &mock.Service{Items: map[string][]mock.Item{}}
	for _, path := range []string{"Inbox", "Work"} {
		s.FolderRecords = append(s.FolderRecords, backend.Folder{Path: path, ScopeKey: "account", Fetchable: true})
	}
	m := testMessage(1, "Inbox")
	m.Ref, m.Labels = "m1", []string{"Inbox", "Work"}
	s.Items["account"] = append(s.Items["account"], mock.Item{Message: &m, RawBytes: 10, PreserveFolders: true})
	root := t.TempDir()
	opts := testOptions(root, s)
	if r := mustRun(t, opts); r.New != 1 {
		t.Fatalf("first sync: %+v", r)
	}
	// The message was archived. It lost the Inbox label and was read.
	archived := m
	archived.Folders, archived.Labels, archived.Flags = []string{"Work"}, []string{"Work"}, mail.Flags{Seen: true}
	s.Items["account"] = append(s.Items["account"], mock.Item{Message: &archived, RawBytes: 10, PreserveFolders: true})
	if r := mustRun(t, opts); r.New != 0 || r.Updated != 1 {
		t.Fatalf("second sync: %+v", r)
	}
	var files []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if len(files) != 1 || !strings.Contains(files[0], string(filepath.Separator)+"Inbox"+string(filepath.Separator)) {
		t.Fatalf("files = %v", files)
	}
	if text := readFile(t, files[0]); !strings.Contains(text, `labels: ["Work"]`) || !strings.Contains(text, `flags: ["seen"]`) {
		t.Fatalf("file was not updated:\n%s", text)
	}
}

func TestArchiveRootCanBeASymbolicLink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "disk")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "mail")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symbolic links are not available:", err)
	}
	r := mustRun(t, testOptions(link, testService(2)))
	if r.New != 2 || r.Archive != filepath.Join(link, "work") {
		t.Fatalf("result = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(real, "work", "index.jsonl")); err != nil {
		t.Fatal(err)
	}
	if s, err := Status(link, testAccount()); err != nil || s.Messages != 2 {
		t.Fatalf("status = %+v, %v", s, err)
	}
}

func TestWiderSinceStartsTheFolderOver(t *testing.T) {
	root := t.TempDir()
	s := testService(20)
	opts := testOptions(root, s)
	opts.Since = time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	r := mustRun(t, opts)
	if r.New != 6 {
		t.Fatalf("sync since Jan 15 = %+v", r)
	}
	// A later --since doesn't change the saved position.
	opts.Since = time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
	if r = mustRun(t, opts); r.New != 0 {
		t.Fatalf("narrower since = %+v", r)
	}
	// Without --since the sync wants all mail. The folder starts over, and
	// only the older messages are new.
	opts.Since = time.Time{}
	if r = mustRun(t, opts); r.New != 14 || r.Unchanged != 6 {
		t.Fatalf("all mail = %+v", r)
	}
}

func TestTooLargeMessageIsSavedWithHeadersOnly(t *testing.T) {
	root := t.TempDir()
	s := testService(3)
	s.Items["inbox"][1].RawBytes = 5000
	r := mustRun(t, testOptions(root, s))
	if r.New != 3 || r.Diagnostics[backend.ReasonTooLarge] != 1 || r.Outcome != OutcomeWarnings {
		t.Fatalf("result = %+v", r)
	}
	status, _ := Status(root, testAccount())
	if len(status.Problems) != 1 || status.Problems[0].Reason != backend.ReasonTooLarge {
		t.Fatalf("status = %+v", status)
	}
}

func TestSecondSyncOfTheSameArchiveIsRefused(t *testing.T) {
	root := t.TempDir()
	unlock, err := lockArchive(filepath.Join(root, "work"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	_, err = Run(context.Background(), testOptions(root, testService(1)))
	if Category(err) != CategoryLocked {
		t.Fatalf("Run() = %v, want locked", err)
	}
}

// .mailmd/lock stays after a sync. Only a running sync holds the lock, so
// the file left behind doesn't stop the next sync.
func TestTheLockFileStaysAndDoesNotBlockTheNextSync(t *testing.T) {
	root := t.TempDir()
	s := testService(1)
	mustRun(t, testOptions(root, s))
	if _, err := os.Stat(filepath.Join(root, "work", ".mailmd", "lock")); err != nil {
		t.Fatalf("lock file after a sync: %v", err)
	}
	if r := mustRun(t, testOptions(root, s)); r.New != 0 {
		t.Fatalf("second sync = %+v", r)
	}
}

func TestWriteFailureKeepsTheCursor(t *testing.T) {
	root := t.TempDir()
	opts := testOptions(root, testService(6))
	calls := 0
	opts.Write = func(ctx context.Context, o writer.Options, m mail.Message) (writer.WriteResult, error) {
		calls++
		if calls == 6 {
			return writer.WriteResult{}, &writer.PathError{Op: "write", Path: "/archive/x.md", Err: errors.New("no space left on device")}
		}
		return writer.WriteMessage(ctx, o, m)
	}
	r, err := Run(context.Background(), opts)
	var runErr *RunError
	if Category(err) != CategoryLocal || !errors.As(err, &runErr) || !strings.Contains(runErr.Info.Message, "no space left on device") {
		t.Fatalf("Run() = %v", err)
	}
	if r.New != 5 {
		t.Fatalf("new = %d", r.New)
	}
	// The second batch failed, so its position was not saved. The next run
	// fetches messages 5 and 6 again.
	opts.Write = nil
	if r = mustRun(t, opts); r.New != 1 || r.Unchanged != 1 {
		t.Fatalf("rerun = %+v", r)
	}
}

func TestLostIndexIsRebuilt(t *testing.T) {
	root := t.TempDir()
	opts := testOptions(root, testService(5))
	mustRun(t, opts)
	index := filepath.Join(root, "work", IndexFile)
	want := readFile(t, index)
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	mustRun(t, opts)
	if got := readFile(t, index); got != want {
		t.Fatalf("rebuilt index differs:\n%s\nwant:\n%s", got, want)
	}
}

func TestDamagedStateIsReported(t *testing.T) {
	root := t.TempDir()
	opts := testOptions(root, testService(1))
	mustRun(t, opts)
	if err := os.WriteFile(filepath.Join(root, "work", ".mailmd", "state.json"), []byte(`{"schema":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), opts)
	if Category(err) != CategoryState {
		t.Fatalf("Run() = %v", err)
	}
}

// refreshing wraps the mock provider with flags it reports on refresh.
type refreshing struct {
	backend.Backend
	flags map[string]mail.Flags
}

func (b refreshing) Refresh(ctx context.Context, req backend.RefreshRequest) (backend.RefreshResult, error) {
	return backend.RefreshResult{Flags: b.flags}, nil
}

func TestFlagChangesAndDeletionsOnTheServer(t *testing.T) {
	root := t.TempDir()
	s := testService(3)
	for i := range s.Items["inbox"] {
		s.Items["inbox"][i].Message.Ref = fmt.Sprint(i + 1)
	}
	opts := testOptions(root, s)
	flags := map[string]mail.Flags{"1": {}, "2": {}, "3": {}}
	open := opts.Open
	opts.Open = func(ctx context.Context) (backend.Backend, error) {
		b, err := open(ctx)
		return refreshing{Backend: b, flags: flags}, err
	}
	mustRun(t, opts)
	// Message 2 was read on another device and message 3 was deleted.
	flags["2"] = mail.Flags{Seen: true}
	delete(flags, "3")
	r := mustRun(t, opts)
	if r.Updated != 1 || r.Deleted != 1 || r.New != 0 {
		t.Fatalf("result = %+v", r)
	}
	index := readFile(t, filepath.Join(root, "work", IndexFile))
	if !strings.Contains(index, `"flags":["seen"]`) || !strings.Contains(index, `"gone":true`) {
		t.Fatalf("index:\n%s", index)
	}
	var seen bool
	_ = filepath.WalkDir(filepath.Join(root, "work", "INBOX"), func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".md") && strings.Contains(readFile(t, path), "flags: [\"seen\"]") {
			seen = true
		}
		return nil
	})
	if !seen {
		t.Fatal("no file shows the new flags")
	}
	// Nothing changes on the next sync, and the deletion is not counted
	// twice.
	if r = mustRun(t, opts); r.Updated != 0 || r.Deleted != 0 {
		t.Fatalf("third sync = %+v", r)
	}
	status, _ := Status(root, testAccount())
	if status.Deleted != 1 {
		t.Fatalf("status = %+v", status)
	}
}

func TestAFileYouReplacedDoesNotStopTheSync(t *testing.T) {
	root := t.TempDir()
	s := testService(2)
	for i := range s.Items["inbox"] {
		s.Items["inbox"][i].Message.Ref = fmt.Sprint(i + 1)
	}
	opts := testOptions(root, s)
	flags := map[string]mail.Flags{"1": {}, "2": {}}
	open := opts.Open
	opts.Open = func(ctx context.Context) (backend.Backend, error) {
		b, err := open(ctx)
		return refreshing{Backend: b, flags: flags}, err
	}
	mustRun(t, opts)
	entries, _, _ := readIndex(filepath.Join(root, "work"))
	for _, e := range entries {
		path := filepath.Join(root, "work", filepath.FromSlash(e.Path))
		if e.Ref == "1" {
			if err := os.WriteFile(path, []byte("my own notes\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	flags["1"] = mail.Flags{Seen: true}
	delete(flags, "2")
	if r := mustRun(t, opts); r.Updated != 1 || r.Deleted != 1 {
		t.Fatalf("sync = %+v", r)
	}
	for _, e := range entries {
		if e.Ref == "1" && readFile(t, filepath.Join(root, "work", filepath.FromSlash(e.Path))) != "my own notes\n" {
			t.Fatal("your file was changed")
		}
	}
}

func TestReconnectHintNamesTheConnectCommand(t *testing.T) {
	gmail := Describe(backend.ErrReconnectRequired, accounts.AccountConfig{Name: "personal", Backend: "gmail"})
	if !strings.HasSuffix(gmail.Hint, "Run: mailmd connect gmail --account personal") || !strings.Contains(gmail.Hint, "7 days") {
		t.Errorf("gmail hint = %q", gmail.Hint)
	}
	outlook := Describe(backend.ErrReconnectRequired, accounts.AccountConfig{Name: "work", Backend: "graph"})
	if outlook.Hint != "Run: mailmd connect outlook --account work" {
		t.Errorf("outlook hint = %q", outlook.Hint)
	}
	imap := Describe(backend.ErrReconnectRequired, accounts.AccountConfig{Name: "home", Backend: "imap"})
	if imap.Hint != "Run: mailmd connect imap --account home" {
		t.Errorf("imap hint = %q", imap.Hint)
	}
}

func TestDamagedSavedLoginIsNamed(t *testing.T) {
	err := fmt.Errorf("%w: the login file has no refresh_token", auth.ErrOAuthSetup)
	info := Describe(err, accounts.AccountConfig{Name: "personal", Backend: "gmail"})
	if info.Message != "The saved login of personal can't be used: the login file has no refresh_token." || !strings.Contains(info.Hint, "mailmd connect gmail") {
		t.Fatalf("info = %+v", info)
	}
}

func TestFailedWriteNamesTheFolderAndTheCause(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "work", "INBOX")
	denied := func(path string, _ os.FileMode) error {
		return &fs.PathError{Op: "chmod", Path: path, Err: fs.ErrPermission}
	}
	err := privatefs.EnsureDir(root, dir, privatefs.Hooks{Chmod: denied})
	if !errors.Is(err, privatefs.ErrWrite) {
		t.Fatalf("err = %v", err)
	}
	info := Describe(err, testAccount())
	if info.Category != CategoryLocal || info.Message != "Cannot write "+filepath.Join(root, "work")+": permission denied." {
		t.Fatalf("info = %+v", info)
	}
}
