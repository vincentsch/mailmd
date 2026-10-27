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

// wrappingFS makes the real file system fail at a chosen point. Tests use it
// to leave an archive half written, then check that a normal run repairs it.
type wrappingFS struct {
	realFileSystem
	// A count goes up only after the operation worked. So a test can fail
	// "after one renamed copy", when the first file is already on disk.
	renameCount       int
	writeCount        int
	removeCount       int
	failRenameAfter   int
	failWriteAfter    int
	failRemoveAfter   int
	renameErr         error
	writeErr          error
	removeErr         error
	cancel            context.CancelFunc
	cancelAfterRename int
}

func (fs *wrappingFS) Rename(oldpath, newpath string) error {
	// A failing rename returns before it touches the disk. A rename that works
	// can cancel the context, like Ctrl-C after some files are written.
	if fs.renameErr != nil && fs.renameCount >= fs.failRenameAfter {
		return fs.renameErr
	}
	if err := fs.realFileSystem.Rename(oldpath, newpath); err != nil {
		return err
	}
	fs.renameCount++
	if fs.cancel != nil && fs.cancelAfterRename > 0 && fs.renameCount >= fs.cancelAfterRename {
		fs.cancel()
		fs.cancel = nil
	}
	return nil
}

func (fs *wrappingFS) WriteTemp(ctx context.Context, dir string, data []byte, perm os.FileMode) (string, error) {
	// A failing WriteTemp returns no temporary file, so atomicWrite never
	// renames anything.
	if fs.writeErr != nil && fs.writeCount >= fs.failWriteAfter {
		return "", fs.writeErr
	}
	tmp, err := fs.realFileSystem.WriteTemp(ctx, dir, data, perm)
	if err != nil {
		return "", err
	}
	fs.writeCount++
	return tmp, nil
}

func (fs *wrappingFS) Remove(name string) error {
	// A failing Remove returns before it touches the disk.
	if fs.removeErr != nil && fs.removeCount >= fs.failRemoveAfter {
		fs.removeCount++
		return fs.removeErr
	}
	fs.removeCount++
	return fs.realFileSystem.Remove(name)
}

func TestPartialRenameFailureCanBeReconciled(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{LocalID: plainLocalID, Subject: "Partial", BodyText: "body", Folders: []string{"A", "B"}}
	renameErr := errors.New("injected rename failure")
	fs := &wrappingFS{failRenameAfter: 1, renameErr: renameErr}
	result, err := writeMessageWithFS(context.Background(), fs, Options{Root: root, Account: "Acct"}, msg)
	if !errors.Is(err, renameErr) {
		t.Fatalf("writeMessageWithFS error = %v, want injected rename failure", err)
	}

	written := 0
	for _, file := range result.Files {
		if _, err := os.Stat(file.Path); err == nil {
			written++
		}
	}
	if written != 1 {
		t.Fatalf("written markdown copies = %d, want 1", written)
	}

	reconciled, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("reconcile WriteMessage: %v", err)
	}
	if !reconciled.Changed {
		t.Fatal("reconcile Changed = false, want true for remaining copy")
	}
	final, err := PlanMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("final PlanMessage: %v", err)
	}
	if final.Changed {
		t.Fatal("final PlanMessage Changed = true, want false")
	}
}

func TestPartialWriteFailureCanBeReconciled(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{LocalID: plainLocalID, Subject: "Partial Write", BodyText: "body", Folders: []string{"A", "B"}}
	writeErr := errors.New("injected write failure")
	fs := &wrappingFS{failWriteAfter: 1, writeErr: writeErr}
	result, err := writeMessageWithFS(context.Background(), fs, Options{Root: root, Account: "Acct"}, msg)
	if !errors.Is(err, writeErr) {
		t.Fatalf("writeMessageWithFS error = %v, want injected write failure", err)
	}

	written := 0
	for _, file := range result.Files {
		if _, err := os.Stat(file.Path); err == nil {
			written++
		}
	}
	if written != 1 {
		t.Fatalf("written markdown copies = %d, want 1", written)
	}

	reconciled, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("reconcile WriteMessage: %v", err)
	}
	if !reconciled.Changed {
		t.Fatal("reconcile Changed = false, want true for remaining copy")
	}
	final, err := PlanMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("final PlanMessage: %v", err)
	}
	if final.Changed {
		t.Fatal("final PlanMessage Changed = true, want false")
	}
}

func TestContextCancellationAfterRenameCanBeReconciled(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{LocalID: plainLocalID, Subject: "Partial Cancel", BodyText: "body", Folders: []string{"A", "B"}}
	ctx, cancel := context.WithCancel(context.Background())
	fs := &wrappingFS{cancel: cancel, cancelAfterRename: 1}
	result, err := writeMessageWithFS(ctx, fs, Options{Root: root, Account: "Acct"}, msg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeMessageWithFS error = %v, want context.Canceled", err)
	}

	written := 0
	for _, file := range result.Files {
		if _, err := os.Stat(file.Path); err == nil {
			written++
		}
	}
	if written != 1 {
		t.Fatalf("written markdown copies = %d, want 1", written)
	}
	if tmp := findTempFiles(t, root); len(tmp) != 0 {
		t.Fatalf("temp files after cancellation = %#v", tmp)
	}

	reconciled, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("reconcile WriteMessage: %v", err)
	}
	if !reconciled.Changed {
		t.Fatal("reconcile Changed = false, want true for remaining copy")
	}
	final, err := PlanMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("final PlanMessage: %v", err)
	}
	if final.Changed {
		t.Fatal("final PlanMessage Changed = true, want false")
	}
}

func TestWriteTempCancellationDoesNotRename(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{
		LocalID:     plainLocalID,
		Subject:     "Cancel",
		BodyText:    "body",
		Attachments: []mail.Attachment{{Filename: "big.bin", Data: make([]byte, writeChunkSize+1)}},
	}
	fs := &wrappingFS{writeErr: context.Canceled}
	result, err := writeMessageWithFS(context.Background(), fs, Options{Root: root, Account: "Acct"}, msg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeMessageWithFS error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(result.Files[0].Attachments[0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attachment was renamed into place after cancellation: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(result.Files[0].Attachments[0].Path), ".mailmd-tmp-*")); err != nil || len(matches) != 0 {
		t.Fatalf("temp files after cancellation = %#v, err %v", matches, err)
	}
}

func findTempFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".mailmd-tmp-") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk temp files: %v", err)
	}
	return out
}
