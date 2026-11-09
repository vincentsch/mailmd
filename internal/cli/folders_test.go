package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/imap/imaptest"
)

// officeFolders is a mailbox like a small company's: three folders with
// mail, an empty Drafts, and Junk and Trash, which a sync leaves out.
func officeFolders() []imaptest.FolderSeed {
	message := func(uid uint32, subject string) imaptest.MessageSeed {
		return imaptest.MessageSeed{UID: uid, Raw: []byte("Message-ID: <" + subject + "@example.test>\r\nFrom: a@example.test\r\nTo: dana@example.test\r\nSubject: " + subject + "\r\nDate: Fri, 06 Nov 2026 09:00:00 +0000\r\n\r\nHello.\r\n")}
	}
	return []imaptest.FolderSeed{
		{Name: "INBOX", Delimiter: "/", UIDValidity: 1, Messages: []imaptest.MessageSeed{message(1, "one"), message(2, "two")}},
		{Name: "Sent", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrSent)}, UIDValidity: 2, Messages: []imaptest.MessageSeed{message(1, "sent")}},
		{Name: "Archive", Delimiter: "/", UIDValidity: 3, Messages: []imaptest.MessageSeed{message(1, "old")}},
		{Name: "Drafts", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrDrafts)}, UIDValidity: 4},
		{Name: "Junk", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrJunk)}, UIDValidity: 5, Messages: []imaptest.MessageSeed{message(1, "spam")}},
		{Name: "Trash", Delimiter: "/", UIDValidity: 6},
	}
}

func TestListFoldersShowsCountsAndWhatASyncCopies(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{Folders: officeFolders()})
	root, home := t.TempDir(), t.TempDir()
	opts := Options{ConfigRoot: root, HomeDir: home, RootCAs: server.CertPool()}
	if code, stdout, stderr := invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "work", "--password-stdin", "--exclude", "Archive")...); code != 0 {
		t.Fatalf("connect %d %s %s", code, stdout, stderr)
	}
	code, stdout, stderr := invoke(t, opts, "", "list", "folders")
	if code != 0 {
		t.Fatalf("list folders %d %s %s", code, stdout, stderr)
	}
	for _, want := range []string{
		"FOLDER   MESSAGES  SYNC",
		"Archive  1         no, left out by --exclude",
		"Drafts   0         yes",
		"INBOX    2         yes",
		"Junk     1         no, left out by default",
		"Trash    0         no, left out by default",
		"A sync copies 3 of these 6 folders.",
		"To copy Junk on every sync, run: mailmd connect imap --account work --include Junk",
		"To copy it once, run: mailmd sync work --folder Junk",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output is missing %q:\n%s", want, stdout)
		}
	}
	_, stdout, _ = invoke(t, opts, "", "list", "folders", "work", "--json")
	var list FolderList
	if err := json.Unmarshal([]byte(stdout), &list); err != nil || list.Schema != "mailmd-folder-list/1" || list.Included != 3 || len(list.Folders) != 6 {
		t.Fatalf("json %v:\n%s", err, stdout)
	}
	for _, f := range list.Folders {
		if f.Path == "Junk" && (f.Included || f.Reason != "left out by default" || f.Role != "junk" || f.Messages == nil || *f.Messages != 1) {
			t.Fatalf("Junk = %+v", f)
		}
	}
	// Listing reads only. There is no archive yet.
	if _, err := os.Stat(filepath.Join(home, "mail")); !os.IsNotExist(err) {
		t.Fatalf("list folders wrote to the archive: %v", err)
	}
}

func TestListFoldersNeedsANameWithSeveralAccounts(t *testing.T) {
	root := t.TempDir()
	registerMock(t, root, "one")
	service := mockService(2)
	// Spam has no fixed mail, so the mock can't count it.
	delete(service.Items, "spam")
	opts := mockOptions(root, service)
	code, stdout, stderr := invoke(t, opts, "", "list", "folders")
	if code != 0 || !strings.Contains(stdout, "INBOX   2         yes") || !strings.Contains(stdout, "Spam    -         no, left out by default") {
		t.Fatalf("one account %d %s %s", code, stdout, stderr)
	}
	registerMock(t, root, "two")
	code, _, stderr = invoke(t, opts, "", "list", "folders")
	if code != 1 || !strings.Contains(stderr, "Name the account") || !strings.Contains(stderr, "Connected accounts: one, two") {
		t.Fatalf("two accounts %d %s", code, stderr)
	}
	opts.Open = func(context.Context, accounts.AccountConfig, accounts.OpenOptions) (backend.Backend, error) {
		return nil, backend.ErrReconnectRequired
	}
	code, _, stderr = invoke(t, opts, "", "list", "folders", "two")
	if code != 3 || !strings.Contains(stderr, "The login of two is no longer valid.") || !strings.Contains(stderr, "--account two") {
		t.Fatalf("expired login %d %s", code, stderr)
	}
}

