package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/auth"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/mock"
	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/mailmd/internal/privatefs"
	"github.com/vincentsch/rungrad/conformance"
	"github.com/vincentsch/rungrad/docsgen"
	"github.com/vincentsch/rungrad/update"
)

type fakeRelease struct {
	calls *int
	err   error
}

func (f fakeRelease) Latest() (update.Release, error) {
	if f.calls != nil {
		*f.calls++
	}
	return update.Release{Version: "v0.1.0"}, f.err
}

func invoke(t *testing.T, opts Options, input string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if opts.HomeDir == "" {
		opts.HomeDir = t.TempDir()
	}
	if opts.UpdateFetcher == nil {
		opts.UpdateFetcher = fakeRelease{}
	}
	app := New(opts)
	if err := app.ValidateCatalog(); err != nil {
		t.Fatal(err)
	}
	code := app.RunIO(args, strings.NewReader(input), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// sweep fails when output contains a secret or message content.
func sweep(t *testing.T, values ...string) {
	t.Helper()
	for _, value := range values {
		for _, s := range []string{"PASSWORD_SENTINEL", "ACCESS_TOKEN_SENTINEL", "REFRESH_TOKEN_SENTINEL", "CLIENT_SECRET_SENTINEL", "DEVICE_CODE_SENTINEL", "BODY_SENTINEL", "ATTACHMENT_SENTINEL"} {
			if strings.Contains(value, s) {
				t.Fatalf("leaked %s in:\n%s", s, value)
			}
		}
	}
}

func registerMock(t *testing.T, root, name string) {
	t.Helper()
	_, err := accounts.Connect(context.Background(), root, name, "mock", func(ctx context.Context, stage string) (accounts.Settings, string, error) {
		return accounts.Settings{}, "", os.WriteFile(filepath.Join(root, "mailmd", stage, "mock.json"), []byte(`{}`), 0o600)
	}, privatefs.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
}

func mockService(n int) *mock.Service {
	s := &mock.Service{
		FolderRecords: []backend.Folder{
			{Path: "INBOX", ScopeKey: "inbox", Fetchable: true, Role: backend.RoleInbox},
			{Path: "Spam", ScopeKey: "spam", Fetchable: true, Role: backend.RoleJunk},
		},
		Items: map[string][]mock.Item{},
	}
	for i := 0; i < n; i++ {
		m := mail.Message{MessageID: "<" + string(rune('a'+i)) + "@example.invalid>", Subject: "Hello", BodyText: "BODY_SENTINEL",
			From: mail.Address{Email: "sender@example.invalid"}, Date: time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)}
		s.Items["inbox"] = append(s.Items["inbox"], mock.Item{Message: &m, RawBytes: 100})
	}
	return s
}

func mockOptions(root string, s *mock.Service) Options {
	return Options{ConfigRoot: root, Open: func(ctx context.Context, a accounts.AccountConfig, o accounts.OpenOptions) (backend.Backend, error) {
		return s.Open(ctx)
	}}
}

func TestCatalogManifestAndOutputModes(t *testing.T) {
	root := t.TempDir()
	opts := Options{ConfigRoot: root}
	code, first, stderr := invoke(t, opts, "", "__rungrad_manifest")
	if code != 0 || !strings.Contains(first, "rungrad-manifest/1") {
		t.Fatalf("manifest %d %s %s", code, first, stderr)
	}
	if _, second, _ := invoke(t, opts, "", "__rungrad_manifest"); first != second {
		t.Fatal("manifest is not deterministic")
	}
	_, help, _ := invoke(t, opts, "", "--help")
	if strings.Contains(help, "__rungrad_manifest") || strings.Contains(help, "--config ") {
		t.Fatalf("help shows hidden things:\n%s", help)
	}
	for _, args := range [][]string{{}, {"--json"}, {"--plain"}, {"--jq", ".accounts"}, {"--template", "{{len .accounts}}"}, {"--no-color", "--no-ansi", "--no-pager"}} {
		code, stdout, stderr := invoke(t, opts, "", append([]string{"list", "accounts"}, args...)...)
		if code != 0 {
			t.Fatalf("mode %v: %d %s %s", args, code, stdout, stderr)
		}
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatal("listing accounts created files")
	}
}

func TestConfigFlagIsRejected(t *testing.T) {
	code, _, stderr := invoke(t, Options{ConfigRoot: t.TempDir()}, "", "list", "accounts", "--config", "x.toml")
	if code != 1 || !strings.Contains(stderr, "--config is not supported") {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestUpdateCheckDoesNotTouchAccounts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "broken")
	if err := os.WriteFile(root, []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	opts := Options{ConfigRoot: root, UpdateFetcher: fakeRelease{calls: &calls}}
	code, stdout, stderr := invoke(t, opts, "", "update", "--check", "--json")
	if code != 0 || calls != 1 || !strings.Contains(stdout, "development_build") {
		t.Fatalf("update %d %s %s", code, stdout, stderr)
	}
	opts.UpdateFetcher = fakeRelease{err: errors.New("ACCESS_TOKEN_SENTINEL")}
	code, stdout, stderr = invoke(t, opts, "", "update", "--check")
	if code == 0 || !strings.Contains(stderr, "cannot reach GitHub") {
		t.Fatalf("failed update: %d %s %s", code, stdout, stderr)
	}
	sweep(t, stdout, stderr)
	if code, _, stderr := invoke(t, opts, "", "update"); code != 1 || !strings.Contains(stderr, "--check") {
		t.Fatalf("update without --check: %d %s", code, stderr)
	}
}

type rewriteTransport struct {
	base      string
	transport http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u, _ := url.Parse(r.base)
	clone.URL.Scheme, clone.URL.Host = u.Scheme, u.Host
	return r.transport.RoundTrip(clone)
}

// googleConsent completes the browser part of the Google sign-in by calling
// the local redirect address, like a browser would after consent.
func googleConsent(ctx context.Context, address string) error {
	u, err := url.Parse(address)
	if err != nil {
		return err
	}
	q := u.Query()
	target, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		return err
	}
	values := target.Query()
	values.Set("code", "fixture-code")
	values.Set("state", q.Get("state"))
	target.RawQuery = values.Encode()
	response, err := http.Get(target.String())
	if err == nil {
		response.Body.Close()
	}
	return err
}

func fakeGoogle(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			io.WriteString(w, `{"access_token":"ACCESS_TOKEN_SENTINEL","refresh_token":"REFRESH_TOKEN_SENTINEL","token_type":"Bearer","expires_in":3600,"scope":"https://www.googleapis.com/auth/gmail.readonly"}`)
		case "/gmail/v1/users/me/labels":
			io.WriteString(w, `{"labels":[{"id":"INBOX","name":"INBOX","type":"system"},{"id":"SPAM","name":"SPAM","type":"system"}]}`)
		case "/gmail/v1/users/me/profile":
			io.WriteString(w, `{"emailAddress":"dana@gmail.example","historyId":"7"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

const desktopClient = `{"installed":{"client_id":"fixture-client","client_secret":"CLIENT_SECRET_SENTINEL","auth_uri":"https://accounts.google.com/o/oauth2/v2/auth","token_uri":"https://oauth2.googleapis.com/token","redirect_uris":["http://localhost"]}}`

func TestConnectGmail(t *testing.T) {
	server := fakeGoogle(t)
	defer server.Close()
	root := t.TempDir()
	client := filepath.Join(root, "client_secret.json")
	if err := os.WriteFile(client, []byte(desktopClient), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := Options{ConfigRoot: root, HTTPClient: &http.Client{Transport: rewriteTransport{base: server.URL, transport: http.DefaultTransport}}, HandleAuthURL: googleConsent}
	code, stdout, stderr := invoke(t, opts, "", "connect", "gmail", "--client-secret", client)
	if code != 0 || !strings.Contains(stdout, "Connected gmail (dana@gmail.example).") || !strings.Contains(stdout, "Next: mailmd sync gmail") {
		t.Fatalf("gmail %d %s %s", code, stdout, stderr)
	}
	sweep(t, stdout, stderr)
	r, err := accounts.Load(root)
	if err != nil || len(r.Accounts) != 1 || r.Accounts[0].Name != "gmail" || r.Accounts[0].Email != "dana@gmail.example" {
		t.Fatalf("registry %v %v", r, err)
	}
	if code, _, stderr := invoke(t, Options{ConfigRoot: t.TempDir()}, "", "connect", "gmail"); code != 1 || !strings.Contains(stderr, "docs/gmail.md") {
		t.Fatalf("missing client secret: %d %s", code, stderr)
	}
}

func TestConnectGmailAgainUsesTheSavedClient(t *testing.T) {
	var secrets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			secrets = append(secrets, r.Form.Get("client_id")+" "+r.Form.Get("client_secret"))
			io.WriteString(w, `{"access_token":"ACCESS_TOKEN_SENTINEL","refresh_token":"REFRESH_TOKEN_SENTINEL","token_type":"Bearer","expires_in":3600,"scope":"https://www.googleapis.com/auth/gmail.readonly"}`)
		case "/gmail/v1/users/me/labels":
			io.WriteString(w, `{"labels":[{"id":"INBOX","name":"INBOX","type":"system"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	opts := Options{ConfigRoot: root, HTTPClient: &http.Client{Transport: rewriteTransport{base: server.URL, transport: http.DefaultTransport}}, HandleAuthURL: googleConsent}
	client := filepath.Join(t.TempDir(), "client_secret.json")
	if err := os.WriteFile(client, []byte(desktopClient), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := invoke(t, opts, "", "connect", "gmail", "--account", "personal", "--client-secret", client); code != 0 {
		t.Fatalf("first connect %d %s %s", code, stdout, stderr)
	}
	// The downloaded file is gone. Connecting again needs only the name.
	if err := os.Remove(client); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := invoke(t, opts, "", "connect", "gmail", "--account", "personal", "--dry-run"); code != 0 || !strings.Contains(stdout, "OAuth client saved for personal") {
		t.Fatalf("dry run %d %s %s", code, stdout, stderr)
	}
	code, stdout, stderr := invoke(t, opts, "", "connect", "gmail", "--account", "personal")
	if code != 0 || !strings.Contains(stdout, "Connected personal") {
		t.Fatalf("connect again %d %s %s", code, stdout, stderr)
	}
	sweep(t, stdout, stderr)
	// A new client file replaces the saved client.
	other := filepath.Join(t.TempDir(), "other.json")
	otherClient := strings.NewReplacer("fixture-client", "other-client", "CLIENT_SECRET_SENTINEL", "OTHER_SECRET").Replace(desktopClient)
	if err := os.WriteFile(other, []byte(otherClient), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--client-secret", other}, nil} {
		if code, stdout, stderr := invoke(t, opts, "", append([]string{"connect", "gmail", "--account", "personal"}, args...)...); code != 0 {
			t.Fatalf("connect %v: %d %s %s", args, code, stdout, stderr)
		}
	}
	want := []string{"fixture-client CLIENT_SECRET_SENTINEL", "fixture-client CLIENT_SECRET_SENTINEL", "other-client OTHER_SECRET", "other-client OTHER_SECRET"}
	if strings.Join(secrets, ",") != strings.Join(want, ",") {
		t.Fatalf("token requests used %v, want %v", secrets, want)
	}
}

func TestConnectOutlookAgainUsesTheSavedClientID(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/devicecode"):
			_ = r.ParseForm()
			requests = append(requests, r.URL.Path+" "+r.Form.Get("client_id"))
			io.WriteString(w, `{"device_code":"DEVICE_CODE_SENTINEL","user_code":"ABCD-EFGH","verification_uri":"https://microsoft.com/devicelogin","expires_in":600,"interval":1}`)
		case strings.HasSuffix(r.URL.Path, "/token"):
			io.WriteString(w, `{"access_token":"ACCESS_TOKEN_SENTINEL","refresh_token":"REFRESH_TOKEN_SENTINEL","token_type":"Bearer","expires_in":3600,"scope":"openid profile offline_access https://graph.microsoft.com/Mail.Read"}`)
		case r.URL.Path == "/v1.0/me/mailFolders":
			io.WriteString(w, `{"value":[{"id":"in","displayName":"Inbox"}]}`)
		case strings.HasSuffix(r.URL.Path, "/childFolders"):
			io.WriteString(w, `{"value":[]}`)
		case r.URL.Path == "/v1.0/me/mailFolders/inbox":
			io.WriteString(w, `{"id":"in"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"code":"ErrorFolderNotFound"}}`)
		}
	}))
	defer server.Close()
	opts := Options{ConfigRoot: t.TempDir(), IdentityBaseURL: server.URL, GraphBaseURL: server.URL + "/v1.0", HTTPClient: server.Client(),
		HandleDeviceCode: func(context.Context, auth.DeviceCodePrompt) error { return nil }}
	for _, args := range [][]string{
		{"--client-id", "12345678-1234-1234-1234-123456789abc", "--tenant", "consumers"},
		nil,
		{"--tenant", "organizations"},
	} {
		if code, stdout, stderr := invoke(t, opts, "", append([]string{"connect", "outlook", "--account", "work"}, args...)...); code != 0 {
			t.Fatalf("connect %v: %d %s %s", args, code, stdout, stderr)
		}
	}
	want := []string{
		"/consumers/oauth2/v2.0/devicecode 12345678-1234-1234-1234-123456789abc",
		"/consumers/oauth2/v2.0/devicecode 12345678-1234-1234-1234-123456789abc",
		"/organizations/oauth2/v2.0/devicecode 12345678-1234-1234-1234-123456789abc",
	}
	if strings.Join(requests, ",") != strings.Join(want, ",") {
		t.Fatalf("sign-in requests %v, want %v", requests, want)
	}
}

// fakeMicrosoft answers the device code sign-in and the Graph calls of
// connect. me is the answer to /me, or "" for a 403 like Microsoft sends
// without the User.Read permission.
func fakeMicrosoft(t *testing.T, me string) *httptest.Server {
	// An ID token with the payload {"preferred_username":"dana@signin.example"}.
	idToken := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(`{"preferred_username":"dana@signin.example"}`)) + ".sig"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/devicecode"):
			io.WriteString(w, `{"device_code":"DEVICE_CODE_SENTINEL","user_code":"ABCD-EFGH","verification_uri":"https://microsoft.com/devicelogin","expires_in":600,"interval":1}`)
		case strings.HasSuffix(r.URL.Path, "/token"):
			io.WriteString(w, `{"access_token":"ACCESS_TOKEN_SENTINEL","refresh_token":"REFRESH_TOKEN_SENTINEL","token_type":"Bearer","expires_in":3600,"scope":"openid profile offline_access https://graph.microsoft.com/Mail.Read","id_token":"`+idToken+`"}`)
		case r.URL.Path == "/v1.0/me" && me == "":
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":{"code":"Authorization_RequestDenied"}}`)
		case r.URL.Path == "/v1.0/me":
			io.WriteString(w, me)
		case r.URL.Path == "/v1.0/me/mailFolders":
			io.WriteString(w, `{"value":[{"id":"in","displayName":"Inbox"},{"id":"junk","displayName":"Junk Email"}]}`)
		case strings.HasSuffix(r.URL.Path, "/childFolders"):
			io.WriteString(w, `{"value":[]}`)
		case r.URL.Path == "/v1.0/me/mailFolders/inbox":
			io.WriteString(w, `{"id":"in"}`)
		case r.URL.Path == "/v1.0/me/mailFolders/junkemail":
			io.WriteString(w, `{"id":"junk"}`)
		case strings.HasPrefix(r.URL.Path, "/v1.0/me/mailFolders/"):
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"code":"ErrorFolderNotFound"}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func TestConnectOutlook(t *testing.T) {
	server := fakeMicrosoft(t, `{"mail":"dana@contoso.example","userPrincipalName":"dana@contoso.onmicrosoft.example"}`)
	defer server.Close()
	root := t.TempDir()
	prompted := false
	opts := Options{ConfigRoot: root, IdentityBaseURL: server.URL, GraphBaseURL: server.URL + "/v1.0", HTTPClient: server.Client(),
		HandleDeviceCode: func(ctx context.Context, p auth.DeviceCodePrompt) error { prompted = true; return nil }}
	code, stdout, stderr := invoke(t, opts, "", "connect", "outlook", "--client-id", "12345678-1234-1234-1234-123456789abc", "--json")
	if code != 0 || !prompted {
		t.Fatalf("outlook %d %s %s", code, stdout, stderr)
	}
	sweep(t, stdout, stderr)
	var result ConnectResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.Account != "outlook" || result.Backend != "graph" || strings.Join(result.LeftOut, ",") != "Junk Email" || result.Address != "dana@contoso.example" {
		t.Fatalf("result %s %v", stdout, err)
	}
}

func TestConnectOutlookFallsBackToTheSignInName(t *testing.T) {
	server := fakeMicrosoft(t, "")
	defer server.Close()
	opts := Options{ConfigRoot: t.TempDir(), IdentityBaseURL: server.URL, GraphBaseURL: server.URL + "/v1.0", HTTPClient: server.Client(),
		HandleDeviceCode: func(context.Context, auth.DeviceCodePrompt) error { return nil }}
	code, stdout, stderr := invoke(t, opts, "", "connect", "outlook", "--client-id", "12345678-1234-1234-1234-123456789abc")
	if code != 0 || !strings.Contains(stdout, "Connected outlook (dana@signin.example).") {
		t.Fatalf("outlook %d %s %s", code, stdout, stderr)
	}
}

func TestConnectOutlookExplainsSignInProblems(t *testing.T) {
	for _, c := range []struct {
		name, devicecode, token, want string
	}{
		{"public client flows off", "", `{"error":"invalid_client","error_codes":[7000218]}`, `"Allow public client flows" to "Enabled"`},
		{"declined", "", `{"error":"authorization_declined"}`, "you declined the request"},
		{"code expired", "", `{"error":"expired_token"}`, "the code expired"},
		{"rejected", "", `{"error":"invalid_grant"}`, "Microsoft did not accept the sign-in."},
		{"unknown client ID", `{"error":"unauthorized_client","error_codes":[700016]}`, "", "Check --client-id"},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/devicecode") && c.devicecode != "":
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, c.devicecode)
				case strings.HasSuffix(r.URL.Path, "/devicecode"):
					io.WriteString(w, `{"device_code":"DEVICE_CODE_SENTINEL","user_code":"ABCD-EFGH","verification_uri":"https://microsoft.com/devicelogin","expires_in":600,"interval":1}`)
				case strings.HasSuffix(r.URL.Path, "/token"):
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, c.token)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			opts := Options{ConfigRoot: t.TempDir(), IdentityBaseURL: server.URL, GraphBaseURL: server.URL + "/v1.0", HTTPClient: server.Client(),
				HandleDeviceCode: func(context.Context, auth.DeviceCodePrompt) error { return nil }}
			code, _, stderr := invoke(t, opts, "", "connect", "outlook", "--client-id", "12345678-1234-1234-1234-123456789abc")
			if code != 3 || !strings.Contains(stderr, c.want) || strings.Contains(stderr, "The login of  is") {
				t.Fatalf("connect = %d %s", code, stderr)
			}
		})
	}
}

