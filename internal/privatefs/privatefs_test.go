package privatefs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplacementFailuresRestoreBytesModesAndTimes(t *testing.T) {
	for _, fault := range []string{"sync-file", "rename-second", "sync-promoted", "remove-backup", "sync-cleanup"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			one, two := filepath.Join(root, "one"), filepath.Join(root, "two")
			old := time.Unix(1700000000, 0)
			for _, p := range []string{one, two} {
				if e := os.WriteFile(p, []byte("old"), 0o600); e != nil {
					t.Fatal(e)
				}
				if e := os.Chtimes(p, old, old); e != nil {
					t.Fatal(e)
				}
			}
			fired := false
			promotions := 0
			removed := 0
			injected := errors.New("injected")
			h := Hooks{SyncFile: func(f *os.File) error {
				if fault == "sync-file" && !fired {
					fired = true
					return injected
				}
				return f.Sync()
			}, Rename: func(from, to string) error {
				if to == one || to == two {
					promotions++
					if fault == "rename-second" && promotions == 2 && !fired {
						fired = true
						return injected
					}
				}
				return os.Rename(from, to)
			}, SyncDir: func(p string) error {
				if !fired && ((fault == "sync-promoted" && promotions == 1) || (fault == "sync-cleanup" && removed >= 2)) {
					fired = true
					return injected
				}
				return SyncDir(p)
			}, Remove: func(p string) error {
				if promotions >= 2 {
					if fault == "remove-backup" && !fired {
						fired = true
						return injected
					}
					removed++
				}
				return os.Remove(p)
			}}
			e := Replace([]Replacement{{Path: one, Data: []byte("new-one")}, {Path: two, Data: []byte("new-two")}}, h)
			if e == nil || !fired {
				t.Fatalf("fault did not fail: %v", e)
			}
			for _, p := range []string{one, two} {
				b, e := os.ReadFile(p)
				if e != nil || string(b) != "old" {
					t.Fatalf("rollback %s %q %v", p, b, e)
				}
				info, _ := os.Stat(p)
				if !HasMode(info.Mode(), 0o600) || !info.ModTime().Equal(old) {
					t.Fatalf("rollback metadata %s %v", p, info)
				}
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 2 {
				t.Fatalf("temporary files retained: %v", entries)
			}
		})
	}
}
func TestStrictJSONAndPathSafety(t *testing.T) {
	for _, data := range []string{`{"value":1,"value":2}`, `{"value":1} {"value":2}`, `{"nested":{"x":1,"x":2}}`, `{"unexpected":1}`} {
		var value struct {
			Value  int            `json:"value"`
			Nested map[string]int `json:"nested"`
		}
		if Decode([]byte(data), &value) == nil {
			t.Fatalf("malformed JSON accepted: %s", data)
		}
	}
	root := t.TempDir()
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	if e := EnsureDir(root, filepath.Join(root, "link", "child"), Hooks{}); !errors.Is(e, ErrIntegrity) {
		t.Fatalf("symlink guard %v", e)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("guard wrote outside root")
	}
}

func TestResolveRootFollowsLinksAboveTheRoot(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symbolic links are not available:", err)
	}
	want, _ := filepath.EvalSymlinks(real)
	for path, expect := range map[string]string{
		link:                                want,
		filepath.Join(link, "mail", "work"): filepath.Join(want, "mail", "work"),
	} {
		got, err := ResolveRoot(path)
		if err != nil || got != expect {
			t.Fatalf("ResolveRoot(%s) = %q, %v; want %q", path, got, err, expect)
		}
		if CheckPath(got) != nil {
			t.Fatalf("the resolved path %s still has a link", got)
		}
	}
	if CheckPath(filepath.Join(link, "mail")) == nil {
		t.Fatal("CheckPath accepted a link")
	}
}

// System links like /var on macOS or /var/run on Linux belong to root. A
// config or archive folder below them must work.
func TestCheckPathFollowsSystemLinks(t *testing.T) {
	for _, link := range []string{"/var", "/tmp", "/var/run", "/etc"} {
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&os.ModeSymlink == 0 || !TrustedLink(info) {
			continue
		}
		if err := CheckPath(filepath.Join(link, "mailmd-test", "accounts.json")); err != nil {
			t.Fatalf("CheckPath below %s: %v", link, err)
		}
		return
	}
	t.Skip("this system has no root-owned link to test with")
}