// The counts of connect, sync and status agree, and a dry run of one folder
// that a sync leaves out says how much new mail it has.
func TestFolderCountsAgreeAndADryRunCountsNewMail(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{Folders: officeFolders()})
	root, home := t.TempDir(), t.TempDir()
	opts := Options{ConfigRoot: root, HomeDir: home, RootCAs: server.CertPool()}
	code, stdout, stderr := invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "work", "--password-stdin")...)
	if code != 0 || !strings.Contains(stdout, "The mailbox has 6 folders. A sync copies 4 of them and leaves out Junk and Trash.") {
		t.Fatalf("connect %d %s %s", code, stdout, stderr)
	}
	if _, stdout, _ = invoke(t, opts, "", "sync"); !strings.Contains(stdout, filepath.FromSlash("mailmd checked 4 folders and saved 4 new messages to ~/mail/work")) {
		t.Fatalf("sync:\n%s", stdout)
	}
	_, stdout, _ = invoke(t, opts, "", "status", "work")
	if !strings.Contains(stdout, "Messages   4 in 3 folders") || !strings.Contains(stdout, "checked 4 folders, 4 new") {
		t.Fatalf("status:\n%s", stdout)
	}
	_, stdout, _ = invoke(t, opts, "", "sync", "work", "--folder", "Junk", "--dry-run")
	if !strings.Contains(stdout, filepath.FromSlash("mailmd checked Junk and would save 1 new message to ~/mail/work. Nothing was written because of --dry-run.")) {
		t.Fatalf("dry run of Junk:\n%s", stdout)
	}
	_, stdout, _ = invoke(t, opts, "", "sync", "work", "--folder", "Trash", "--dry-run")
	if !strings.Contains(stdout, "work has no new mail. mailmd checked Trash in") {
		t.Fatalf("dry run of Trash:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(home, "mail", "work", "Junk")); !os.IsNotExist(err) {
		t.Fatalf("the dry run wrote Junk: %v", err)
	}
	// --folder on sync is for that run only. The next sync leaves Junk out
	// again, and status still shows the last real sync.
	if _, stdout, _ = invoke(t, opts, "", "sync"); !strings.Contains(stdout, "work is up to date. mailmd checked 4 folders") {
		t.Fatalf("sync after the dry runs:\n%s", stdout)
	}
	if r, _ := accounts.Load(root); len(r.Accounts[0].Folders) != 0 {
		t.Fatalf("sync saved --folder: %+v", r.Accounts[0])
	}
}

// --include adds a folder to the default ones and keeps the other saved
// folder settings. --folder on connect means only these folders, and an
// empty value clears a saved list.
func TestIncludeAddsAFolderAndKeepsTheDefaults(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{Folders: officeFolders()})
	root, home := t.TempDir(), t.TempDir()
	opts := Options{ConfigRoot: root, HomeDir: home, RootCAs: server.CertPool()}
	if code, stdout, stderr := invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "work", "--password-stdin", "--exclude", "Archive")...); code != 0 {
		t.Fatalf("connect %d %s %s", code, stdout, stderr)
	}
	connect := func(args ...string) string {
		t.Helper()
		code, stdout, stderr := invoke(t, opts, "", append([]string{"connect", "imap", "--account", "work"}, args...)...)
		if code != 0 {
			t.Fatalf("connect %v: %d %s %s", args, code, stdout, stderr)
		}
		return stdout
	}
	saved := func() accounts.AccountConfig {
		r, _ := accounts.Load(root)
		return r.Accounts[0]
	}
	if out := connect("--include", "junk"); !strings.Contains(out, "A sync copies 4 of them and leaves out Archive and Trash.") {
		t.Fatalf("--include junk:\n%s", out)
	}
	if a := saved(); strings.Join(a.Include, ",") != "junk" || strings.Join(a.Exclude, ",") != "Archive" || len(a.Folders) != 0 {
		t.Fatalf("saved = %+v", a)
	}
	_, stdout, _ := invoke(t, opts, "", "list", "folders")
	if !strings.Contains(stdout, "Junk     1         yes") || !strings.Contains(stdout, "To copy Trash on every sync") {
		t.Fatalf("list folders:\n%s", stdout)
	}
	if _, stdout, _ = invoke(t, opts, "", "sync"); !strings.Contains(stdout, "mailmd checked 4 folders and saved 4 new messages") {
		t.Fatalf("sync:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(home, "mail", "work", "Junk")); err != nil {
		t.Fatalf("Junk was not copied: %v", err)
	}
	// A typo is not saved.
	code, _, stderr := invoke(t, opts, "", "connect", "imap", "--account", "work", "--include", "Junkk")
	if code != 1 || !strings.Contains(stderr, `--include "Junkk" matches no folder of work. Nothing was saved.`) {
		t.Fatalf("typo: %d %s", code, stderr)
	}
	// --folder means only these folders, and the included ones.
	if out := connect("--folder", "INBOX"); !strings.Contains(out, "A sync copies 2 of them and leaves out Archive, Drafts, Sent and Trash.") {
		t.Fatalf("--folder INBOX:\n%s", out)
	}
	// An empty value clears a saved list. Then a sync copies the default
	// folders again.
	if out := connect("--folder", "", "--include", "", "--exclude", ""); !strings.Contains(out, "A sync copies 4 of them and leaves out Junk and Trash.") {
		t.Fatalf("cleared:\n%s", out)
	}
	if a := saved(); len(a.Folders)+len(a.Include)+len(a.Exclude) != 0 {
		t.Fatalf("saved = %+v", a)
	}
	// '*' names every folder, also the ones left out by default.
	if out := connect("--folder", "*", "--exclude", "Trash"); !strings.Contains(out, "A sync copies 5 of them and leaves out Trash.") {
		t.Fatalf("'*':\n%s", out)
	}
}