func TestGeneratedDocumentationMatchesCatalog(t *testing.T) {
	for name, data := range docsgen.Generate(New(Options{})) {
		actual, err := os.ReadFile(filepath.Join("..", "..", "docs", "commands", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, []byte(data)) {
			t.Fatalf("docs/commands/%s is out of date; run make docs", name)
		}
	}
}

func TestOfflineConformance(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	for _, name := range []string{"fixture", "forbidden", "broken"} {
		registerMock(t, root, name)
	}
	brokenState := filepath.Join(out, "broken", ".mailmd", "state.json")
	if err := os.MkdirAll(filepath.Dir(brokenState), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brokenState, []byte(`{"schema":"malformed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "mailmd-offline")
	build := exec.Command("go", "build", "-tags", "mailmd_mock", "-o", exe, "./cmd/mailmd")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	runner, err := conformance.NewRunner(conformance.Target{
		Path:         exe,
		Read:         []string{"list", "accounts", "--config-root", root},
		Mutate:       []string{"sync", "fixture", "--out", out, "--config-root", root},
		NotFound:     []string{"status", "missing", "--out", out, "--config-root", root},
		APIError:     []string{"status", "broken", "--out", out, "--config-root", root},
		Forbidden:    []string{"sync", "forbidden", "--out", out, "--config-root", root},
		HasUpdate:    true,
		ManifestMode: conformance.ManifestModeRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	rules, err := conformance.DefaultRuleset()
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.DiscoverManifest(); err != nil {
		t.Fatal(err)
	}
	for _, rule := range runner.Score(rules).Rules {
		// mailmd has no config file, so it hides rungrad's --config flag.
		if rule.ID == "auth.config-flag" {
			continue
		}
		if rule.Result == conformance.ResultFail {
			t.Errorf("conformance %s: %s", rule.ID, rule.Reason)
		}
	}
}

func TestConfigFolderCanBeASymbolicLink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	registerMock(t, real, "work")
	link := filepath.Join(dir, "config")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symbolic links are not available:", err)
	}
	code, stdout, stderr := invoke(t, Options{ConfigRoot: link}, "", "list", "accounts")
	if code != 0 || !strings.Contains(stdout, "work") {
		t.Fatalf("list accounts: %d %s %s", code, stdout, stderr)
	}
	// A link inside the config folder is refused, with a message that says so.
	list := filepath.Join(real, "mailmd", "accounts.json")
	if err := os.Rename(list, list+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(list+".real", list); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = invoke(t, Options{ConfigRoot: link}, "", "list", "accounts")
	if code == 0 || !strings.Contains(stderr, "does not follow symbolic links") {
		t.Fatalf("linked account list: %d %s", code, stderr)
	}
}

func TestConnectGmailOpensABrowserOnlyForAPerson(t *testing.T) {
	server := fakeGoogle(t)
	defer server.Close()
	yes := true
	for name, tc := range map[string]struct {
		interactive *bool
		args        []string
		opened      bool
	}{
		"script":     {nil, nil, false},
		"no-browser": {&yes, []string{"--no-browser"}, false},
		"terminal":   {&yes, nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			client := filepath.Join(root, "client_secret.json")
			if err := os.WriteFile(client, []byte(desktopClient), 0o600); err != nil {
				t.Fatal(err)
			}
			opened := false
			opts := Options{ConfigRoot: root, Interactive: tc.interactive, HandleAuthURL: googleConsent,
				HTTPClient:  &http.Client{Transport: rewriteTransport{base: server.URL, transport: http.DefaultTransport}},
				OpenBrowser: func(context.Context, string) error { opened = true; return nil }}
			code, stdout, stderr := invoke(t, opts, "", append([]string{"connect", "gmail", "--client-secret", client}, tc.args...)...)
			if code != 0 || opened != tc.opened || strings.Contains(stderr, "opens this address") != tc.opened {
				t.Fatalf("code %d opened %v\n%s\n%s", code, opened, stdout, stderr)
			}
		})
	}
}

func TestConnectGmailWithoutATerminalStopsWaiting(t *testing.T) {
	server := fakeGoogle(t)
	defer server.Close()
	root := t.TempDir()
	client := filepath.Join(root, "client_secret.json")
	if err := os.WriteFile(client, []byte(desktopClient), 0o600); err != nil {
		t.Fatal(err)
	}
	// Nobody opens the address.
	opts := Options{ConfigRoot: root, GmailWaitLimit: 100 * time.Millisecond, HandleAuthURL: func(context.Context, string) error { return nil },
		HTTPClient: &http.Client{Transport: rewriteTransport{base: server.URL, transport: http.DefaultTransport}}}
	code, _, stderr := invoke(t, opts, "", "connect", "gmail", "--client-secret", client)
	if code != 3 || !strings.Contains(stderr, "Waiting up to") || !strings.Contains(stderr, "nobody allowed access in the browser within 100ms") || !strings.Contains(stderr, "Run the connect command again") {
		t.Fatalf("connect = %d\n%s", code, stderr)
	}
	if r, err := accounts.Load(root); err != nil || len(r.Accounts) != 0 {
		t.Fatalf("accounts after the time ran out = %+v, %v", r.Accounts, err)
	}
}

func TestConnectNamesAnAccountOfAnotherProvider(t *testing.T) {
	root := t.TempDir()
	registerMock(t, root, "work")
	for _, args := range [][]string{{"connect", "gmail", "--account", "work"}, {"connect", "outlook", "--account", "work"}} {
		code, _, stderr := invoke(t, Options{ConfigRoot: root}, "", args...)
		if code != 1 || !strings.Contains(stderr, "The account work already exists and uses") || strings.Contains(stderr, "Missing") {
			t.Fatalf("%v: %d %s", args, code, stderr)
		}
	}
}

func TestConfigRootCanNameTheMailmdFolder(t *testing.T) {
	root := t.TempDir()
	registerMock(t, root, "work")
	for _, given := range []string{root, filepath.Join(root, "mailmd")} {
		code, stdout, stderr := invoke(t, Options{}, "", "list", "accounts", "--config-root", given)
		if code != 0 || !strings.Contains(stdout, "work") {
			t.Fatalf("--config-root %s: %d %s %s", given, code, stdout, stderr)
		}
	}
}

func TestConnectGmailExplainsAMissingClientSecret(t *testing.T) {
	root := t.TempDir()
	client := filepath.Join(root, "client_secret.json")
	masked := strings.Replace(desktopClient, "CLIENT_SECRET_SENTINEL", "", 1)
	if err := os.WriteFile(client, []byte(masked), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := invoke(t, Options{ConfigRoot: root}, "", "connect", "gmail", "--client-secret", client, "--dry-run")
	if code != 1 || !strings.Contains(stderr, "no client_secret") || !strings.Contains(stderr, "only once") {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestMistypedCommandSuggestsTheRightOne(t *testing.T) {
	code, _, stderr := invoke(t, Options{ConfigRoot: t.TempDir()}, "", "snyc")
	if code != 1 || !strings.Contains(stderr, "Did you mean this?") || !strings.HasSuffix(strings.TrimSpace(stderr), "\tsync") {
		t.Fatalf("%d %q", code, stderr)
	}
}
