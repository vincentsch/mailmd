package imap_test

import (
	"context"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
	imapbackend "github.com/vincentsch/mailmd/internal/backend/imap"
	"github.com/vincentsch/mailmd/internal/backend/imap/imaptest"
	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/mailmd/internal/writer"
)

func TestInboxListingFetchAndWriterPaths(t *testing.T) {
	for _, delimiter := range []string{"/", "."} {
		for _, name := range []string{"INBOX", "Inbox", "inbox", "iNbOx"} {
			t.Run(name+"/delimiter="+delimiter, func(t *testing.T) {
				inbox := imaptest.SeedFolders()[0]
				inbox.Name, inbox.Delimiter = name, delimiter
				archive := imaptest.SeedFolders()[9]
				archive.Name, archive.Delimiter = "Inbox Archive", delimiter
				srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{
					archive, inbox, {Name: "IA", Delimiter: delimiter},
				}})
				b := openWriterTestBackend(t, srv)
				defer b.Close()

				folders, err := b.Folders(context.Background())
				if err != nil {
					t.Fatalf("Folders() error = %v", err)
				}
				paths := make([]string, len(folders))
				for i, folder := range folders {
					paths[i] = folder.Path
				}
				if want := []string{"IA", "INBOX", "Inbox Archive"}; !reflect.DeepEqual(paths, want) {
					t.Errorf("listed paths = %#v, want sorted %#v", paths, want)
				}
				scope := srv.ScopeKey(name)
				if folders[1].ScopeKey != scope {
					t.Errorf("listed scope = %q, want %q", folders[1].ScopeKey, scope)
				}
				if delimiter == "/" && scope != "imap-v1:bWFpbG1kLWltYXAtc2NvcGUtdjEABUlOQk9YAQEv" {
					t.Fatalf("frozen INBOX scope changed: %q", scope)
				}
				res := fetchWriterTestScope(t, b, mdbackend.FetchRequest{ScopeKey: scope, Limit: 30})
				if again := fetchWriterTestScope(t, b, mdbackend.FetchRequest{ScopeKey: scope, Cursor: res.Cursor}); len(again.Messages) != 0 || !again.Done {
					t.Fatalf("fetching with the returned cursor gave %d messages again", len(again.Messages))
				}
				if len(res.Messages) == 0 {
					t.Fatal("INBOX fetch returned no messages")
				}
				for _, msg := range res.Messages {
					if !reflect.DeepEqual(msg.Folders, []string{"INBOX"}) {
						t.Fatalf("fetched folders = %#v, want INBOX", msg.Folders)
					}
				}
				msg := res.Messages[0]
				if msg.LocalID != inbox.Messages[0].Expected.LocalID {
					t.Fatalf("LocalID = %q, want frozen %q", msg.LocalID, inbox.Messages[0].Expected.LocalID)
				}
				written, err := writer.WriteMessage(context.Background(), writer.Options{Root: t.TempDir(), Account: "test"}, msg)
				if err != nil {
					t.Fatalf("WriteMessage() error = %v", err)
				}
				if len(written.Files) != 1 || !strings.HasPrefix(written.Files[0].RelPath, "test/INBOX/") {
					t.Fatalf("writer files = %#v, want test/INBOX/ destination", written.Files)
				}
				archiveRes := fetchWriterTestScope(t, b, mdbackend.FetchRequest{ScopeKey: srv.ScopeKey(archive.Name)})
				if len(archiveRes.Messages) != 1 || !reflect.DeepEqual(archiveRes.Messages[0].Folders, []string{"Inbox Archive"}) {
					t.Fatalf("unrelated mailbox folders changed: %#v", archiveRes.Messages)
				}
			})
		}
	}
}

func TestFetchedMessagesWriteIdempotently(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{TLS: imaptest.TLSImplicit})
	b := openWriterTestBackend(t, srv)
	defer b.Close()

	res := fetchWriterTestScope(t, b, mdbackend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX"), Limit: 30})
	root := t.TempDir()
	for _, msg := range res.Messages {
		first, err := writer.WriteMessage(context.Background(), writer.Options{Root: root, Account: "test"}, msg)
		if err != nil {
			t.Fatalf("first WriteMessage(%q) error = %v", msg.Subject, err)
		}
		if !first.Changed {
			t.Fatalf("first WriteMessage(%q) Changed = false, want true", msg.Subject)
		}
		second, err := writer.WriteMessage(context.Background(), writer.Options{Root: root, Account: "test"}, msg)
		if err != nil {
			t.Fatalf("second WriteMessage(%q) error = %v", msg.Subject, err)
		}
		if second.Changed {
			t.Fatalf("second WriteMessage(%q) Changed = true, want false", msg.Subject)
		}
	}
}

func TestDisplayCollisionWritesStableFinalBytes(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{TLS: imaptest.TLSImplicit})
	b := openWriterTestBackend(t, srv)
	defer b.Close()

	scopes := []string{srv.ScopeKey("Foo.Bar"), srv.ScopeKey("Foo/Bar")}
	sort.Strings(scopes)
	messages := []mail.Message{}
	for _, scope := range scopes {
		res := fetchWriterTestScope(t, b, mdbackend.FetchRequest{ScopeKey: scope})
		if len(res.Messages) != 1 {
			t.Fatalf("scope %q message count = %d, want 1", scope, len(res.Messages))
		}
		messages = append(messages, res.Messages[0])
	}
	if messages[0].LocalID != messages[1].LocalID {
		t.Fatalf("collision messages LocalID mismatch: %q vs %q", messages[0].LocalID, messages[1].LocalID)
	}

	root := t.TempDir()
	finalPath, firstBytes := writeCollisionRun(t, root, messages)
	_, secondBytes := writeCollisionRun(t, root, messages)
	if string(firstBytes) != string(secondBytes) {
		t.Fatalf("final collision bytes changed across repeated run at %s", finalPath)
	}
}

func writeCollisionRun(t *testing.T, root string, messages []mail.Message) (string, []byte) {
	t.Helper()
	var finalPath string
	for _, msg := range messages {
		res, err := writer.WriteMessage(context.Background(), writer.Options{Root: root, Account: "test"}, msg)
		if err != nil {
			t.Fatalf("WriteMessage(%q) error = %v", msg.Subject, err)
		}
		if len(res.Files) != 1 {
			t.Fatalf("WriteMessage(%q) file count = %d, want 1", msg.Subject, len(res.Files))
		}
		finalPath = res.Files[0].Path
	}
	// Both scopes give the same path and LocalID but different flags, so each
	// run writes the file again. That is a known limit. The final bytes are
	// still the same each time, because the same scope comes last.
	data, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read final collision file: %v", err)
	}
	return finalPath, data
}

func openWriterTestBackend(t *testing.T, srv *imaptest.Server) *imapbackend.Backend {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := imapbackend.Open(ctx, imapbackend.Config{
		Host:     srv.Host(),
		Port:     srv.Port(),
		Username: srv.Username(),
		Password: srv.Password(),
		TLSMode:  imapbackend.TLSImplicit,
		RootCAs:  srv.CertPool(),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return b
}

func fetchWriterTestScope(t *testing.T, b *imapbackend.Backend, req mdbackend.FetchRequest) mdbackend.FetchResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := b.Fetch(ctx, req)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	return res
}
