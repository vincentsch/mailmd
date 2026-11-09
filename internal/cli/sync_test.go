package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/gmail"
	"github.com/vincentsch/mailmd/internal/backend/imap/imaptest"
	"github.com/vincentsch/mailmd/internal/backend/mock"
	"github.com/vincentsch/mailmd/internal/privatefs"
	mdsync "github.com/vincentsch/mailmd/internal/sync"
)

func TestSyncCountsTheFoldersItChecked(t *testing.T) {
	root := t.TempDir()
	registerMock(t, root, "one")
	service := mockService(3)
	service.FolderRecords = append(service.FolderRecords, backend.Folder{Path: "Archive", ScopeKey: "archive", Fetchable: true, Role: backend.RoleArchive})
	service.Items["archive"] = []mock.Item{}
	opts := mockOptions(root, service)
	opts.HomeDir = t.TempDir()
	// Archive is empty. Both syncs checked it, so both count it, and
	// status says that the archive's 3 messages are in 1 folder.
	if _, stdout, _ := invoke(t, opts, "", "sync"); !strings.Contains(stdout, "mailmd checked 2 folders and saved 3 new messages to") {
		t.Fatalf("sync:\n%s", stdout)
	}
	if _, stdout, _ := invoke(t, opts, "", "sync"); !strings.Contains(stdout, "one is up to date. mailmd checked 2 folders") {
		t.Fatalf("second sync:\n%s", stdout)
	}
	_, stdout, _ := invoke(t, opts, "", "status", "one")
	if !strings.Contains(stdout, "Messages   3 in 1 folder") || !strings.Contains(stdout, "Last sync  just now, completed, checked 2 folders") {
		t.Fatalf("status:\n%s", stdout)
	}
}

func TestSyncAllAccountsIntoTheDefaultRoot(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	registerMock(t, root, "one")
	registerMock(t, root, "two")
	opts := mockOptions(root, mockService(3))
	opts.HomeDir = home
	code, stdout, stderr := invoke(t, opts, "", "sync")
	if code != 0 {
		t.Fatalf("sync %d %s %s", code, stdout, stderr)
	}
	for _, want := range []string{"one: mailmd checked INBOX and saved 3 new messages to ~/mail/one in", "two: mailmd checked INBOX and saved 3 new messages"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output is missing %q:\n%s", want, stdout)
		}
	}
	sweep(t, stdout, stderr)
	for _, name := range []string{"one", "two"} {
		if _, err := os.Stat(filepath.Join(home, "mail", name, "index.jsonl")); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	code, stdout, _ = invoke(t, opts, "", "sync", "one")
	if code != 0 || !strings.Contains(stdout, "one is up to date. mailmd checked INBOX in") {
		t.Fatalf("second sync %d %s", code, stdout)
	}
	if code, stdout, _ = invoke(t, opts, "", "sync", "--quiet"); code != 0 || stdout != "" {
		t.Fatalf("a quiet sync with nothing to report printed %q", stdout)
	}
	_, stdout, _ = invoke(t, opts, "", "status")
	for _, want := range []string{"one (mock)", "Archive    ~/mail/one", "Messages   3 in 1 folder", "Last sync  just now, completed, checked INBOX"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status is missing %q:\n%s", want, stdout)
		}
	}
	code, stdout, _ = invoke(t, opts, "", "list", "accounts")
	if code != 0 || !strings.Contains(stdout, "NAME") || !strings.Contains(stdout, "~/mail/two") {
		t.Fatalf("list %d %s", code, stdout)
	}
}

func TestSyncJSONIsStable(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	registerMock(t, root, "work")
	code, stdout, stderr := invoke(t, mockOptions(root, mockService(2)), "", "sync", "work", "--out", out, "--json")
	if code != 0 {
		t.Fatalf("%d %s %s", code, stdout, stderr)
	}
	var result SyncOutput
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.Schema != "mailmd-sync/1" || len(result.Accounts) != 1 {
		t.Fatalf("%s %v", stdout, err)
	}
	r := result.Accounts[0]
	if r.New != 2 || r.Outcome != "completed" || r.Archive != filepath.Join(out, "work") || r.Error != nil {
		t.Fatalf("result %+v", r)
	}
	var folders []string
	for _, f := range r.Folders {
		folders = append(folders, f.Path+":"+f.Status)
	}
	if strings.Join(folders, ",") != "INBOX:synced,Spam:excluded" {
		t.Fatalf("folders %v", folders)
	}
	code, stdout, _ = invoke(t, mockOptions(root, mockService(2)), "", "status", "work", "--out", out, "--json")
	var status StatusOutput
	if err := json.Unmarshal([]byte(stdout), &status); code != 0 || err != nil || status.Accounts[0].Messages != 2 || status.Accounts[0].LastSync == nil {
		t.Fatalf("status %d %s %v", code, stdout, err)
	}
}

