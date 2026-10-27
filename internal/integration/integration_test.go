//go:build integration

// Package integration runs mailmd against real IMAP servers in Docker, which
// are Dovecot and GreenMail. scripts/integration.sh starts them and runs these
// tests, and CI does the same.
//
// The tests read these environment variables:
//
//	MAILMD_IT_CA         CA certificate that signed Dovecot's certificate
//	MAILMD_IT_DOVECOT    Dovecot implicit TLS address, like localhost:3993
//	MAILMD_IT_STARTTLS   Dovecot STARTTLS address, like localhost:3143
//	MAILMD_IT_GREENMAIL  GreenMail plain IMAP address, like localhost:4143
package integration

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/vincentsch/mailmd/internal/cli"
	"github.com/vincentsch/mailmd/internal/demo"
)

// server is one IMAP server under test.
type server struct {
	name     string
	addr     string // address mailmd connects to
	tls      string // implicit or starttls
	roots    *x509.CertPool
	password string
	// seedAddr and seedTLS are used to put mail into the account.
	seedAddr string
	seedTLS  bool
}

func servers(t *testing.T) []server {
	t.Helper()
	ca := os.Getenv("MAILMD_IT_CA")
	if ca == "" {
		t.Skip("MAILMD_IT_CA is not set; run scripts/integration.sh")
	}
	pem, err := os.ReadFile(ca)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem)
	var out []server
	if addr := os.Getenv("MAILMD_IT_DOVECOT"); addr != "" {
		out = append(out, server{name: "dovecot", addr: addr, tls: "implicit", roots: roots, password: "pass", seedAddr: addr, seedTLS: true})
	}
	if addr := os.Getenv("MAILMD_IT_STARTTLS"); addr != "" {
		out = append(out, server{name: "dovecot-starttls", addr: addr, tls: "starttls", roots: roots, password: "pass", seedAddr: os.Getenv("MAILMD_IT_DOVECOT"), seedTLS: true})
	}
	if addr := os.Getenv("MAILMD_IT_GREENMAIL"); addr != "" {
		front, frontRoots := tlsFront(t, addr)
		out = append(out, server{name: "greenmail", addr: front, tls: "implicit", roots: frontRoots, password: "secret", seedAddr: addr})
	}
	return out
}

// tlsFront handles TLS in front of GreenMail's plain IMAP port. GreenMail's
// own TLS certificate has no subject alternative name, and Go rejects such a
// certificate.
func tlsFront(t *testing.T, upstream string) (string, *x509.CertPool) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mailmd test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			in, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer in.Close()
				up, err := net.Dial("tcp", upstream)
				if err != nil {
					return
				}
				defer up.Close()
				go func() { _, _ = io.Copy(up, in) }()
				_, _ = io.Copy(in, up)
			}()
		}
	}()
	return l.Addr().String(), roots
}

