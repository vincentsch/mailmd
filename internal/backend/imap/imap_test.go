package imap

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/imap/imaptest"
)

func TestValidateConfig(t *testing.T) {
	valid := Config{Host: "mail.example.test", Port: 993, Username: "user", Password: "secret-password", TLSMode: TLSImplicit}
	for name, mutate := range map[string]func(*Config){
		"missing host":     func(c *Config) { c.Host = "" },
		"missing username": func(c *Config) { c.Username = "" },
		"missing password": func(c *Config) { c.Password = "" },
		"missing tls mode": func(c *Config) { c.TLSMode = "" },
		"unknown tls mode": func(c *Config) { c.TLSMode = "plain" },
		"negative port":    func(c *Config) { c.Port = -1 },
		"too large port":   func(c *Config) { c.Port = 70000 },
	} {
		cfg := valid
		mutate(&cfg)
		err := validateConfig(cfg)
		if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), valid.Password) {
			t.Errorf("%s: validateConfig() = %v, want ErrInvalidConfig without the password", name, err)
		}
	}
	if err := validateConfig(valid); err != nil {
		t.Fatalf("validateConfig(valid) = %v", err)
	}
	for cfg, want := range map[Config]string{
		{Host: "imap.example.test", TLSMode: TLSImplicit}:             "imap.example.test:993",
		{Host: "imap.example.test", TLSMode: TLSStartTLS}:             "imap.example.test:143",
		{Host: "imap.example.test", Port: 1993, TLSMode: TLSImplicit}: "imap.example.test:1993",
	} {
		if got := address(cfg); got != want {
			t.Errorf("address(%+v) = %q, want %q", cfg, got, want)
		}
	}
}

func TestOpenWithImplicitTLSAndSTARTTLS(t *testing.T) {
	for _, mode := range []TLSMode{TLSImplicit, TLSStartTLS} {
		kind := imaptest.TLSImplicit
		if mode == TLSStartTLS {
			kind = imaptest.TLSStartTLS
		}
		srv := imaptest.Start(t, imaptest.Options{TLS: kind})
		b := openTestBackend(t, srv, mode)
		if len(listTestFolders(t, b)) == 0 {
			t.Fatalf("%s: no folders", mode)
		}
		_ = b.Close()
	}
}

func TestOpenExplainsCertificateProblems(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{TLS: imaptest.TLSImplicit})
	cfg := Config{Host: srv.Host(), Port: srv.Port(), Username: srv.Username(), Password: srv.Password(), TLSMode: TLSImplicit}
	_, err := Open(context.Background(), cfg)
	var problem *backend.Problem
	if !errors.As(err, &problem) || !errors.Is(err, ErrTLS) || errors.Is(err, backend.ErrRetryable) {
		t.Fatalf("Open() = %v, want a non-retryable TLS problem", err)
	}
	if !strings.Contains(problem.Text, "not trusted") || strings.Contains(err.Error(), cfg.Password) {
		t.Fatalf("problem text = %q", problem.Text)
	}
}

func TestOpenShowsTheServersLoginText(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{TLS: imaptest.TLSImplicit})
	cfg := Config{Host: srv.Host(), Port: srv.Port(), Username: srv.Username(), Password: "wrong-secret", TLSMode: TLSImplicit, RootCAs: srv.CertPool()}
	_, err := Open(context.Background(), cfg)
	var problem *backend.Problem
	if !errors.As(err, &problem) || !errors.Is(err, ErrAuth) || errors.Is(err, backend.ErrRetryable) {
		t.Fatalf("Open() = %v, want a non-retryable login problem", err)
	}
	if !strings.Contains(problem.Text, "Authentication failed") || strings.Contains(problem.Text, "wrong-secret") {
		t.Fatalf("problem text = %q, want the server's text without the password", problem.Text)
	}
	if problem.Hint == "" {
		t.Fatalf("login problem has no hint")
	}
}

func TestLoginHintForAppPasswords(t *testing.T) {
	if hint := loginHint("imap.gmail.com", "Application-specific password required: https://support.google.com/accounts/answer/185833"); !strings.Contains(hint, "app password") {
		t.Fatalf("hint = %q", hint)
	}
	if hint := loginHint("outlook.office365.com", "AUTHENTICATE failed."); !strings.Contains(hint, "connect outlook") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestOpenExplainsWhenSTARTTLSIsRefused(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{TLS: imaptest.TLSNone})
	cfg := Config{Host: srv.Host(), Port: srv.Port(), Username: srv.Username(), Password: srv.Password(), TLSMode: TLSStartTLS, RootCAs: srv.CertPool()}
	_, err := Open(context.Background(), cfg)
	var problem *backend.Problem
	if !errors.As(err, &problem) || !strings.Contains(problem.Text, "STARTTLS") {
		t.Fatalf("Open() = %v, want a STARTTLS problem", err)
	}
	if strings.Contains(strings.ToUpper(srv.DebugLog()), " LOGIN ") {
		t.Fatalf("password was sent without TLS:\n%s", srv.DebugLog())
	}
}