func TestSyncErrors(t *testing.T) {
	root := t.TempDir()
	opts := mockOptions(root, mockService(1))
	code, _, stderr := invoke(t, opts, "", "sync")
	if code != 5 || !strings.Contains(stderr, "No accounts are connected yet") || !strings.Contains(stderr, "mailmd connect imap") {
		t.Fatalf("no accounts: %d %s", code, stderr)
	}
	registerMock(t, root, "work")
	code, _, stderr = invoke(t, opts, "", "sync", "nope")
	if code != 5 || !strings.Contains(stderr, `There is no account named "nope"`) || !strings.Contains(stderr, "Connected accounts: work") {
		t.Fatalf("unknown account: %d %s", code, stderr)
	}
	for args, want := range map[string]string{
		"sync work --since tomorrow":      "--since \"tomorrow\"",
		"sync work --folder [":            "is not valid",
		"sync work --max-message-bytes 0": "--max-message-bytes",
	} {
		code, _, stderr := invoke(t, opts, "", strings.Fields(args)...)
		if code != 1 || !strings.Contains(stderr, want) {
			t.Errorf("%s: %d %s", args, code, stderr)
		}
	}
	// A provider that refuses the login is exit code 3 with a reconnect hint.
	opts.Open = func(context.Context, accounts.AccountConfig, accounts.OpenOptions) (backend.Backend, error) {
		return nil, backend.ErrReconnectRequired
	}
	code, stdout, stderr := invoke(t, opts, "", "sync", "work")
	if code != 3 || !strings.Contains(stderr, "The login of work is no longer valid.") || !strings.Contains(stderr, "mailmd connect mock --account work") {
		t.Fatalf("login: %d %s %s", code, stdout, stderr)
	}
}

func TestSyncEndToEndOverIMAP(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{})
	root, out := t.TempDir(), t.TempDir()
	opts := Options{ConfigRoot: root, RootCAs: server.CertPool()}
	if code, stdout, stderr := invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "work", "--password-stdin")...); code != 0 {
		t.Fatalf("connect %d %s %s", code, stdout, stderr)
	}
	// The test server has Gmail's All Mail, which a sync would use alone,
	// and two broken folders, Fault/...
	code, stdout, stderr := invoke(t, opts, "", "sync", "work", "--out", out, "--json", "--folder", "INBOX", "--folder", "Work")
	if code != 0 {
		t.Fatalf("sync %d %s %s", code, stdout, stderr)
	}
	var result SyncOutput
	_ = json.Unmarshal([]byte(stdout), &result)
	r := result.Accounts[0]
	if r.New == 0 || r.Outcome != "completed_with_warnings" || r.Diagnostics["parse_failure"] == 0 {
		t.Fatalf("result %+v", r)
	}
	server.AppendMessage("INBOX", imaptest.MessageSeed{UID: 30, Raw: []byte("Message-ID: <new@example.test>\r\nFrom: a@example.test\r\nSubject: New\r\nDate: Fri, 03 Jul 2026 09:00:00 +0000\r\n\r\nNew.\r\n")})
	// --folder chose the folders, so the summary names them.
	code, stdout, _ = invoke(t, opts, "", "sync", "work", "--out", out, "--folder", "INBOX", "--folder", "Work")
	if code != 0 || !strings.Contains(stdout, "mailmd checked INBOX, Work and Work/Projects and saved 1 new message to") {
		t.Fatalf("incremental %d %s", code, stdout)
	}
}

// tunnel forwards connections to the test server and can drop them, to
// test how sync recovers from a broken connection.
type tunnel struct {
	mu       sync.Mutex
	listener net.Listener
	conns    []net.Conn
	target   string
}

func startTunnel(t *testing.T, target string) *tunnel {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &tunnel{listener: l, target: target}
	go func() {
		for {
			in, err := l.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				in.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, in, up)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(up, in); up.Close() }()
			go func() { _, _ = io.Copy(in, up); in.Close() }()
		}
	}()
	t.Cleanup(func() { l.Close(); p.drop() })
	return p
}

func (p *tunnel) drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}

