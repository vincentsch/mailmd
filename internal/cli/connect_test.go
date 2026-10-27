package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend/imap/imaptest"
	"github.com/vincentsch/mailmd/internal/presets"
)

func imapArgs(server *imaptest.Server, extra ...string) []string {
	return append([]string{"connect", "imap", "--host", server.Host(), "--port", strconv.Itoa(server.Port()), "--username", server.Username()}, extra...)
}

func TestConnectIMAPPasswordSources(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{})
	yes := true
	for _, source := range []string{"stdin", "env", "prompt"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			opts := Options{ConfigRoot: root, RootCAs: server.CertPool(), Interactive: &yes,
				Getenv: func(key string) string {
					if source == "env" && key == "MAILMD_IMAP_PASSWORD" {
						return server.Password()
					}
					return ""
				},
				PasswordPrompt: func(context.Context, io.Reader, io.Writer, string) (string, error) {
					if source != "prompt" {
						t.Fatal("asked for a password")
					}
					return server.Password(), nil
				},
			}
			args := imapArgs(server, "--account", "work", "--json")
			input := ""
			if source == "stdin" {
				args = append(args, "--password-stdin")
				input = server.Password() + "\n"
			}
			code, stdout, stderr := invoke(t, opts, input, args...)
			if code != 0 || strings.Contains(stdout+stderr, server.Password()) {
				t.Fatalf("connect %d %s %s", code, stdout, stderr)
			}
			var result ConnectResult
			if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.Account != "work" || result.Folders == 0 || result.Next != "mailmd sync work" {
				t.Fatalf("result %s %v", stdout, err)
			}
			// The test server has Gmail's All Mail, which a sync uses alone.
			if strings.Contains(strings.Join(result.LeftOut, ","), "[Gmail]/All Mail") || !strings.Contains(strings.Join(result.LeftOut, ","), "INBOX") || !strings.Contains(result.Note, "[Gmail]/All Mail") {
				t.Fatalf("left out = %v, note = %q", result.LeftOut, result.Note)
			}
		})
	}
}

func TestConnectIMAPHumanOutputAndSavedSettings(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{})
	root, home := t.TempDir(), t.TempDir()
	opts := Options{ConfigRoot: root, HomeDir: home, RootCAs: server.CertPool()}
	// The login name has no @, so the address comes from --email.
	args := imapArgs(server, "--email", "dana@example.com", "--account", "work", "--password-stdin", "--out", "~/archive", "--exclude", "Work", "--since", "12m")
	code, stdout, stderr := invoke(t, opts, server.Password()+"\n", args...)
	if code != 0 {
		t.Fatalf("connect %d %s %s", code, stdout, stderr)
	}
	for _, want := range []string{"Connected work (dana@example.com).", "so mailmd syncs only that folder", "Mail goes to ~/archive/work.", "Next: mailmd sync work"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output is missing %q:\n%s", want, stdout)
		}
	}
	r, _ := accounts.Load(root)
	a := r.Accounts[0]
	if a.Root != filepath.Join(home, "archive") || len(a.Exclude) != 1 || a.Since != "12m" || a.Email != "dana@example.com" || a.Settings.Username != server.Username() {
		t.Fatalf("saved account = %+v", a)
	}
	// Connecting again renews the login, and the settings stay.
	code, _, stderr = invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "WORK", "--password-stdin")...)
	if code != 0 {
		t.Fatalf("reconnect %d %s", code, stderr)
	}
	r, _ = accounts.Load(root)
	if len(r.Accounts) != 1 || r.Accounts[0].Root != a.Root || r.Accounts[0].Since != "12m" || r.Accounts[0].Email != "dana@example.com" {
		t.Fatalf("after reconnect = %+v", r.Accounts)
	}
	_, stdout, _ = invoke(t, opts, "", "list", "accounts")
	if !strings.Contains(stdout, "dana@example.com") {
		t.Fatalf("list accounts:\n%s", stdout)
	}
}

