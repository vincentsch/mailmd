package writer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vincentsch/mailmd/internal/mail"
)

func TestPathCollisionAndOwnedUpdate(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{LocalID: plainLocalID, Subject: "Collision", BodyText: "body"}
	plan, err := PlanMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("PlanMessage: %v", err)
	}
	path := plan.Files[0].Path
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	foreign := strings.Replace(string(renderMarkdownBytes(msgWithLocalID(msg, otherLocalID), msg.Date, nil, "foreign", "")), "foreign", "foreign body", 1)
	if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
		t.Fatalf("write foreign: %v", err)
	}
	_, err = WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if !errors.Is(err, ErrPathCollision) {
		t.Fatalf("WriteMessage error = %v, want ErrPathCollision", err)
	}
	var perr *PathError
	if !errors.As(err, &perr) || perr.Op != "collision" || perr.Path != path {
		t.Fatalf("PathError = %#v, want collision at %s", perr, path)
	}
	if got := string(readFile(t, path)); got != foreign {
		t.Fatal("foreign file was overwritten")
	}

	owned := string(renderMarkdownBytes(msg, msg.Date, nil, "old body", ""))
	if err := os.WriteFile(path, []byte(owned), 0o644); err != nil {
		t.Fatalf("write owned: %v", err)
	}
	result, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("WriteMessage owned: %v", err)
	}
	if got := result.Files[0].Status; got != WriteUpdated {
		t.Fatalf("owned status = %s, want updated", got)
	}
	if strings.Contains(string(readFile(t, path)), "old body") {
		t.Fatal("owned file was not overwritten")
	}
}

func TestPathCollisionVariants(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "no frontmatter", content: "plain text\n"},
		{name: "malformed delimiter", content: "---\nlocal_id: \"foreign\"\n--\n"},
		{name: "missing local id", content: "---\nsubject: \"foreign\"\n---\n"},
		{name: "non top-level local id", content: "---\n  local_id: \"foreign\"\n---\n"},
		{name: "unparseable scalar", content: "---\nlocal_id: \"foreign\\x\"\n---\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			msg := mail.Message{LocalID: plainLocalID, Subject: "Collision", BodyText: "body"}
			plan, err := PlanMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
			if err != nil {
				t.Fatalf("initial PlanMessage: %v", err)
			}
			path := plan.Files[0].Path
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write collision fixture: %v", err)
			}

			_, err = PlanMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
			if !errors.Is(err, ErrPathCollision) {
				t.Fatalf("PlanMessage error = %v, want ErrPathCollision", err)
			}
			_, err = WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
			if !errors.Is(err, ErrPathCollision) {
				t.Fatalf("WriteMessage error = %v, want ErrPathCollision", err)
			}
			if got := string(readFile(t, path)); got != tt.content {
				t.Fatal("colliding file was overwritten")
			}
		})
	}
}