func TestSyncRecoversFromADroppedConnection(t *testing.T) {
	var seeds []imaptest.MessageSeed
	for i := 1; i <= 250; i++ {
		seeds = append(seeds, imaptest.MessageSeed{UID: uint32(i), Raw: []byte("Message-ID: <" + strconv.Itoa(i) + "@example.test>\r\nFrom: a@example.test\r\nSubject: M\r\nDate: Thu, 1 Jan 2026 00:00:00 +0000\r\n\r\nBODY_SENTINEL\r\n")})
	}
	server := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{{Name: "INBOX", Delimiter: "/", UIDValidity: 1, Messages: seeds}}})
	tun := startTunnel(t, server.Addr())
	_, port, _ := net.SplitHostPort(tun.listener.Addr().String())
	root, out := t.TempDir(), t.TempDir()
	opts := Options{ConfigRoot: root, RootCAs: server.CertPool(), Sleep: func(context.Context, time.Duration) error { return nil }}
	args := []string{"connect", "imap", "--account", "work", "--host", "127.0.0.1", "--port", port, "--username", server.Username(), "--password-stdin"}
	if code, stdout, stderr := invoke(t, opts, server.Password()+"\n", args...); code != 0 {
		t.Fatalf("connect %d %s %s", code, stdout, stderr)
	}
	dropped := false
	open := accounts.OpenBackend
	opts.Open = func(ctx context.Context, a accounts.AccountConfig, o accounts.OpenOptions) (backend.Backend, error) {
		b, err := open(ctx, a, o)
		if err != nil {
			return b, err
		}
		return &dropAfterFirstBatch{Backend: b, drop: func() {
			if !dropped {
				dropped = true
				tun.drop()
			}
		}}, nil
	}
	code, stdout, stderr := invoke(t, opts, "", "sync", "work", "--out", out, "--json")
	var result SyncOutput
	_ = json.Unmarshal([]byte(stdout), &result)
	if code != 0 || !dropped || result.Accounts[0].New != 250 || result.Accounts[0].Retries != 1 {
		t.Fatalf("%d dropped=%v %s %s", code, dropped, stdout, stderr)
	}
	sweep(t, stdout, stderr)
}

// dropAfterFirstBatch breaks the connection once, right after the first
// batch was fetched.
type dropAfterFirstBatch struct {
	backend.Backend
	drop    func()
	fetches int
}

func (b *dropAfterFirstBatch) Fetch(ctx context.Context, req backend.FetchRequest) (backend.FetchResult, error) {
	b.fetches++
	if b.fetches == 2 {
		b.drop()
	}
	return b.Backend.Fetch(ctx, req)
}

// The real program stops cleanly on Ctrl-C, and what it saved stays.
func TestInterruptedProcessKeepsProgress(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sends a Unix signal")
	}
	root, out := t.TempDir(), t.TempDir()
	registerMock(t, root, "interrupt")
	exe := filepath.Join(t.TempDir(), "mailmd-offline")
	build := exec.Command("go", "build", "-tags", "mailmd_mock", "-o", exe, "./cmd/mailmd")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := []string{"sync", "interrupt", "--config-root", root, "--out", out, "--json"}
	child := exec.CommandContext(ctx, exe, args...)
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	statePath := filepath.Join(out, "interrupt", ".mailmd", "state.json")
	for {
		if data, err := os.ReadFile(statePath); err == nil && strings.Contains(string(data), `"cursor"`) {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("exited early: %v %s %s", err, stdout.String(), stderr.String())
		case <-ctx.Done():
			t.Fatal("no cursor was saved")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := child.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("an interrupted sync exited with success")
	}
	var result SyncOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Accounts[0].Outcome != mdsync.OutcomeInterrupted {
		t.Fatalf("result %s %v", stdout.String(), err)
	}
	output, err := exec.CommandContext(ctx, exe, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("resume: %v %s", err, output)
	}
	sweep(t, stdout.String(), stderr.String(), string(output))
}