// seed creates folders and appends messages for a fresh user.
func seed(t *testing.T, s server, user string, messages []demo.Message) {
	t.Helper()
	opts := &imapclient.Options{TLSConfig: &tls.Config{RootCAs: s.roots, ServerName: "localhost"}}
	var c *imapclient.Client
	var err error
	if s.seedTLS {
		c, err = imapclient.DialTLS(s.seedAddr, opts)
	} else {
		c, err = imapclient.DialInsecure(s.seedAddr, opts)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login(user, s.password).Wait(); err != nil {
		t.Fatal(err)
	}
	created := map[string]bool{"INBOX": true}
	for _, m := range messages {
		if !created[m.Folder] {
			parts := strings.Split(m.Folder, "/")
			for i := range parts {
				name := strings.Join(parts[:i+1], "/")
				if !created[name] {
					_ = c.Create(name, nil).Wait()
					created[name] = true
				}
			}
		}
		cmd := c.Append(m.Folder, int64(len(m.Raw)), nil)
		if _, err := cmd.Write(m.Raw); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := cmd.Wait(); err != nil {
			t.Fatalf("append to %s: %v", m.Folder, err)
		}
	}
}

func mailbox() []demo.Message {
	msgs := demo.Messages()
	for i := 0; i < 130; i++ {
		raw := fmt.Sprintf("Message-ID: <bulk-%d@example.org>\r\nDate: Mon, 6 Oct 2026 09:%02d:00 +0000\r\nFrom: Bulk <bulk@example.org>\r\nTo: dana@example.com\r\nSubject: Bulk message %d\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nLine of text %d.\r\n", i, i%60, i, i)
		msgs = append(msgs, demo.Message{Folder: "Bulk", Raw: []byte(raw)})
	}
	msgs = append(msgs, demo.Message{Folder: "仕事", Raw: []byte("Message-ID: <cjk-1@example.jp>\r\nDate: Mon, 6 Oct 2026 10:00:00 +0900\r\nFrom: =?UTF-8?B?55Sw5Lit?= <tanaka@example.jp>\r\nTo: dana@example.com\r\nSubject: =?UTF-8?B?5Lya6K2w44Gu6K2w5LqL6Yyy?=\r\n\r\nこんにちは\r\n")})
	return msgs
}

type run struct {
	t    *testing.T
	opts cli.Options
	root string
	out  string
}

func newRun(t *testing.T, s server) *run {
	return &run{t: t, opts: cli.Options{ConfigRoot: t.TempDir(), HomeDir: t.TempDir(), RootCAs: s.roots}, out: t.TempDir()}
}

func (r *run) mailmd(input string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := cli.New(r.opts).RunIO(args, strings.NewReader(input), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func (r *run) connect(s server, user string) {
	host, port, _ := net.SplitHostPort(s.addr)
	code, stdout, stderr := r.mailmd(s.password+"\n", "connect", "imap", "--account", "work", "--host", host, "--port", port, "--tls", s.tls, "--username", user, "--password-stdin")
	if code != 0 {
		r.t.Fatalf("connect: %d %s %s", code, stdout, stderr)
	}
}

func (r *run) sync(args ...string) cli.SyncOutput {
	code, stdout, stderr := r.mailmd("", append([]string{"sync", "work", "--out", r.out, "--json"}, args...)...)
	var out cli.SyncOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || code != 0 {
		r.t.Fatalf("sync: %d %v\n%s\n%s", code, err, stdout, stderr)
	}
	return out
}

func markdownFiles(t *testing.T, dir string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func user(t *testing.T) string {
	return "it-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func TestFullAndIncrementalSync(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			u := user(t)
			msgs := mailbox()
			seed(t, s, u, msgs)
			r := newRun(t, s)
			r.connect(s, u)
			out := r.sync()
			result := out.Accounts[0]
			if result.Outcome != "completed" || result.New != len(msgs)-1 {
				t.Fatalf("first sync: %+v", result)
			}
			files := markdownFiles(t, filepath.Join(r.out, "work"))
			if len(files) != len(msgs)-1 {
				t.Fatalf("%d files for %d messages", len(files), len(msgs)-1)
			}
			index, _ := os.ReadFile(filepath.Join(r.out, "work", "index.jsonl"))
			if bytes.Count(index, []byte("\n")) != len(msgs)-1 {
				t.Fatalf("index has %d lines", bytes.Count(index, []byte("\n")))
			}
			if out = r.sync(); out.Accounts[0].New != 0 || out.Accounts[0].Unchanged != 0 {
				t.Fatalf("second sync fetched again: %+v", out.Accounts[0])
			}
			seed(t, s, u, []demo.Message{{Folder: "INBOX", Raw: []byte("Message-ID: <late@example.org>\r\nDate: Tue, 7 Oct 2026 09:00:00 +0000\r\nFrom: Late <late@example.org>\r\nSubject: Arrived later\r\n\r\nHello.\r\n")}})
			if out = r.sync(); out.Accounts[0].New != 1 {
				t.Fatalf("incremental sync: %+v", out.Accounts[0])
			}
		})
	}
}

func TestTooLargeMessageDoesNotBlockAndIsFetchedLater(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			u := user(t)
			big := "Message-ID: <big@example.org>\r\nDate: Tue, 7 Oct 2026 09:00:00 +0000\r\nFrom: Big <big@example.org>\r\nSubject: Large scan\r\n\r\nSTART" + strings.Repeat("0123456789", 20000) + "END\r\n"
			// The large message sits in the middle of a full batch of 100.
			msgs := []demo.Message{}
			for i := 0; i < 120; i++ {
				if i == 50 {
					msgs = append(msgs, demo.Message{Folder: "INBOX", Raw: []byte(big)})
				}
				msgs = append(msgs, demo.Message{Folder: "INBOX", Raw: []byte(fmt.Sprintf("Message-ID: <small-%d@example.org>\r\nDate: Tue, 7 Oct 2026 08:00:00 +0000\r\nFrom: a@example.org\r\nSubject: Small %d\r\n\r\nHi.\r\n", i, i))})
			}
			seed(t, s, u, msgs)
			r := newRun(t, s)
			r.connect(s, u)
			result := r.sync("--max-message-bytes", "50000").Accounts[0]
			if result.New != 121 || result.Diagnostics["too_large"] != 1 {
				t.Fatalf("result: %+v", result)
			}
			// A higher limit fetches the message again and replaces its file.
			result = r.sync("--max-message-bytes", "500000").Accounts[0]
			if result.New != 0 || result.Updated != 1 {
				t.Fatalf("with a higher limit: %+v", result)
			}
			files := markdownFiles(t, filepath.Join(r.out, "work"))
			if len(files) != 121 {
				t.Fatalf("%d files", len(files))
			}
			for _, f := range files {
				data, _ := os.ReadFile(f)
				if bytes.Contains(data, []byte("Large scan")) && (!bytes.Contains(data, []byte("END")) || bytes.Contains(data, []byte("omitted_bytes"))) {
					t.Fatalf("the stub was not replaced:\n%.400s", data)
				}
			}
		})
	}
}

