package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vincentsch/mailmd/internal/privatefs"
)

func mockSetup(root, secret string) Setup {
	return func(ctx context.Context, stage string) (Settings, string, error) {
		return Settings{}, "", os.WriteFile(filepath.Join(root, "mailmd", stage, "mock.json"), []byte(secret), 0o600)
	}
}
func TestConnectCollisionRefreshAndPrivateRegistry(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	a, e := Connect(ctx, root, "A/B", "mock", mockSetup(root, "secret-one"), privatefs.Hooks{})
	if e != nil {
		t.Fatal(e)
	}
	b, e := Connect(ctx, root, "A\\B", "mock", mockSetup(root, "secret-two"), privatefs.Hooks{})
	if e != nil {
		t.Fatal(e)
	}
	if a.ArchiveDir == b.ArchiveDir || len(b.ArchiveDir) > 120 {
		t.Fatal("archive collision")
	}
	refreshed, e := Connect(ctx, root, a.Name, "mock", mockSetup(root, "rotated-secret"), privatefs.Hooks{})
	if e != nil || refreshed.ArchiveDir != a.ArchiveDir {
		t.Fatalf("refresh: %#v %v", refreshed, e)
	}
	registry, e := Load(root)
	if e != nil || len(registry.Accounts) != 2 {
		t.Fatalf("registry %v %v", registry, e)
	}
	data, _ := os.ReadFile(filepath.Join(root, "mailmd", "accounts.json"))
	if strings.Contains(string(data), "secret") {
		t.Fatal("registry leaked credentials")
	}
	if e = filepath.WalkDir(filepath.Join(root, "mailmd"), func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if !privatefs.HasMode(info.Mode(), want) {
			t.Fatalf("mode %s: %o", p, info.Mode().Perm())
		}
		if strings.Contains(d.Name(), "connect-stage-") {
			t.Fatal("stage retained")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestConnectingAgainLeavesTheSavedSettings(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	if _, e := Connect(ctx, root, "work", "mock", mockSetup(root, "secret-one"), privatefs.Hooks{}); e != nil {
		t.Fatal(e)
	}
	want, e := Update(root, "work", func(a *AccountConfig) {
		a.Root, a.Folders, a.Include, a.Exclude, a.Since, a.KeepRaw, a.PreferText = filepath.Join(root, "archive"), []string{"INBOX"}, []string{"Junk"}, []string{"Spam"}, "12m", true, true
	})
	if e != nil {
		t.Fatal(e)
	}
	got, e := Connect(ctx, root, "work", "mock", mockSetup(root, "secret-two"), privatefs.Hooks{})
	if e != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("after connecting again = %+v, %v", got, e)
	}
}

func TestConnectFailuresRestorePriorRegistryAndCredentials(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, failure := range []string{"setup", "rename-credential", "rename-registry", "sync-credential", "sync-registry", "cleanup"} {
			t.Run(failure+map[bool]string{true: "-refresh", false: "-new"}[existing], func(t *testing.T) {
				root := t.TempDir()
				ctx := context.Background()
				if existing {
					if _, e := Connect(ctx, root, "account", "mock", mockSetup(root, "old-secret"), privatefs.Hooks{}); e != nil {
						t.Fatal(e)
					}
				}
				before, _ := Load(root)
				oldRegistry, _ := os.ReadFile(filepath.Join(root, "mailmd", "accounts.json"))
				oldCred, _ := os.ReadFile(filepath.Join(root, "mailmd", "account", "mock.json"))
				injected := errors.New("injected")
				fired := false
				promoted := false
				h := privatefs.Hooks{Rename: func(old, new string) error {
					if !fired && ((failure == "rename-credential" && strings.HasSuffix(new, "mock.json")) || (failure == "rename-registry" && strings.HasSuffix(new, "accounts.json"))) {
						fired = true
						return injected
					}
					e := os.Rename(old, new)
					if e == nil {
						promoted = true
					}
					return e
				}, SyncDir: func(p string) error {
					if promoted && !fired && ((failure == "sync-credential" && filepath.Base(p) == "account") || (failure == "sync-registry" && filepath.Base(p) == "mailmd")) {
						fired = true
						return injected
					}
					return privatefs.SyncDir(p)
				}, Remove: func(p string) error {
					if existing && failure == "cleanup" && promoted && !fired {
						fired = true
						return injected
					}
					return os.Remove(p)
				}}
				setup := mockSetup(root, "new-secret")
				if failure == "setup" {
					setup = func(context.Context, string) (Settings, string, error) { return Settings{}, "", injected }
				}
				_, e := Connect(ctx, root, "account", "mock", setup, h)
				if failure == "cleanup" && !existing {
					if e != nil {
						t.Fatal(e)
					}
					return
				}
				if e == nil {
					t.Fatal("injected failure passed")
				}
				after, _ := Load(root)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("failed connect changed visible registry")
				}
				registry, _ := os.ReadFile(filepath.Join(root, "mailmd", "accounts.json"))
				cred, _ := os.ReadFile(filepath.Join(root, "mailmd", "account", "mock.json"))
				if string(registry) != string(oldRegistry) || string(cred) != string(oldCred) {
					t.Fatal("failed connect changed prior bytes")
				}
				entries, _ := os.ReadDir(filepath.Join(root, "mailmd"))
				for _, d := range entries {
					if strings.HasPrefix(d.Name(), "connect-stage-") {
						t.Fatal("stage retained")
					}
				}
			})
		}
	}
}
func TestRegistryRejectsCorruptionAndBackendChangeBeforeSetup(t *testing.T) {
	root := t.TempDir()
	_, e := Connect(context.Background(), root, "account", "mock", mockSetup(root, "fixture"), privatefs.Hooks{})
	if e != nil {
		t.Fatal(e)
	}
	called := false
	_, e = Connect(context.Background(), root, "account", "gmail", func(context.Context, string) (Settings, string, error) { called = true; return Settings{}, "", nil }, privatefs.Hooks{})
	if !errors.Is(e, ErrKindMismatch) || called {
		t.Fatal("provider mismatch reached setup")
	}
	if e = os.WriteFile(filepath.Join(root, "mailmd", "accounts.json"), []byte(`{"schema":"unknown","accounts":[]}`), 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(root); !errors.Is(e, ErrRegistry) {
		t.Fatal("corruption accepted")
	}
}

func TestRemoveDeletesOnlyThatAccountsFiles(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	for _, name := range []string{"work", "home"} {
		a, e := Connect(ctx, root, name, "mock", mockSetup(root, "secret-"+name), privatefs.Hooks{})
		if e != nil {
			t.Fatal(e)
		}
		if e = SaveLastSync(root, a, LastSync{Outcome: "completed"}); e != nil {
			t.Fatal(e)
		}
	}
	workDir := filepath.Join(root, "mailmd", "work")
	want := []string{filepath.Join(workDir, "mock.json"), filepath.Join(workDir, "last-sync.json")}
	registry, _ := os.ReadFile(filepath.Join(root, "mailmd", "accounts.json"))

	// A dry run names the files and changes nothing.
	a, files, e := Remove(root, "WORK", true)
	if e != nil || a.Name != "work" || !reflect.DeepEqual(files, want) {
		t.Fatalf("dry run = %+v %v %v", a, files, e)
	}
	if after, _ := os.ReadFile(filepath.Join(root, "mailmd", "accounts.json")); string(after) != string(registry) {
		t.Fatal("dry run changed accounts.json")
	}
	for _, path := range want {
		if _, e := os.Stat(path); e != nil {
			t.Fatalf("dry run deleted %s", path)
		}
	}

	_, files, e = Remove(root, "work", false)
	if e != nil || !reflect.DeepEqual(files, want) {
		t.Fatalf("remove = %v %v", files, e)
	}
	if _, e := os.Lstat(workDir); !os.IsNotExist(e) {
		t.Fatalf("the account's folder is still there: %v", e)
	}
	r, e := Load(root)
	if e != nil || len(r.Accounts) != 1 || r.Accounts[0].Name != "home" {
		t.Fatalf("registry after remove = %+v %v", r, e)
	}
	if data, e := os.ReadFile(filepath.Join(root, "mailmd", "home", "mock.json")); e != nil || string(data) != "secret-home" {
		t.Fatalf("the other account's login changed: %q %v", data, e)
	}
	if _, ok := LoadLastSync(root, r.Accounts[0]); !ok {
		t.Fatal("the other account's last sync is gone")
	}

	if _, _, e = Remove(root, "work", false); !errors.Is(e, ErrNotFound) {
		t.Fatalf("removing it again = %v", e)
	}
}

// A login file that is already gone, for example because the user deleted it
// by hand, doesn't stop the account from being removed.
func TestRemoveWorksWithoutALoginFile(t *testing.T) {
	root := t.TempDir()
	if _, e := Connect(context.Background(), root, "work", "mock", mockSetup(root, "secret"), privatefs.Hooks{}); e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(filepath.Join(root, "mailmd", "work", "mock.json")); e != nil {
		t.Fatal(e)
	}
	_, files, e := Remove(root, "work", false)
	if e != nil || len(files) != 0 {
		t.Fatalf("remove = %v %v", files, e)
	}
	if r, _ := Load(root); len(r.Accounts) != 0 {
		t.Fatalf("still listed: %+v", r.Accounts)
	}
}

func TestRegistryAcceptsACAFileOnlyAsAnAbsolutePathForIMAP(t *testing.T) {
	imap := AccountConfig{Name: "work", Backend: "imap", ArchiveDir: "work", CredentialRef: "work/imap.json",
		Settings: Settings{Host: "imap.example.com", Port: 993, Username: "dana", TLSMode: "implicit"}}
	abs, _ := filepath.Abs("ca.pem")
	for _, tt := range []struct {
		backend, file string
		ok            bool
	}{
		{"imap", abs, true},
		{"imap", "ca.pem", false},
		{"imap", abs + "\n", false},
		{"mock", abs, false},
	} {
		a := imap
		a.Settings.CAFile = tt.file
		if tt.backend == "mock" {
			a.Backend, a.CredentialRef, a.Settings = "mock", "work/mock.json", Settings{CAFile: tt.file}
		}
		if err := validate(Registry{Schema: Schema, Accounts: []AccountConfig{a}}); (err == nil) != tt.ok {
			t.Errorf("%s with %q: %v", tt.backend, tt.file, err)
		}
	}
}