func TestOpenExplainsNetworkFailures(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	_, err = Open(context.Background(), Config{Host: "127.0.0.1", Port: port, Username: "u", Password: "p", TLSMode: TLSImplicit})
	if !strings.Contains(err.Error(), "refused the connection") || !errors.Is(err, backend.ErrRetryable) {
		t.Fatalf("refused port: %v", err)
	}
	_, err = Open(context.Background(), Config{Host: "imap.mailmd-test.invalid", Username: "u", Password: "p", TLSMode: TLSImplicit})
	if !strings.Contains(err.Error(), "Cannot find the server imap.mailmd-test.invalid") {
		t.Fatalf("unknown host: %v", err)
	}
}

// A server that accepts the connection but never speaks, like an implicit
// TLS port reached with STARTTLS, must not hang the command.
func TestOpenGivesUpOnASilentServer(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port
	start := time.Now()
	_, err = Open(context.Background(), Config{Host: "127.0.0.1", Port: port, Username: "u", Password: "p", TLSMode: TLSStartTLS, IdleTimeout: 200 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "greeting") {
		t.Fatalf("Open() = %v, want a greeting problem", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Open() took %s", time.Since(start))
	}
}

func TestFetchGivesUpOnAServerThatStopsAnswering(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{TLS: imaptest.TLSImplicit, BlockFetch: true})
	defer srv.UnblockFetch()
	cfg := Config{Host: srv.Host(), Port: srv.Port(), Username: srv.Username(), Password: srv.Password(), TLSMode: TLSImplicit, RootCAs: srv.CertPool(), IdleTimeout: 200 * time.Millisecond}
	b, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	_, err = b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: srv.ScopeKey("Work")})
	if !errors.Is(err, backend.ErrRetryable) || !strings.Contains(err.Error(), "stopped answering") {
		t.Fatalf("Fetch() = %v, want a retryable timeout", err)
	}
}

func TestCancelInterruptsABlockedCommand(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{TLS: imaptest.TLSImplicit, BlockList: true})
	defer srv.UnblockList()
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.Folders(ctx)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Folders() = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Folders() did not return after cancel")
	}
	if _, err := b.Folders(context.Background()); !errors.Is(err, backend.ErrClosed) {
		t.Fatalf("Folders() after cancel = %v, want ErrClosed", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{})
	b := openTestBackend(t, srv, TLSImplicit)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: srv.ScopeKey("Work")}); !errors.Is(err, backend.ErrClosed) {
		t.Fatalf("Fetch() after Close = %v", err)
	}
}

func TestServerResponseLargerThanBudgetIsAProtocolError(t *testing.T) {
	flags := make([]string, 0, 40000)
	for i := 0; i < 40000; i++ {
		flags = append(flags, "keyword-that-is-long-enough-to-fill-the-budget-"+strings.Repeat("x", 10))
	}
	srv := imaptest.Start(t, imaptest.Options{Folders: []imaptest.FolderSeed{{
		Name: "INBOX", Delimiter: "/", UIDValidity: 1,
		Messages: []imaptest.MessageSeed{{UID: 1, Flags: flags, Raw: rawTestMessage("big-flags", "Big flags")}},
	}}})
	b := openTestBackend(t, srv, TLSImplicit)
	defer b.Close()
	_, err := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: srv.ScopeKey("INBOX")})
	if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "more data than expected") {
		t.Fatalf("Fetch() = %v, want a protocol error about the response size", err)
	}
}

func openTestBackend(t *testing.T, srv *imaptest.Server, mode TLSMode) *Backend {
	t.Helper()
	cfg := Config{Host: srv.Host(), Port: srv.Port(), Username: srv.Username(), Password: srv.Password(), TLSMode: mode, RootCAs: srv.CertPool()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return b
}

func listTestFolders(t *testing.T, b *Backend) []backend.Folder {
	t.Helper()
	folders, err := b.Folders(context.Background())
	if err != nil {
		t.Fatalf("Folders() error = %v", err)
	}
	return folders
}
