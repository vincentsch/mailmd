//go:build !windows

package writer

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestPrivateModesAcrossUmasks(t *testing.T) {
	for _, mask := range []string{"0", "77", "777"} {
		t.Run(mask, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestPrivateModesUmaskChild$")
			cmd.Env = append(os.Environ(), "MAILMD_TEST_UMASK="+mask)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("umask child: %v\n%s", err, out)
			}
		})
	}
}

func TestPrivateModesUmaskChild(t *testing.T) {
	raw := os.Getenv("MAILMD_TEST_UMASK")
	if raw == "" {
		t.Skip("subprocess-only umask test")
	}
	mask, err := strconv.ParseInt(raw, 8, 32)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(int(mask))
	defer syscall.Umask(old)
	_, err = WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, privateTestMessage())
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if path == root {
			want = 0o755
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The root can also be a folder that doesn't exist yet.
	newRoot := filepath.Join(root, "new-parent", "new-root")
	if _, err = WriteMessage(context.Background(), Options{Root: newRoot, Account: "Acct"}, privateTestMessage()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Dir(newRoot), newRoot} {
		info, e := os.Stat(p)
		if e != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("new output mode %s %v %v", p, info, e)
		}
	}
}

func TestModeOnlyRepairsArePlannedAndPreserveBytesAndMtime(t *testing.T) {
	opts := Options{Root: t.TempDir(), Account: "Acct"}
	msg := privateTestMessage()
	first, err := WriteMessage(context.Background(), opts, msg)
	if err != nil {
		t.Fatal(err)
	}
	file := first.Files[0].Path
	attachment := first.Files[0].Attachments[0].Path
	for _, path := range []string{file, attachment, filepath.Dir(file), filepath.Join(opts.Root, "Acct"), filepath.Dir(attachment)} {
		t.Run(path, func(t *testing.T) {
			before, _ := os.Stat(file)
			data, _ := os.ReadFile(file)
			if err := os.Chmod(path, 0o777); err != nil {
				t.Fatal(err)
			}
			plan, err := PlanMessage(context.Background(), opts, msg)
			if err != nil || !plan.Changed {
				t.Fatalf("plan = %#v, err=%v; want permission change", plan, err)
			}
			written, err := WriteMessage(context.Background(), opts, msg)
			if err != nil || !written.Changed {
				t.Fatalf("write = %#v, err=%v", written, err)
			}
			after, _ := os.Stat(file)
			afterData, _ := os.ReadFile(file)
			if !before.ModTime().Equal(after.ModTime()) || !bytes.Equal(data, afterData) {
				t.Fatal("mode-only repair changed markdown bytes or mtime")
			}
			quiet, err := PlanMessage(context.Background(), opts, msg)
			if err != nil || quiet.Changed {
				t.Fatalf("post-repair plan=%#v err=%v", quiet, err)
			}
		})
	}
}