func TestConnectIMAPWithoutAnAddressLeavesItEmpty(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{})
	root := t.TempDir()
	opts := Options{ConfigRoot: root, HomeDir: t.TempDir(), RootCAs: server.CertPool()}
	code, stdout, stderr := invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "work", "--password-stdin")...)
	if code != 0 || !strings.Contains(stdout, "Connected work (IMAP).") {
		t.Fatalf("connect %d %s %s", code, stdout, stderr)
	}
	_, list, _ := invoke(t, opts, "", "list", "accounts")
	_, status, _ := invoke(t, opts, "", "status")
	if strings.Contains(list+status, " at ") || !strings.Contains(status, "work (IMAP)") {
		t.Fatalf("list:\n%s\nstatus:\n%s", list, status)
	}
}

func TestConnectIMAPChecksSettingsBeforeThePassword(t *testing.T) {
	no := false
	asked := false
	opts := Options{ConfigRoot: t.TempDir(), Interactive: &no, PasswordPrompt: func(context.Context, io.Reader, io.Writer, string) (string, error) {
		asked = true
		return "x", nil
	}}
	for args, want := range map[string]string{
		"connect imap":                                               "Missing --host and --username",
		"connect imap --host imap.example.com":                       "Missing --username",
		"connect imap --email you@example.com --tls none":            "--tls must be implicit or starttls",
		"connect imap --email not-an-address":                        "is not an email address",
		"connect imap --email you@outlook.com":                       "mailmd connect outlook",
		"connect imap --email you@example.com --since soon":          "--since \"soon\"",
		"connect imap --email you@example.com --host h --port 70000": "--port must be between",
		"connect imap --email you@example.com --host h":              "No password was given",
	} {
		code, stdout, stderr := invoke(t, opts, "", strings.Fields(args)...)
		if code != 1 || !strings.Contains(stderr, want) {
			t.Errorf("%s: %d %s %s, want %q", args, code, stdout, stderr, want)
		}
	}
	if asked {
		t.Fatal("asked for the password")
	}
}

func TestConnectIMAPAsksForMissingSettings(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{})
	yes := true
	root := t.TempDir()
	opts := Options{ConfigRoot: root, RootCAs: server.CertPool(), Interactive: &yes,
		PasswordPrompt: func(context.Context, io.Reader, io.Writer, string) (string, error) { return server.Password(), nil }}
	// Email, server, then Enter takes the suggested account name.
	input := "mailmd-user@example.test\n" + server.Host() + "\n\n"
	code, stdout, stderr := invoke(t, opts, input, "connect", "imap", "--port", strconv.Itoa(server.Port()), "--username", server.Username())
	if code != 0 {
		t.Fatalf("connect %d %s %s", code, stdout, stderr)
	}
	for _, want := range []string{"Email address", "IMAP server", "Account name [example]"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("prompts are missing %q:\n%s", want, stderr)
		}
	}
	r, _ := accounts.Load(root)
	if len(r.Accounts) != 1 || r.Accounts[0].Name != "example" || r.Accounts[0].Settings.Username != server.Username() {
		t.Fatalf("accounts = %+v", r.Accounts)
	}
}

func TestConnectIMAPExplainsLoginFailures(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{})
	root := t.TempDir()
	opts := Options{ConfigRoot: root, RootCAs: server.CertPool()}
	code, stdout, stderr := invoke(t, opts, "PASSWORD_SENTINEL\n", imapArgs(server, "--account", "bad", "--password-stdin")...)
	if code != 3 || !strings.Contains(stderr, "rejected the login") || !strings.Contains(stderr, "Authentication failed") {
		t.Fatalf("%d %s %s", code, stdout, stderr)
	}
	sweep(t, stdout, stderr)
	if r, _ := accounts.Load(root); len(r.Accounts) != 0 {
		t.Fatal("a failed login was saved")
	}
}

func TestConnectIMAPDryRunSavesNothing(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{})
	root := t.TempDir()
	opts := Options{ConfigRoot: root, RootCAs: server.CertPool()}
	code, stdout, stderr := invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "work", "--password-stdin", "--dry-run")...)
	if code != 0 || !strings.Contains(stdout, "The login works. Nothing was saved") {
		t.Fatalf("%d %s %s", code, stdout, stderr)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("dry run wrote %v", entries)
	}
}

