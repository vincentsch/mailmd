package writer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vincentsch/mailmd/internal/mail"
)

func privateTestMessage() mail.Message {
	return mail.Message{LocalID: plainLocalID, Subject: "Private", BodyText: "body", Folders: []string{"A/B"},
		Attachments: []mail.Attachment{{Filename: "a.txt", Data: []byte("payload"), Size: 7}}}
}

type durabilityEvent struct{ operation, path string }

type durabilityFS struct {
	realFileSystem
	events []durabilityEvent
	fail   func(operation, path string) bool
	cause  error
}

func (fs *durabilityFS) event(operation, path string) error {
	fs.events = append(fs.events, durabilityEvent{operation, path})
	if fs.fail != nil && fs.fail(operation, path) {
		return fs.cause
	}
	return nil
}
func (fs *durabilityFS) Chmod(path string, perm os.FileMode) error {
	if err := fs.event("chmod", path); err != nil {
		return err
	}
	return fs.realFileSystem.Chmod(path, perm)
}
func (fs *durabilityFS) Rename(old, path string) error {
	if err := fs.event("rename", path); err != nil {
		return err
	}
	return fs.realFileSystem.Rename(old, path)
}
func (fs *durabilityFS) Remove(path string) error {
	if err := fs.event("remove", path); err != nil {
		return err
	}
	return fs.realFileSystem.Remove(path)
}

func TestBatchRecordsChangedFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	batch := NewBatch()
	opts := Options{Root: root, Account: "Acct", Batch: batch}
	msg := privateTestMessage()
	result, err := WriteMessage(context.Background(), opts, msg)
	if err != nil {
		t.Fatal(err)
	}
	file := result.Files[0]
	if !batch.files[file.Path] || !batch.dirs[filepath.Dir(file.Path)] {
		t.Fatalf("batch is missing the Markdown file or its directory: %v %v", batch.files, batch.dirs)
	}
	for _, att := range file.Attachments {
		if att.Path != "" && !batch.files[att.Path] {
			t.Fatalf("batch is missing attachment %s", att.Path)
		}
	}
	if !batch.dirs[filepath.Join(root, "Acct")] || !batch.dirs[root] {
		t.Fatalf("batch is missing new directories: %v", batch.dirs)
	}
	if err := batch.Sync(); err != nil {
		t.Fatal(err)
	}
	if len(batch.files) != 0 || len(batch.dirs) != 0 {
		t.Fatalf("Sync did not empty the batch")
	}
	// Writing the same message again changes nothing and records nothing.
	if _, err := WriteMessage(context.Background(), opts, msg); err != nil {
		t.Fatal(err)
	}
	if len(batch.files) != 0 || len(batch.dirs) != 0 {
		t.Fatalf("an unchanged message was recorded: %v %v", batch.files, batch.dirs)
	}
	// Removing the attachments records the folders they were in.
	msg.Attachments = nil
	if _, err := WriteMessage(context.Background(), opts, msg); err != nil {
		t.Fatal(err)
	}
	if !batch.dirs[filepath.Dir(file.Path)] {
		t.Fatalf("removal did not record its directory: %v", batch.dirs)
	}
}

// A crash before the batch flush can leave an empty or half-written file.
// The next run must replace it instead of reporting a collision.
func TestLeftoverFromACrashIsReplaced(t *testing.T) {
	opts := Options{Root: t.TempDir(), Account: "Acct"}
	msg := privateTestMessage()
	first, err := WriteMessage(context.Background(), opts, msg)
	if err != nil {
		t.Fatal(err)
	}
	path := first.Files[0].Path
	for _, leftover := range []string{"", "---\nlocal_id: \"mailmd-loc"} {
		if err := os.WriteFile(path, []byte(leftover), 0o600); err != nil {
			t.Fatal(err)
		}
		again, err := WriteMessage(context.Background(), opts, msg)
		if err != nil || again.Files[0].Status != WriteUpdated {
			t.Fatalf("leftover %q: %#v %v", leftover, again.Files[0], err)
		}
	}
}

func TestWriteFailuresAreTypedAndReplayConverges(t *testing.T) {
	for _, operation := range []string{"chmod", "rename"} {
		t.Run(operation, func(t *testing.T) {
			opts := Options{Root: t.TempDir(), Account: "Acct"}
			msg := privateTestMessage()
			first, err := WriteMessage(context.Background(), opts, msg)
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "chmod":
				if runtime.GOOS == "windows" {
					t.Skip("Windows has no Unix file modes to repair")
				}
				if err := os.Chmod(first.Files[0].Path, 0o644); err != nil {
					t.Fatal(err)
				}
			default:
				msg.BodyText = "changed"
			}
			cause := errors.New("injected failure")
			fs := &durabilityFS{cause: cause, fail: func(op, path string) bool { return op == operation }}
			_, err = writeMessageWithFS(context.Background(), fs, opts, msg)
			var pathErr *PathError
			if !errors.Is(err, cause) || !errors.As(err, &pathErr) {
				t.Fatalf("error=%v, want typed injected failure", err)
			}
			if _, err := WriteMessage(context.Background(), opts, msg); err != nil {
				t.Fatalf("replay: %v", err)
			}
			plan, err := PlanMessage(context.Background(), opts, msg)
			if err != nil || plan.Changed {
				t.Fatalf("replay failed to converge: %#v, %v", plan, err)
			}
			if temp := findTempFiles(t, opts.Root); len(temp) != 0 {
				t.Fatalf("temporary leftovers: %v", temp)
			}
		})
	}
}