func TestCollisionPreflightPreventsOtherCopies(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{LocalID: plainLocalID, Subject: "Multi", BodyText: "body", Folders: []string{"A", "B"}}
	plan, err := PlanMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("PlanMessage: %v", err)
	}
	colliding := plan.Files[1].Path
	if err := os.MkdirAll(filepath.Dir(colliding), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(colliding, []byte("---\nlocal_id: \"foreign\"\n---\n"), 0o644); err != nil {
		t.Fatalf("write collision: %v", err)
	}

	_, err = WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if !errors.Is(err, ErrPathCollision) {
		t.Fatalf("WriteMessage error = %v, want ErrPathCollision", err)
	}
	if _, err := os.Stat(plan.Files[0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight wrote non-colliding copy %s: %v", plan.Files[0].Path, err)
	}
}

func TestUnsafeFilesystemState(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, plan WriteResult)
	}{
		{
			name: "markdown target symlink",
			setup: func(t *testing.T, plan WriteResult) {
				if err := os.MkdirAll(filepath.Dir(plan.Files[0].Path), 0o755); err != nil {
					t.Fatalf("mkdir target parent: %v", err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "target"), plan.Files[0].Path); err != nil {
					t.Fatalf("symlink target: %v", err)
				}
			},
		},
		{
			name: "markdown target directory",
			setup: func(t *testing.T, plan WriteResult) {
				if err := os.MkdirAll(plan.Files[0].Path, 0o755); err != nil {
					t.Fatalf("mkdir target: %v", err)
				}
			},
		},
		{
			name: "required parent symlink",
			setup: func(t *testing.T, plan WriteResult) {
				account := filepath.Dir(filepath.Dir(filepath.Dir(plan.Files[0].Path)))
				if err := os.Symlink(t.TempDir(), account); err != nil {
					t.Fatalf("symlink parent: %v", err)
				}
			},
		},
		{
			name: "required parent file",
			setup: func(t *testing.T, plan WriteResult) {
				account := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(plan.Files[0].Path))))
				if err := os.WriteFile(account, []byte("file"), 0o644); err != nil {
					t.Fatalf("write parent file: %v", err)
				}
			},
		},
		{
			name: "sidecar dir file",
			setup: func(t *testing.T, plan WriteResult) {
				sidecar := filepath.Dir(plan.Files[0].Attachments[0].Path)
				if err := os.MkdirAll(filepath.Dir(sidecar), 0o755); err != nil {
					t.Fatalf("mkdir sidecar parent: %v", err)
				}
				if err := os.WriteFile(sidecar, []byte("file"), 0o644); err != nil {
					t.Fatalf("write sidecar dir file: %v", err)
				}
			},
		},
		{
			name: "sidecar dir symlink",
			setup: func(t *testing.T, plan WriteResult) {
				sidecar := filepath.Dir(plan.Files[0].Attachments[0].Path)
				if err := os.MkdirAll(filepath.Dir(sidecar), 0o755); err != nil {
					t.Fatalf("mkdir sidecar parent: %v", err)
				}
				if err := os.Symlink(t.TempDir(), sidecar); err != nil {
					t.Fatalf("symlink sidecar: %v", err)
				}
			},
		},
		{
			name: "expected sidecar symlink",
			setup: func(t *testing.T, plan WriteResult) {
				attPath := plan.Files[0].Attachments[0].Path
				if err := os.MkdirAll(filepath.Dir(attPath), 0o755); err != nil {
					t.Fatalf("mkdir attachment parent: %v", err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "target"), attPath); err != nil {
					t.Fatalf("symlink attachment path: %v", err)
				}
			},
		},
		{
			name: "expected sidecar directory",
			setup: func(t *testing.T, plan WriteResult) {
				if err := os.MkdirAll(plan.Files[0].Attachments[0].Path, 0o755); err != nil {
					t.Fatalf("mkdir attachment path: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			msg := mail.Message{
				LocalID:     plainLocalID,
				Subject:     "Unsafe",
				BodyText:    "body",
				Attachments: []mail.Attachment{{Filename: "a.txt", Data: []byte("a")}},
			}
			plan, err := PlanMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
			if err != nil {
				t.Fatalf("PlanMessage: %v", err)
			}
			tt.setup(t, plan)
			_, err = PlanMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("PlanMessage error = %v, want ErrUnsafePath", err)
			}
			_, err = WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("WriteMessage error = %v, want ErrUnsafePath", err)
			}
		})
	}
}

func TestUnsafePreflightPreventsOtherCopies(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{
		LocalID:     plainLocalID,
		Subject:     "Unsafe Multi",
		BodyText:    "body",
		Folders:     []string{"A", "B"},
		Attachments: []mail.Attachment{{Filename: "a.txt", Data: []byte("a")}},
	}
	plan, err := PlanMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("PlanMessage: %v", err)
	}
	if err := os.MkdirAll(plan.Files[1].Attachments[0].Path, 0o755); err != nil {
		t.Fatalf("mkdir at the attachment path: %v", err)
	}
	_, err = WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("WriteMessage error = %v, want ErrUnsafePath", err)
	}
	if _, err := os.Stat(plan.Files[0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight wrote non-unsafe copy %s: %v", plan.Files[0].Path, err)
	}
}

func msgWithLocalID(m mail.Message, id string) mail.Message {
	m.LocalID = id
	return m
}