func TestDefaultAccountNames(t *testing.T) {
	r := accounts.Registry{Accounts: []accounts.AccountConfig{
		{Name: "gmail", Backend: "imap", Settings: accounts.Settings{Host: "imap.gmail.com", Username: "old@gmail.com"}},
	}}
	gmail, _ := presets.ForDomain("gmail.com")
	for _, tt := range []struct {
		s    imapSettings
		want string
	}{
		{imapSettings{username: "new@gmail.com", host: "imap.gmail.com", known: true, preset: gmail}, "gmail-2"},
		{imapSettings{username: "old@gmail.com", host: "imap.gmail.com", known: true, preset: gmail}, "gmail"},
		{imapSettings{username: "dana@example.org", host: "imap.example.org"}, "example"},
		{imapSettings{username: "dana", host: "imap.myhost.net"}, "myhost"},
	} {
		if got := defaultAccountName(r, tt.s); got != tt.want {
			t.Errorf("defaultAccountName(%s) = %q, want %q", tt.s.username, got, tt.want)
		}
	}
}

func TestCtrlCStopsConnectPrompts(t *testing.T) {
	yes := true
	for name, args := range map[string][]string{
		"question": {"connect", "imap"},
		"password": {"connect", "imap", "--email", "you@example.com", "--host", "imap.example.com", "--account", "x"},
	} {
		t.Run(name, func(t *testing.T) {
			opts := Options{ConfigRoot: t.TempDir(), HomeDir: t.TempDir(), Interactive: &yes, UpdateFetcher: fakeRelease{},
				PasswordPrompt: func(ctx context.Context, _ io.Reader, _ io.Writer, _ string) (string, error) {
					<-ctx.Done()
					return "", ctx.Err()
				}}
			app := New(opts)
			ctx, cancel := context.WithCancel(context.Background())
			app.Root().SetContext(ctx)
			// Nobody types anything, so stdin stays open and empty.
			stdin, typing := io.Pipe()
			defer typing.Close()
			time.AfterFunc(100*time.Millisecond, cancel)
			var stdout, stderr strings.Builder
			done := make(chan int, 1)
			go func() { done <- app.RunIO(args, stdin, &stdout, &stderr) }()
			select {
			case code := <-done:
				if code == 0 || !strings.Contains(stderr.String(), "Canceled. Nothing was saved.") || strings.Contains(stderr.String(), "sync") {
					t.Fatalf("code %d stderr:\n%s", code, stderr.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Ctrl-C did not stop the prompt")
			}
		})
	}
}

func TestConnectIMAPAgainNeedsOnlyTheAccountName(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{})
	root := t.TempDir()
	opts := Options{ConfigRoot: root, RootCAs: server.CertPool()}
	if code, stdout, stderr := invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "work", "--password-stdin")...); code != 0 {
		t.Fatalf("connect %d %s %s", code, stdout, stderr)
	}
	// For a new password, the other settings come from the saved account.
	code, stdout, stderr := invoke(t, opts, server.Password()+"\n", "connect", "imap", "--account", "work", "--password-stdin")
	if code != 0 || !strings.Contains(stdout, "Connected work") {
		t.Fatalf("refresh %d %s %s", code, stdout, stderr)
	}
}

func TestConnectRejectsAFolderThatDoesNotExist(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{})
	root := t.TempDir()
	opts := Options{ConfigRoot: root, RootCAs: server.CertPool()}
	code, stdout, stderr := invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "work", "--password-stdin", "--folder", "Inbx", "--since", "30d")...)
	if code != 1 || !strings.Contains(stderr, `--folder "Inbx" matches no folder of work. Nothing was saved.`) || !strings.Contains(stderr, "INBOX") || !strings.Contains(stderr, "Run the connect command again with the right folder.") {
		t.Fatalf("%d %s %s", code, stdout, stderr)
	}
	if r, _ := accounts.Load(root); len(r.Accounts) != 0 {
		t.Fatalf("accounts = %+v", r.Accounts)
	}
	if code, stdout, stderr = invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "work", "--password-stdin")...); code != 0 {
		t.Fatalf("connect: %d %s %s", code, stdout, stderr)
	}
	code, _, stderr = invoke(t, opts, "", "sync", "work", "--folder", "Nope", "--out", t.TempDir())
	if code != 1 || !strings.Contains(stderr, `--folder "Nope" matches no folder of work`) {
		t.Fatalf("sync: %d %s", code, stderr)
	}
}