func TestUnreadableMessageIsKept(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			u := user(t)
			broken := "Message-ID: <broken@example.org>\r\nDate: Tue, 7 Oct 2026 09:00:00 +0000\r\nFrom: Ann <ann@example.org>\r\nSubject: Broken attachment\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nnot %%% base64 !!!\r\n"
			seed(t, s, u, []demo.Message{{Folder: "INBOX", Raw: []byte(broken)}, demo.Messages()[0]})
			r := newRun(t, s)
			r.connect(s, u)
			result := r.sync().Accounts[0]
			if result.New != 2 || result.Diagnostics["parse_failure"] != 1 {
				t.Fatalf("result: %+v", result)
			}
			found := false
			for _, f := range markdownFiles(t, filepath.Join(r.out, "work")) {
				data, _ := os.ReadFile(f)
				if bytes.Contains(data, []byte("Broken attachment")) {
					raw, err := os.ReadFile(strings.TrimSuffix(f, ".md") + ".eml")
					found = bytes.Contains(data, []byte("parse_error: ")) && err == nil && bytes.Contains(raw, []byte("not %%% base64"))
				}
			}
			if !found {
				t.Fatal("no file with parse_error and the original .eml")
			}
			// Without .mailmd the folder is read again. The broken message
			// was reported already, so it is no new warning.
			if err := os.Rename(filepath.Join(r.out, "work", ".mailmd"), filepath.Join(t.TempDir(), "old")); err != nil {
				t.Fatal(err)
			}
			if result = r.sync().Accounts[0]; result.New != 0 || len(result.Diagnostics) != 0 || result.Outcome != "completed" {
				t.Fatalf("sync without .mailmd: %+v", result)
			}
		})
	}
}

func TestInviteAndEncodedSenderAreReadable(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nSUMMARY:Planning at Seeblick\r\n" +
		"DTSTART;TZID=W. Europe Standard Time:20261105T100000\r\nDTEND;TZID=W. Europe Standard Time:20261105T113000\r\n" +
		"LOCATION:Seeblick\\, room 2\r\nORGANIZER;CN=\"Weber, Dana\":mailto:dana@example.org\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	invite := "Message-ID: <invite@example.org>\r\nDate: Tue, 7 Oct 2026 09:00:00 +0000\r\nFrom: \"Weber, Dana\" <dana@example.org>\r\n" +
		"Subject: Planning\r\nContent-Type: text/calendar; charset=utf-8; method=REQUEST\r\n\r\n" + ics
	// "田中太郎 <tanaka@example.jp>" as one encoded word.
	encoded := "Message-ID: <encoded@example.org>\r\nDate: Tue, 7 Oct 2026 10:00:00 +0000\r\nFrom: =?utf-8?b?55Sw5Lit5aSq6YOOIDx0YW5ha2FAZXhhbXBsZS5qcD4=?=\r\n" +
		"Subject: Hello\r\n\r\nHi.\r\n"
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			u := user(t)
			seed(t, s, u, []demo.Message{{Folder: "INBOX", Raw: []byte(invite)}, {Folder: "INBOX", Raw: []byte(encoded)}})
			r := newRun(t, s)
			r.connect(s, u)
			if result := r.sync().Accounts[0]; result.New != 2 {
				t.Fatalf("result: %+v", result)
			}
			var all []byte
			for _, f := range markdownFiles(t, filepath.Join(r.out, "work")) {
				data, _ := os.ReadFile(f)
				all = append(all, data...)
			}
			for _, want := range []string{"## Invitation", "**When:** Thu, 5 Nov 2026, 10:00 to 11:30 (W. Europe Standard Time)", "**Location:** Seeblick, room 2", `from: "田中太郎 <tanaka@example.jp>"`} {
				if !bytes.Contains(all, []byte(want)) {
					t.Fatalf("no file has %q:\n%s", want, all)
				}
			}
		})
	}
}

