package imap

import (
	"context"
	"reflect"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/imap/imaptest"
)

func TestDisplayPathUsesSlashesAndCanonicalInbox(t *testing.T) {
	for _, delimiter := range []string{"/", ".", ""} {
		for _, name := range []string{"INBOX", "Inbox", "inbox", "iNbOx"} {
			if got := displayPath(name, delimiter); got != "INBOX" {
				t.Errorf("displayPath(%q, %q) = %q, want INBOX", name, delimiter, got)
			}
		}
	}
	for _, tt := range []struct{ name, delimiter, want string }{
		{"Inbox Archive", "/", "Inbox Archive"},
		{"Inbox/Children", "/", "Inbox/Children"},
		{"inbox.Children", ".", "inbox/Children"},
		{"Projects.Inbox", ".", "Projects/Inbox"},
		{"Inbox.Children", "", "Inbox.Children"},
		{"仕事", "/", "仕事"},
	} {
		if got := displayPath(tt.name, tt.delimiter); got != tt.want {
			t.Errorf("displayPath(%q, %q) = %q, want %q", tt.name, tt.delimiter, got, tt.want)
		}
	}
}

func TestFolderRolesFromAttributesAndNames(t *testing.T) {
	list := []*imap.ListData{
		{Mailbox: "INBOX", Delim: '/'},
		{Mailbox: "[Gmail]/All Mail", Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrAll, imap.MailboxAttrHasNoChildren}},
		{Mailbox: "[Gmail]/Spam", Delim: '/', Attrs: []imap.MailboxAttr{`\junk`}},
		{Mailbox: "[Gmail]/Bin", Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrTrash}},
		{Mailbox: "[Gmail]/Important", Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrImportant}},
		{Mailbox: "Junk E-mail", Delim: '/'},
		{Mailbox: "INBOX.Papierkorb", Delim: '.'},
		{Mailbox: "Work", Delim: '/'},
		{Mailbox: "[Gmail]", Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}},
	}
	want := map[string]backend.Role{
		"INBOX":             backend.RoleInbox,
		"[Gmail]/All Mail":  backend.RoleAll,
		"[Gmail]/Spam":      backend.RoleJunk,
		"[Gmail]/Bin":       backend.RoleTrash,
		"[Gmail]/Important": backend.RoleImportant,
		"Junk E-mail":       backend.RoleJunk,
		"INBOX/Papierkorb":  backend.RoleTrash,
		"Work":              backend.RoleNone,
		"[Gmail]":           backend.RoleNone,
	}
	for _, folder := range foldersFromList(list) {
		if folder.Role != want[folder.Path] {
			t.Errorf("%s role = %q, want %q", folder.Path, folder.Role, want[folder.Path])
		}
		if folder.Path == "[Gmail]" && (folder.Fetchable || folder.ScopeKey != "") {
			t.Errorf("[Gmail] should not be fetchable: %#v", folder)
		}
	}
}

func TestFoldersSeededListing(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{TLS: imaptest.TLSImplicit})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()

	got := listTestFolders(t, b)
	var withoutRoles []backend.Folder
	for _, folder := range got {
		folder.Role = ""
		withoutRoles = append(withoutRoles, folder)
	}
	var want []backend.Folder
	for _, folder := range imaptest.ExpectedFolders() {
		want = append(want, backend.Folder{Path: folder.Path, Fetchable: folder.Fetchable, ScopeKey: folder.ScopeKey})
	}
	if !reflect.DeepEqual(withoutRoles, want) {
		t.Fatalf("Folders() mismatch\n got: %#v\nwant: %#v", withoutRoles, want)
	}
	if f := requireFolder(t, got, "[Gmail]/All Mail", true); f.Role != backend.RoleAll {
		t.Fatalf("All Mail role = %q", f.Role)
	}
	requireFolder(t, got, "Отчёты", true)
	requireFolder(t, got, "[Gmail]", false)

	var collisions []backend.Folder
	for _, folder := range got {
		if folder.Path == "Foo/Bar" && folder.Fetchable {
			collisions = append(collisions, folder)
		}
	}
	if len(collisions) != 2 || collisions[0].ScopeKey == collisions[1].ScopeKey {
		t.Fatalf("Foo.Bar and Foo/Bar should be two folders with distinct keys: %#v", collisions)
	}
}

func requireFolder(t *testing.T, folders []backend.Folder, path string, fetchable bool) backend.Folder {
	t.Helper()
	for _, folder := range folders {
		if folder.Path == path && folder.Fetchable == fetchable {
			if fetchable && folder.ScopeKey == "" {
				t.Fatalf("%s fetchable ScopeKey is empty", path)
			}
			return folder
		}
	}
	t.Fatalf("folder %q fetchable=%v not found in %#v", path, fetchable, folders)
	return backend.Folder{}
}

func TestCountUsesStatusAndLeavesOutRefusedFolders(t *testing.T) {
	refused := &imap.Error{Type: imap.StatusResponseTypeNo, Text: "Permission denied"}
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{
		{Name: "INBOX", Delimiter: "/", UIDValidity: 1, Messages: []imaptest.MessageSeed{{UID: 1, Raw: []byte("Subject: a\r\n\r\na\r\n")}, {UID: 2, Raw: []byte("Subject: b\r\n\r\nb\r\n")}}},
		{Name: "Empty", Delimiter: "/", UIDValidity: 2},
		{Name: "Locked", Delimiter: "/", UIDValidity: 3, SelectError: refused},
		{Name: "Parent", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrNoSelect)}},
	}})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	counts, err := b.Count(context.Background(), listTestFolders(t, b))
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 2 || counts["INBOX"] != 2 || counts["Empty"] != 0 {
		t.Fatalf("counts = %v, want INBOX 2 and Empty 0 only", counts)
	}
	// The connection still works after the refused STATUS.
	if _, err := b.Folders(context.Background()); err != nil {
		t.Fatal(err)
	}
}