// A Gmail message with several labels is written once, in the folder of its
// most important label, with all labels in the frontmatter. Spam is left
// out.
func TestSyncGmailWritesEachMessageOnce(t *testing.T) {
	raw := func(id, subject string) string {
		return base64.RawURLEncoding.EncodeToString([]byte("Message-ID: <" + id + "@example.test>\r\nFrom: a@example.test\r\nSubject: " + subject + "\r\nDate: Fri, 03 Jul 2026 09:00:00 +0000\r\n\r\nBODY_SENTINEL\r\n"))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/profile"):
			io.WriteString(w, `{"emailAddress":"dana@example.test","historyId":"10"}`)
		case strings.HasSuffix(r.URL.Path, "/labels"):
			io.WriteString(w, `{"labels":[{"id":"INBOX","name":"INBOX","type":"system"},{"id":"SPAM","name":"SPAM","type":"system"},{"id":"IMPORTANT","name":"IMPORTANT","type":"system"},{"id":"Label_1","name":"Work","type":"user"}]}`)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			if r.URL.Query().Get("includeSpamTrash") == "true" {
				t.Errorf("spam was listed")
			}
			io.WriteString(w, `{"messages":[{"id":"m1"},{"id":"m2"}]}`)
		case strings.HasSuffix(r.URL.Path, "/messages/m1"):
			json.NewEncoder(w).Encode(map[string]any{"id": "m1", "threadId": "t1", "labelIds": []string{"INBOX", "Label_1", "IMPORTANT"}, "raw": raw("m1", "Hello")})
		case strings.HasSuffix(r.URL.Path, "/messages/m2"):
			json.NewEncoder(w).Encode(map[string]any{"id": "m2", "threadId": "t2", "labelIds": []string{"Label_1"}, "raw": raw("m2", "Archived")})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root, out := t.TempDir(), t.TempDir()
	_, err := accounts.Connect(context.Background(), root, "gmail", "gmail", func(ctx context.Context, stage string) (accounts.Settings, string, error) {
		return accounts.Settings{}, "", os.WriteFile(filepath.Join(root, "mailmd", stage, "gmail-token.json"), []byte(`{}`), 0o600)
	}, privatefs.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{ConfigRoot: root, Open: func(ctx context.Context, a accounts.AccountConfig, o accounts.OpenOptions) (backend.Backend, error) {
		return gmail.Open(ctx, gmail.Config{HTTPClient: &http.Client{Transport: rewriteTransport{base: server.URL, transport: http.DefaultTransport}}})
	}}
	code, stdout, stderr := invoke(t, opts, "", "sync", "gmail", "--out", out)
	if code != 0 || !strings.Contains(stdout, "mailmd checked all mail and saved 2 new messages to ") {
		t.Fatalf("%d %s %s", code, stdout, stderr)
	}
	var files []string
	_ = filepath.WalkDir(out, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".md") {
			rel, _ := filepath.Rel(filepath.Join(out, "gmail"), path)
			files = append(files, filepath.ToSlash(filepath.Dir(filepath.Dir(rel))))
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), "Hello") && !strings.Contains(string(data), `labels: ["Important", "Inbox", "Work"]`) {
				t.Errorf("labels missing:\n%s", data)
			}
		}
		return nil
	})
	sort.Strings(files)
	if strings.Join(files, ",") != "Inbox,Work" {
		t.Fatalf("files are in %v", files)
	}
}

func TestSeveralAccountsAreSeparatedByOneBlankLine(t *testing.T) {
	ok := func(name string) mdsync.Result {
		return mdsync.Result{Account: name, Archive: "/home/dana/mail/" + name, Outcome: mdsync.OutcomeCompleted}
	}
	failed := ok("broken")
	failed.Outcome, failed.Error = "failed", &mdsync.ErrorInfo{Message: "The login of broken is no longer valid."}
	var out bytes.Buffer
	renderSync(&out, SyncOutput{Accounts: []mdsync.Result{ok("one"), failed, ok("two")}}, "/home/dana", true, false)
	want := "one is up to date. mailmd checked 0 folders in less than a second.\n\ntwo is up to date. mailmd checked 0 folders in less than a second.\n"
	if out.String() != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

func TestGmailSummaryDoesNotCountItsOneScopeAsAFolder(t *testing.T) {
	r := mdsync.Result{Account: "personal", Archive: "/home/dana/mail/personal", Outcome: mdsync.OutcomeCompleted, New: 13, Seconds: 7,
		Folders: []mdsync.FolderResult{{Path: mdsync.AllMailPath, Status: "synced", New: 13}}}
	var out bytes.Buffer
	renderSyncResult(&out, r, "/home/dana", false)
	if want := "mailmd checked all mail and saved 13 new messages to ~/mail/personal in 7 s.\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
	r.New, r.Folders[0].New = 0, 0
	out.Reset()
	renderSyncResult(&out, r, "/home/dana", false)
	if !strings.HasPrefix(out.String(), "personal is up to date. mailmd checked all mail in ") {
		t.Fatalf("got %q", out.String())
	}
}

func TestDryRunWarningsDontSayAnythingWasSaved(t *testing.T) {
	r := mdsync.Result{Account: "work", DryRun: true, Diagnostics: map[backend.DiagnosticReason]int{backend.ReasonTooLarge: 1, backend.ReasonParseFailure: 2}}
	got := strings.Join(warnings(r), "\n")
	want := "1 message is larger than the size limit. A sync would save only the headers.\n" +
		"2 messages can't be read. A sync would save the headers it can read, and the original as an .eml file."
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	r.DryRun = false
	if got := strings.Join(warnings(r), "\n"); !strings.Contains(got, "mailmd saved only the headers") || !strings.Contains(got, "mailmd status work") {
		t.Fatalf("a real sync:\n%s", got)
	}
}