func TestLoginFailureShowsTheServerText(t *testing.T) {
	for _, s := range servers(t) {
		if s.name == "greenmail" {
			continue // GreenMail accepts any password
		}
		t.Run(s.name, func(t *testing.T) {
			r := newRun(t, s)
			host, port, _ := net.SplitHostPort(s.addr)
			code, _, stderr := r.mailmd("wrong\n", "connect", "imap", "--account", "work", "--host", host, "--port", port, "--tls", s.tls, "--username", user(t), "--password-stdin")
			if code != 3 || !strings.Contains(stderr, "rejected the login") {
				t.Fatalf("%d %s", code, stderr)
			}
		})
	}
}

func TestRecreatedFolderIsNotDuplicated(t *testing.T) {
	for _, s := range servers(t) {
		if s.name != "dovecot" {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			u := user(t)
			msg := demo.Message{Folder: "Projects", Raw: demo.Messages()[1].Raw}
			seed(t, s, u, []demo.Message{msg})
			r := newRun(t, s)
			r.connect(s, u)
			r.sync()
			// Delete the folder and create it again. The server gives it a new
			// UIDVALIDITY, and mailmd starts the folder over.
			c, err := imapclient.DialTLS(s.seedAddr, &imapclient.Options{TLSConfig: &tls.Config{RootCAs: s.roots, ServerName: "localhost"}})
			if err != nil {
				t.Fatal(err)
			}
			_ = c.Login(u, s.password).Wait()
			if err := c.Delete("Projects").Wait(); err != nil {
				t.Fatal(err)
			}
			c.Close()
			seed(t, s, u, []demo.Message{msg})
			// The file is still the same one. Only its ref changes to the new
			// UIDVALIDITY.
			result := r.sync().Accounts[0]
			if result.New != 0 || result.Updated != 1 || result.Deleted != 0 {
				t.Fatalf("after recreate: %+v", result)
			}
			if files := markdownFiles(t, filepath.Join(r.out, "work")); len(files) != 1 {
				t.Fatalf("files: %v", files)
			}
		})
	}
}

// adminClient logs in to change the mailbox the way another mail client
// would.
func adminClient(t *testing.T, s server, user string) *imapclient.Client {
	t.Helper()
	opts := &imapclient.Options{TLSConfig: &tls.Config{RootCAs: s.roots, ServerName: "localhost"}}
	var c *imapclient.Client
	var err error
	if s.seedTLS {
		c, err = imapclient.DialTLS(s.seedAddr, opts)
	} else {
		c, err = imapclient.DialInsecure(s.seedAddr, opts)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(user, s.password).Wait(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFlagChangesAndDeletionsOnTheServer(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			u := user(t)
			msgs := demo.Messages()[:3]
			for i := range msgs {
				msgs[i].Folder = "INBOX"
			}
			seed(t, s, u, msgs)
			r := newRun(t, s)
			r.connect(s, u)
			r.sync()
			// Another client reads the first message and deletes the second.
			c := adminClient(t, s, u)
			if _, err := c.Select("INBOX", nil).Wait(); err != nil {
				t.Fatal(err)
			}
			if err := c.Store(imap.UIDSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}, Silent: true}, nil).Close(); err != nil {
				t.Fatal(err)
			}
			if err := c.Store(imap.UIDSetNum(2), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close(); err != nil {
				t.Fatal(err)
			}
			if err := c.Expunge().Close(); err != nil {
				t.Fatal(err)
			}
			c.Close()
			result := r.sync().Accounts[0]
			if result.Updated != 1 || result.Deleted != 1 {
				t.Fatalf("after changes: %+v", result)
			}
			if result = r.sync().Accounts[0]; result.Updated != 0 || result.Deleted != 0 {
				t.Fatalf("after no changes: %+v", result)
			}
			files := markdownFiles(t, filepath.Join(r.out, "work"))
			if len(files) != 3 {
				t.Fatalf("files of deleted messages must stay: %d files", len(files))
			}
			deleted := 0
			for _, f := range files {
				if data, _ := os.ReadFile(f); bytes.Contains(data, []byte("deleted_on_server: ")) {
					deleted++
				}
			}
			if deleted != 1 {
				t.Fatalf("%d files are marked deleted_on_server, want 1", deleted)
			}
		})
	}
}
