package cli

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/privatefs"
	rg "github.com/vincentsch/rungrad"
)

// readTree returns the content of every file below dir.
func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		files[path] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestDisconnectDeletesTheLoginAndKeepsTheArchive(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	registerMock(t, root, "work")
	registerMock(t, root, "home")
	opts := mockOptions(root, mockService(2))
	opts.HomeDir = home
	if code, stdout, stderr := invoke(t, opts, "", "sync"); code != 0 {
		t.Fatalf("sync %d %s %s", code, stdout, stderr)
	}
	archive := filepath.Join(home, "mail", "work")
	before := readTree(t, archive)
	if len(before) == 0 {
		t.Fatal("the sync wrote no archive")
	}
	login := filepath.Join(root, "mailmd", "work", "mock.json")
	shown := filepath.Join("~", "mail", "work")
	registry, _ := os.ReadFile(filepath.Join(root, "mailmd", "accounts.json"))

	code, stdout, stderr := invoke(t, opts, "", "disconnect", "work", "--dry-run")
	if code != 0 {
		t.Fatalf("dry run %d %s %s", code, stdout, stderr)
	}
	for _, want := range []string{"mailmd would remove work from its account list and delete these files:", login, "last-sync.json", "The archive in " + shown + " stays as it is. Nothing was changed because of --dry-run."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry run output is missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(login); err != nil {
		t.Fatalf("the dry run deleted the login: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(root, "mailmd", "accounts.json")); string(after) != string(registry) {
		t.Fatal("the dry run changed accounts.json")
	}

	code, stdout, stderr = invoke(t, opts, "", "disconnect", "work")
	if code != 0 {
		t.Fatalf("disconnect %d %s %s", code, stdout, stderr)
	}
	for _, want := range []string{"Disconnected work. mailmd removed it from its account list and deleted these files:", login, "The archive in " + shown + " stays as it is.\n"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output is missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "mailmd", "work")); !os.IsNotExist(err) {
		t.Fatalf("the account's config folder is still there: %v", err)
	}
	after := readTree(t, archive)
	if len(after) != len(before) {
		t.Fatalf("the archive changed: %d files before, %d after", len(before), len(after))
	}
	for path, data := range before {
		if after[path] != data {
			t.Fatalf("the archive file %s changed", path)
		}
	}
	var list AccountList
	_, stdout, _ = invoke(t, opts, "", "list", "accounts", "--json")
	if err := json.Unmarshal([]byte(stdout), &list); err != nil || len(list.Accounts) != 1 || list.Accounts[0].Name != "home" {
		t.Fatalf("accounts after disconnect: %s %v", stdout, err)
	}

	var result DisconnectResult
	code, stdout, _ = invoke(t, opts, "", "disconnect", "home", "--dry-run", "--json")
	if code != 0 || json.Unmarshal([]byte(stdout), &result) != nil {
		t.Fatalf("json %d %s", code, stdout)
	}
	if result.Schema != "mailmd-disconnect/1" || result.Account != "home" || !result.DryRun || len(result.Deleted) != 2 || result.Archive != filepath.Join(home, "mail", "home") {
		t.Fatalf("json result = %+v", result)
	}
}

func TestDisconnectNamesTheConnectedAccounts(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := invoke(t, Options{ConfigRoot: root}, "", "disconnect", "work"); code != rg.ExitNotFound || !strings.Contains(stderr, "No accounts are connected yet") {
		t.Fatalf("without accounts: %d %s", code, stderr)
	}
	registerMock(t, root, "work")
	code, _, stderr := invoke(t, Options{ConfigRoot: root}, "", "disconnect", "wrok")
	if code != rg.ExitNotFound || !strings.Contains(stderr, `There is no account named "wrok"`) || !strings.Contains(stderr, "Connected accounts: work") {
		t.Fatalf("unknown account: %d %s", code, stderr)
	}
	if code, _, _ := invoke(t, Options{ConfigRoot: root}, "", "disconnect"); code != rg.ExitUsage {
		t.Fatalf("without a name: %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "mailmd", "work", "mock.json")); err != nil {
		t.Fatalf("a failed disconnect deleted the login: %v", err)
	}
}

func TestDisconnectSaysWhereToRemoveTheAppsAccess(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct {
		name, kind, file string
		settings         accounts.Settings
		want             string
	}{
		{"gmail", "gmail", "gmail-token.json", accounts.Settings{}, "https://myaccount.google.com/connections"},
		{"outlook", "graph", "graph-token.json", accounts.Settings{ClientID: "12345678-1234-1234-1234-123456789abc", Tenant: "common"}, "https://myapps.microsoft.com"},
		{"fastmail", "imap", "imap.json", accounts.Settings{Host: "imap.fastmail.com", Port: 993, Username: "you@fastmail.com", TLSMode: "implicit"}, "app password"},
	} {
		_, err := accounts.Connect(context.Background(), root, c.name, c.kind, func(ctx context.Context, stage string) (accounts.Settings, string, error) {
			return c.settings, "", os.WriteFile(filepath.Join(root, "mailmd", stage, c.file), []byte(`{}`), 0o600)
		}, privatefs.Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := invoke(t, Options{ConfigRoot: root}, "", "disconnect", c.name)
		if code != 0 || !strings.Contains(stdout, c.want) || !strings.Contains(stdout, c.file) {
			t.Fatalf("%s: %d %s %s", c.name, code, stdout, stderr)
		}
	}
}
