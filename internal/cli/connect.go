package cli

import (
	"bufio"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/auth"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/imap"
	"github.com/vincentsch/mailmd/internal/presets"
	mdsync "github.com/vincentsch/mailmd/internal/sync"
	rg "github.com/vincentsch/rungrad"
	"github.com/vincentsch/rungrad/output"
	"golang.org/x/term"
)

// ConnectResult is the JSON result of the connect commands.
type ConnectResult struct {
	Schema   string `json:"schema"`
	Account  string `json:"account"`
	Backend  string `json:"backend"`
	Provider string `json:"provider"`
	Address  string `json:"address"`
	Archive  string `json:"archive"`
	DryRun   bool   `json:"dry_run"`
	// Folders counts the folders found on the server. LeftOut names those a
	// sync leaves out by default.
	Folders int      `json:"folders"`
	LeftOut []string `json:"left_out"`
	// CAFile is the saved PEM file with more certificates to trust.
	CAFile string `json:"ca_file,omitempty"`
	Next   string `json:"next"`
	// Note is a provider-specific remark, such as Gmail's All Mail folder.
	Note string `json:"note,omitempty"`
}

func (m module) connectCommands(commands map[string]*rg.Command) {
	imapCmd := commands["connect imap"]
	imapCmd.Configure = func(c *cobra.Command) {
		c.Flags().String("email", "", "Your email address. mailmd fills in the server settings of known providers")
		c.Flags().String("account", "", "Name for this account, also used as its archive folder (default: from the provider or address)")
		c.Flags().String("host", "", "IMAP server, like imap.example.com")
		c.Flags().Int("port", 0, "IMAP port (default: 993, or 143 with --tls starttls)")
		c.Flags().String("username", "", "Login name (default: the email address)")
		c.Flags().String("tls", "implicit", "How to encrypt the connection: implicit or starttls")
		c.Flags().String("ca-file", "", "PEM file with the certificate of your server's own certificate authority. mailmd trusts it in addition to the system's certificates and saves it with the account")
		c.Flags().Bool("password-stdin", false, "Read the password from the first line of standard input")
		c.Flags().String("out", "", "Folder for the archive (default: ~/mail)")
		addFilterFlags(c, true)
	}
	imapCmd.Run = m.connectIMAP

	gmailCmd := commands["connect gmail"]
	gmailCmd.Configure = func(c *cobra.Command) {
		c.Flags().String("client-secret", "", "The OAuth client JSON file you downloaded from Google Cloud (not needed to connect an account again)")
		c.Flags().String("account", "", "Name for this account, also used as its archive folder (default: gmail)")
		c.Flags().Bool("no-browser", false, "Don't open a browser, only print the sign-in address")
		c.Flags().String("out", "", "Folder for the archive (default: ~/mail)")
		addFilterFlags(c, true)
	}
	gmailCmd.Run = m.connectGmail

	outlookCmd := commands["connect outlook"]
	outlookCmd.Configure = func(c *cobra.Command) {
		c.Flags().String("client-id", "", "The application (client) ID of your Microsoft app registration (not needed to connect an account again)")
		c.Flags().String("tenant", "common", "Who can sign in: common, organizations, consumers, or your tenant ID")
		c.Flags().String("account", "", "Name for this account, also used as its archive folder (default: outlook)")
		c.Flags().String("out", "", "Folder for the archive (default: ~/mail)")
		addFilterFlags(c, true)
	}
	outlookCmd.Run = m.connectOutlook
}

// interactive reports whether connect may ask questions.
func (m module) interactive(f *rg.Factory) bool {
	if f.Flags != nil && (f.Flags.NoPrompt || f.Flags.JSON || f.Flags.JQ != "" || f.Flags.Template != "") {
		return false
	}
	if m.opts.Interactive != nil {
		return *m.opts.Interactive
	}
	return isTerminal(f.Stdin) && isTerminal(f.Stderr)
}

// asker reads answers from stdin, one line each.
type asker struct {
	ctx context.Context
	in  *bufio.Reader
	out io.Writer
}

func (a *asker) ask(question, fallback string) (string, error) {
	if fallback != "" {
		fmt.Fprintf(a.out, "%s [%s]: ", question, fallback)
	} else {
		fmt.Fprintf(a.out, "%s: ", question)
	}
	line, err := readLine(a.ctx, a.in)
	if a.ctx.Err() != nil {
		fmt.Fprintln(a.out)
		return "", a.ctx.Err()
	}
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", newUserError(rg.ExitUsage, "No answer was given", "")
	}
	if answer := strings.TrimSpace(line); answer != "" {
		return answer, nil
	}
	return fallback, nil
}

// readLine reads one line, but returns as soon as ctx ends, so Ctrl-C works
// at a prompt. The read goes on in the background until the program ends.
func readLine(ctx context.Context, in *bufio.Reader) (string, error) {
	type answer struct {
		line string
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		line, err := in.ReadString('\n')
		done <- answer{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case a := <-done:
		return a.line, a.err
	}
}

// connectOptions are the settings every connect command shares.
type connectOptions struct {
	root    string
	archive string
	filter  mdsync.Filter
	since   string
}

func (m module) readConnectOptions(c *cobra.Command) (connectOptions, error) {
	root, err := m.configRoot(c)
	if err != nil {
		return connectOptions{}, err
	}
	opts := connectOptions{root: root, since: flagString(c, "since")}
	if out := flagString(c, "out"); out != "" {
		if opts.archive, err = absPath(out, m.home()); err != nil {
			return connectOptions{}, err
		}
	}
	opts.filter = mdsync.Filter{Folders: patterns(c, "folder"), Include: patterns(c, "include"), Exclude: patterns(c, "exclude")}
	if err := opts.filter.Validate(); err != nil {
		return connectOptions{}, newUserError(rg.ExitUsage, upperFirst(err.Error()), "")
	}
	if _, err := mdsync.ParseSince(opts.since, m.opts.Clock()); err != nil {
		return connectOptions{}, newUserError(rg.ExitUsage, upperFirst(err.Error()), "")
	}
	return opts, nil
}

// patterns returns the folder patterns of a connect flag. An empty value
// clears the list that is saved for the flag.
func patterns(c *cobra.Command, name string) []string {
	var out []string
	for _, p := range flagStrings(c, name) {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// savedFilter returns the folder filter saved with an account.
func savedFilter(a accounts.AccountConfig) mdsync.Filter {
	return mdsync.Filter{Folders: a.Folders, Include: a.Include, Exclude: a.Exclude}
}

// imapSettings collects the IMAP settings from flags, presets and prompts.
type imapSettings struct {
	account, email, host, username, tls, caFile string
	port                                        int
	preset                                      presets.Preset
	known                                       bool
}

func (m module) connectIMAP(f *rg.Factory, c *cobra.Command, args []string) error {
	if err := checkGlobals(f); err != nil {
		return err
	}
	opts, err := m.readConnectOptions(c)
	if err != nil {
		return err
	}
	registry, err := accounts.Load(opts.root)
	if err != nil {
		return registryError(err)
	}
	interactive := m.interactive(f)
	ask := &asker{ctx: c.Context(), in: bufio.NewReader(f.Stdin), out: f.Stderr}
	s, err := m.imapSettings(c, registry, interactive, ask)
	if err != nil {
		return connectError(err, "imap")
	}
	if s.caFile, err = m.caFile(c, registry, s.account); err != nil {
		return err
	}
	settings := accounts.Settings{Host: s.host, Port: s.port, Username: s.username, TLSMode: s.tls, CAFile: s.caFile}
	// IMAP has no way to ask for the address. A login name without an @
	// says nothing about it, so the address stays empty then.
	email := s.email
	if email == "" && strings.Contains(s.username, "@") {
		email = s.username
	}

	// A connected account keeps its saved password when the command only
	// changes settings, like the folders or --out. A new password, or
	// another server or login name, needs the password.
	old, saved := savedAccount(opts.root, s.account, "imap")
	problem := ""
	if saved && !m.passwordGiven(c) && strings.EqualFold(old.Settings.Host, s.host) && old.Settings.Username == s.username && changesSettings(c, "ca-file", "tls", "port") {
		next := old
		next.Settings = settings
		if email != "" {
			next.Email = email
		}
		done, err := m.changeSettings(f, c, old, next, opts)
		if done {
			return err
		}
		problem = mdsync.Describe(err, old).Message
	}

	// Everything else is checked. Only now mailmd asks for the password.
	if s.known && s.preset.Password != "" && interactive {
		fmt.Fprintln(f.Stderr, s.preset.Password)
	}
	password, err := m.readPassword(f, c, s, problem)
	if err != nil {
		return connectError(err, "imap")
	}
	f.RegisterSecret(password)

	cfg := imap.Config{Host: s.host, Port: s.port, Username: s.username, Password: password, TLSMode: imap.TLSMode(s.tls), RootCAs: m.opts.RootCAs, CAFile: s.caFile}
	if flagBool(c, "verbose") {
		cfg.Log = f.Stderr
	}
	var folders []backend.Folder
	probe := func(ctx context.Context, stage string) (accounts.Settings, string, error) {
		if err := accounts.StoreIMAPCredential(opts.root, stage, password); err != nil {
			return settings, "", err
		}
		account := accounts.AccountConfig{Name: s.account, Backend: "imap", ArchiveDir: stage, CredentialRef: stage + "/imap.json", Settings: settings}
		folders, _, err = m.probe(ctx, account, accounts.OpenOptions{ConfigRoot: opts.root, RootCAs: m.opts.RootCAs, Log: cfg.Log})
		var u *userError
		if err == nil && errors.As(checkFolders(opts, account, folders), &u) {
			// A typo in --folder stops connect before anything is saved.
			// Fixing it costs only the password prompt again.
			u.info.Message = strings.TrimSuffix(u.info.Message, ".") + ". Nothing was saved."
			u.info.Hint = strings.TrimSuffix(u.info.Hint, ".") + ". Run the connect command again with the right folder."
			return settings, email, u
		}
		return settings, email, err
	}
	if f.DryRun() {
		b, err := imap.Open(c.Context(), cfg)
		if err != nil {
			return connectError(err, "imap")
		}
		folders, err = b.Folders(c.Context())
		_ = b.Close()
		if err != nil {
			return connectError(err, "imap")
		}
		account, err := plannedAccount(opts.root, s.account, "imap")
		if err != nil {
			return err
		}
		account.Settings, account.Email = settings, email
		applySettings(c, opts, &account)
		if err := checkFolders(opts, account, folders); err != nil {
			return err
		}
		return m.writeConnect(f, c, account, folders, connectReport{dry: true})
	}
	account, err := accounts.Connect(c.Context(), opts.root, s.account, "imap", probe, m.opts.ConnectHooks)
	if err != nil {
		return connectError(err, "imap")
	}
	return m.finishConnect(f, c, account, opts, folders)
}

// passwordGiven reports whether connect imap got a password on stdin or in
// the environment.
func (m module) passwordGiven(c *cobra.Command) bool {
	return flagBool(c, "password-stdin") || m.opts.Getenv("MAILMD_IMAP_PASSWORD") != ""
}

// settingFlags are the connect flags that change what a sync does. A
// command that gives only these, and no new login, keeps the saved login.
var settingFlags = []string{"out", "folder", "include", "exclude", "since", "keep-raw", "prefer-text"}

// changesSettings reports whether the command gives one of the setting
// flags, or one of the extra flags of a provider, like --ca-file for IMAP.
func changesSettings(c *cobra.Command, extra ...string) bool {
	for _, name := range append(append([]string(nil), settingFlags...), extra...) {
		if c.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// applySettings puts the settings that the command gives into the account.
func applySettings(c *cobra.Command, opts connectOptions, a *accounts.AccountConfig) {
	if opts.archive != "" {
		a.Root = opts.archive
	}
	// Each folder flag replaces the list saved for it. So --include Junk
	// keeps the saved --exclude.
	if c.Flags().Changed("folder") {
		a.Folders = opts.filter.Folders
	}
	if c.Flags().Changed("include") {
		a.Include = opts.filter.Include
	}
	if c.Flags().Changed("exclude") {
		a.Exclude = opts.filter.Exclude
	}
	if c.Flags().Changed("since") {
		a.Since = opts.since
	}
	if c.Flags().Changed("keep-raw") {
		a.KeepRaw = flagBool(c, "keep-raw")
	}
	if c.Flags().Changed("prefer-text") {
		a.PreferText = flagBool(c, "prefer-text")
	}
}

// changeSettings saves the settings that a connect command gives for a
// connected account, and keeps the saved login. So connect asks for no
// password and no sign-in. It logs in with the saved login first, which
// checks the folder names and a new CA file. next is the account with its
// new provider settings. When the saved login doesn't work, done is false
// and err says why, and the caller asks for a new login instead.
func (m module) changeSettings(f *rg.Factory, c *cobra.Command, old, next accounts.AccountConfig, opts connectOptions) (done bool, err error) {
	policy := auth.PersistTokens
	if f.DryRun() {
		policy = auth.MemoryOnlyTokens
	}
	var log io.Writer
	if flagBool(c, "verbose") {
		log = f.Stderr
	}
	b, err := m.open(c.Context(), opts.root, next, policy, log)
	var folders []backend.Folder
	if err == nil {
		folders, err = b.Folders(c.Context())
	}
	err = errors.Join(err, backend.Close(b))
	if err != nil {
		if loginFailed(err) {
			return false, err
		}
		return true, accountProblem(err, next)
	}
	applySettings(c, opts, &next)
	if err := checkFolders(opts, next, folders); err != nil {
		var u *userError
		if errors.As(err, &u) {
			u.info.Message = strings.TrimSuffix(u.info.Message, ".") + ". Nothing was saved."
		}
		return true, err
	}
	if !f.DryRun() {
		if next, err = accounts.Update(opts.root, old.Name, func(a *accounts.AccountConfig) { *a = next }); err != nil {
			return true, newUserError(rg.ExitAPI, "The new settings of "+old.Name+" could not be saved", "Run the same connect command again.")
		}
	}
	return true, m.writeConnect(f, c, next, folders, connectReport{dry: f.DryRun(), keptLogin: true})
}

// loginFailed reports whether the saved login of an account can't be used,
// so connect needs a new one.
func loginFailed(err error) bool {
	switch mdsync.Category(err) {
	case mdsync.CategoryAuth, mdsync.CategoryCredential:
		return true
	}
	return errors.Is(err, auth.ErrOAuthSetup) || errors.Is(err, auth.ErrInsecurePermission)
}

func (m module) imapSettings(c *cobra.Command, registry accounts.Registry, interactive bool, ask *asker) (imapSettings, error) {
	s := imapSettings{
		account:  strings.TrimSpace(flagString(c, "account")),
		email:    strings.TrimSpace(flagString(c, "email")),
		host:     strings.TrimSpace(flagString(c, "host")),
		username: strings.TrimSpace(flagString(c, "username")),
		tls:      strings.ToLower(strings.TrimSpace(flagString(c, "tls"))),
	}
	s.port, _ = c.Flags().GetInt("port")
	// Connecting an existing account again, for a new password, needs only
	// its name. The saved settings fill in the rest.
	if old, err := registry.Find(s.account); err == nil && s.account != "" && old.Backend == "imap" && s.email == "" && s.host == "" && s.username == "" {
		s.account, s.host, s.username = old.Name, old.Settings.Host, old.Settings.Username
		if s.port == 0 {
			s.port = old.Settings.Port
		}
		if !c.Flags().Changed("tls") {
			s.tls = old.Settings.TLSMode
		}
		s.email = old.Email
		s.preset, s.known = presets.ForHost(s.host)
		return s, nil
	}
	if s.tls != "implicit" && s.tls != "starttls" {
		return s, newUserError(rg.ExitUsage, fmt.Sprintf("--tls must be implicit or starttls, not %q", s.tls), "Most servers use implicit TLS on port 993.")
	}
	if s.port < 0 || s.port > 65535 {
		return s, newUserError(rg.ExitUsage, "--port must be between 1 and 65535", "")
	}
	var err error
	if interactive && s.email == "" && s.host == "" {
		if s.email, err = ask.ask("Email address", ""); err != nil {
			return s, err
		}
	}
	if s.email != "" && !strings.Contains(s.email, "@") {
		return s, newUserError(rg.ExitUsage, fmt.Sprintf("%q is not an email address", s.email), "")
	}
	if s.preset, s.known = presets.ForEmail(s.email); !s.known && s.host != "" {
		s.preset, s.known = presets.ForHost(s.host)
	}
	if s.known && s.preset.UseOutlook {
		return s, newUserError(rg.ExitUsage,
			"Microsoft no longer accepts passwords over IMAP for Outlook.com and most Microsoft 365 mailboxes",
			"Connect it through Microsoft Graph instead: mailmd connect outlook --client-id YOUR-APP-ID. The Outlook guide shows how to get the ID: https://github.com/vincentsch/mailmd/blob/main/docs/outlook.md")
	}
	if s.host == "" && s.known {
		s.host = s.preset.Host
		if !c.Flags().Changed("tls") {
			s.tls = s.preset.TLS
		}
		if s.port == 0 {
			s.port = s.preset.Port
		}
	}
	if s.host == "" && interactive {
		if s.host, err = ask.ask("IMAP server (like imap.example.com)", guessHost(s.email)); err != nil {
			return s, err
		}
	}
	if s.username == "" {
		s.username = s.email
	}
	if s.username == "" && interactive {
		if s.username, err = ask.ask("Login name", ""); err != nil {
			return s, err
		}
	}
	var missing []string
	if s.host == "" {
		missing = append(missing, "--host")
	}
	if s.username == "" {
		missing = append(missing, "--username")
	}
	if len(missing) > 0 {
		return s, newUserError(rg.ExitUsage, "Missing "+strings.Join(missing, " and "),
			"Give your email address, and mailmd fills in the settings of known providers: mailmd connect imap --email you@example.com. For other servers, also add --host imap.example.com.")
	}
	if s.port == 0 {
		s.port = imap.DefaultPort(imap.TLSMode(s.tls))
	}
	if s.account == "" {
		s.account = defaultAccountName(registry, s)
		if interactive {
			if s.account, err = ask.ask("Account name", s.account); err != nil {
				return s, err
			}
		}
	}
	if old, err := registry.Find(s.account); err == nil && old.Backend != "imap" {
		return s, newUserError(rg.ExitUsage, fmt.Sprintf("The account %s already exists and uses %s", old.Name, providerName(old.Backend)), "Choose another name with --account.")
	}
	return s, nil
}

// caFile returns the CA file for the account: the one from --ca-file, or
// the one saved with the account. An empty --ca-file removes it. The file is
// checked here, before mailmd asks for the password.
func (m module) caFile(c *cobra.Command, registry accounts.Registry, account string) (string, error) {
	if !c.Flags().Changed("ca-file") {
		if old, err := registry.Find(account); err == nil && old.Backend == "imap" {
			return old.Settings.CAFile, nil
		}
		return "", nil
	}
	value := strings.TrimSpace(flagString(c, "ca-file"))
	if value == "" {
		return "", nil
	}
	path, err := absPath(value, m.home())
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(path, "\x00\r\n") {
		return "", newUserError(rg.ExitUsage, "--ca-file is not a usable path", "")
	}
	if _, err := imap.CAPool(path, x509.NewCertPool()); err != nil {
		var problem *backend.Problem
		if errors.As(err, &problem) {
			return "", newUserError(rg.ExitUsage, strings.TrimSuffix(problem.Text, "."), problem.Hint)
		}
		return "", err
	}
	return path, nil
}

// guessHost suggests imap.<domain> for an unknown provider.
func guessHost(email string) string {
	if _, domain, ok := strings.Cut(email, "@"); ok && domain != "" {
		return "imap." + domain
	}
	return ""
}

// defaultAccountName picks a short name like "gmail" or "example". It reuses
// the name of an existing account for the same login, so connecting again
// refreshes it.
func defaultAccountName(r accounts.Registry, s imapSettings) string {
	base := ""
	address := s.email
	if address == "" {
		address = s.username
	}
	switch {
	case s.known:
		base = s.preset.Short
	case strings.Contains(address, "@"):
		_, domain, _ := strings.Cut(address, "@")
		base, _, _ = strings.Cut(domain, ".")
	default:
		base, _, _ = strings.Cut(s.host, ".")
		if base == "imap" || base == "mail" {
			parts := strings.Split(s.host, ".")
			if len(parts) > 1 {
				base = parts[1]
			}
		}
	}
	if base == "" {
		base = "mail"
	}
	for _, a := range r.Accounts {
		if a.Backend == "imap" && strings.EqualFold(a.Settings.Host, s.host) && a.Settings.Username == s.username {
			return a.Name
		}
	}
	name := base
	for i := 2; ; i++ {
		if _, err := r.Find(name); err != nil {
			return name
		}
		name = base + "-" + strconv.Itoa(i)
	}
}

// readPassword gets the password from stdin, the environment or a prompt.
// problem says why the saved password can't be used, when connect tried it.
func (m module) readPassword(f *rg.Factory, c *cobra.Command, s imapSettings, problem string) (string, error) {
	if flagBool(c, "password-stdin") {
		password, err := readPasswordLine(f.Stdin)
		if err != nil {
			return "", newUserError(rg.ExitUsage, "Cannot read the password from stdin: "+err.Error(), "")
		}
		return password, nil
	}
	if password := m.opts.Getenv("MAILMD_IMAP_PASSWORD"); password != "" {
		return password, nil
	}
	canPrompt := isTerminal(f.Stdin)
	if m.opts.Interactive != nil {
		canPrompt = *m.opts.Interactive
	}
	if !canPrompt || f.Flags != nil && f.Flags.NoPrompt {
		if problem != "" {
			return "", newUserError(rg.ExitAuth, strings.TrimSuffix(problem, "."),
				"To save the new settings, mailmd needs the password again. Give it with --password-stdin, or set MAILMD_IMAP_PASSWORD. In a terminal, mailmd asks for it.")
		}
		return "", newUserError(rg.ExitUsage, "No password was given",
			"Pipe it in with --password-stdin, or set MAILMD_IMAP_PASSWORD. In a terminal, mailmd asks for it.")
	}
	if problem != "" {
		fmt.Fprintln(f.Stderr, problem)
	}
	password, err := m.opts.PasswordPrompt(c.Context(), f.Stdin, f.Stderr, "Password for "+s.username)
	if c.Context().Err() != nil {
		return "", c.Context().Err()
	}
	if err != nil {
		return "", newUserError(rg.ExitUsage, "No password was given", "")
	}
	return password, nil
}

// probe opens the new account from its temporary folder and lists the
// mailbox folders. So connect saves only a login that works. It also asks
// for the mailbox's address, where the provider tells it.
func (m module) probe(ctx context.Context, a accounts.AccountConfig, o accounts.OpenOptions) ([]backend.Folder, string, error) {
	o.HTTPClient, o.Clock, o.GraphBaseURL, o.IdentityBaseURL = m.opts.HTTPClient, m.opts.Clock, m.opts.GraphBaseURL, m.opts.IdentityBaseURL
	b, err := m.opts.Open(ctx, a, o)
	if err != nil {
		return nil, "", errors.Join(err, backend.Close(b))
	}
	if b == nil {
		return nil, "", errors.New("the provider returned no connection")
	}
	folders, err := b.Folders(ctx)
	address := ""
	if asker, ok := b.(backend.Addresser); ok && err == nil {
		// The account works without its address, so a failure here only
		// leaves the address empty.
		address, _ = asker.Address(ctx)
		address = cleanAddress(address)
	}
	return folders, address, errors.Join(err, backend.Close(b))
}

// cleanAddress keeps an address from the provider only when it looks like
// one, so a status line never shows something odd.
func cleanAddress(s string) string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "@") || len(s) > 320 || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return ""
	}
	return s
}

// checkFolders makes sure each --folder and --include pattern matches a
// folder, so a typo is not saved.
func checkFolders(opts connectOptions, a accounts.AccountConfig, folders []backend.Folder) error {
	err := opts.filter.CheckMatches(folders)
	if err == nil {
		return nil
	}
	return &userError{code: rg.ExitUsage, info: mdsync.Describe(err, a)}
}

// finishConnect saves the archive folder and filters, then prints the result.
func (m module) finishConnect(f *rg.Factory, c *cobra.Command, a accounts.AccountConfig, opts connectOptions, folders []backend.Folder) error {
	if err := checkFolders(opts, a, folders); err != nil {
		var u *userError
		errors.As(err, &u)
		// The login is saved at this point, but no setting of this command
		// is, so a sync doesn't run with half of them.
		u.info.Message = fmt.Sprintf("Connected %s, but %s mailmd saved only the login and none of the settings of this command, like --folder or --out", a.Name, lowerFirst(u.info.Message))
		u.info.Hint += ". Run the connect command again with the right folder."
		return u
	}
	if changesSettings(c) {
		updated, err := accounts.Update(opts.root, a.Name, func(acc *accounts.AccountConfig) { applySettings(c, opts, acc) })
		if err != nil {
			return newUserError(rg.ExitAPI, "The account was connected, but its settings could not be saved", "Run the same connect command again.")
		}
		a = updated
	}
	return m.writeConnect(f, c, a, folders, connectReport{})
}

// archiveOf returns the archive folder of an account.
func (m module) archiveOf(a accounts.AccountConfig) string {
	root := a.Root
	if root == "" {
		root, _ = m.defaultRoot()
	}
	return filepath.Join(root, a.ArchiveDir)
}

// connectReport says how a connect went, for its output.
type connectReport struct {
	dry bool
	// keptLogin is set when only settings changed, and the saved login
	// stayed.
	keptLogin bool
}

// writeConnect prints the result of a connect. a has the settings that a
// sync uses from now on, or would use after a dry run.
func (m module) writeConnect(f *rg.Factory, c *cobra.Command, a accounts.AccountConfig, folders []backend.Folder, report connectReport) error {
	dry := report.dry
	result := ConnectResult{
		Schema: "mailmd-connect/1", Account: a.Name, Backend: a.Backend, Provider: providerName(a.Backend),
		Address: a.Email, Archive: m.archiveOf(a), DryRun: dry, LeftOut: []string{},
		CAFile: a.Settings.CAFile, Next: "mailmd sync " + a.Name,
	}
	filter := savedFilter(a).ForFolders(folders)
	for _, folder := range folders {
		if !folder.Fetchable {
			continue
		}
		result.Folders++
		if out, _ := filter.Excludes(folder.Path, folder.Role); out {
			result.LeftOut = append(result.LeftOut, folder.Path)
		}
	}
	allMail := filter.GmailAllMail()
	if allMail != "" {
		result.Note = "Gmail has every message once in " + allMail + ", including archived mail, so mailmd syncs only that folder. " +
			"The files won't list Gmail's labels. If you want the labels, use mailmd connect gmail instead."
	}
	home := m.home()
	login := "login"
	if a.Backend == "imap" {
		login = "password"
	}
	render := func(w io.Writer) {
		who := result.Provider
		if result.Address != "" {
			who = result.Address
		}
		switch {
		case dry && report.keptLogin:
			fmt.Fprintf(w, "The saved %s still works. Nothing was saved because of --dry-run.\n", login)
		case dry:
			fmt.Fprintf(w, "The login works. Nothing was saved because of --dry-run.\n")
		case report.keptLogin:
			fmt.Fprintf(w, "Changed the settings of %s (%s) and kept its saved %s.\n", a.Name, who, login)
		default:
			fmt.Fprintf(w, "Connected %s (%s).\n", a.Name, who)
		}
		if result.Folders > 0 {
			line := fmt.Sprintf("The mailbox has %s.", plural(result.Folders, "folder", "folders"))
			switch {
			case allMail != "":
			case len(result.LeftOut) == 0:
				line += " A sync copies all of them."
			default:
				line += fmt.Sprintf(" A sync copies %d of them and leaves out %s.", result.Folders-len(result.LeftOut), joinNames(result.LeftOut))
			}
			fmt.Fprintln(w, line)
		}
		if result.Note != "" {
			fmt.Fprintln(w, result.Note)
		}
		if result.CAFile != "" {
			fmt.Fprintf(w, "mailmd trusts the certificate authority in %s for this account, in addition to the system's certificates.\n", shortPath(result.CAFile, home))
		}
		fmt.Fprintf(w, "Mail goes to %s.\n", shortPath(result.Archive, home))
		if !dry {
			fmt.Fprintf(w, "\nNext: %s\n", result.Next)
		}
	}
	return f.WriteOutput(rg.Output{Model: result, Human: render, Plain: render, Meta: output.Meta{Extra: map[string]any{"command": c.CommandPath()}}})
}

// connectError explains a failed connect.
func connectError(err error, kind string) error {
	var u *userError
	if errors.As(err, &u) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return &userError{code: rg.ExitAPI, info: mdsync.ErrorInfo{Category: mdsync.CategoryCanceled, Message: "Canceled. Nothing was saved"}}
	}
	var problem *backend.Problem
	if errors.As(err, &problem) {
		return &userError{code: exitCode(mdsync.Category(err)), info: mdsync.ErrorInfo{Category: mdsync.Category(err), Message: problem.Text, Hint: problem.Hint}}
	}
	text := err.Error()
	for _, prefix := range []string{"imap: invalid config: ", "auth: invalid options: ", "auth: invalid oauth client: ", "auth: oauth setup failed: "} {
		text = strings.TrimPrefix(text, prefix)
	}
	switch {
	case errors.Is(err, accounts.ErrKindMismatch):
		return newUserError(rg.ExitUsage, "An account with this name already exists and uses another provider", "Choose another name with --account.")
	case errors.Is(err, imap.ErrInvalidConfig), errors.Is(err, auth.ErrInvalidOptions):
		return newUserError(rg.ExitUsage, upperFirst(text), "")
	case errors.Is(err, auth.ErrNoClientSecret):
		return newUserError(rg.ExitUsage, upperFirst(text),
			"Google shows the client secret only once, right after you create the client. Create a new client of the type \"Desktop app\" and click \"Download JSON\" in the dialog that opens. The Gmail guide shows each step: https://github.com/vincentsch/mailmd/blob/main/docs/gmail.md")
	case errors.Is(err, auth.ErrInvalidOAuthClient):
		return newUserError(rg.ExitUsage, upperFirst(text),
			"In Google Cloud, create an OAuth client of the type \"Desktop app\" and download its JSON file. The Gmail guide shows each step: https://github.com/vincentsch/mailmd/blob/main/docs/gmail.md")
	case errors.Is(err, auth.ErrOAuthSetup):
		return newUserError(rg.ExitAuth, "The sign-in did not finish: "+text, "Run the connect command again.")
	}
	info := mdsync.Describe(err, accounts.AccountConfig{Backend: kind})
	if info.Category == mdsync.CategoryAuth {
		// Describe words this for a saved login that stopped working. Here
		// the first sign-in failed, and the account has no name yet.
		info.Message = map[string]string{"gmail": "Google", "graph": "Microsoft"}[kind] + " did not accept the sign-in."
		info.Hint = "Check that you signed in with the right account, then run the connect command again."
	}
	return &userError{code: exitCode(info.Category), info: info}
}

func upperFirst(s string) string {
	if s == "" || strings.HasPrefix(s, "--") {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func readPasswordLine(r io.Reader) (string, error) {
	var b []byte
	one := make([]byte, 1)
	for len(b) <= 65536 {
		n, err := r.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				break
			}
			b = append(b, one[0])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if len(b) > 65536 {
		return "", errors.New("the password is too long")
	}
	s := strings.TrimSuffix(string(b), "\r")
	if s == "" {
		return "", errors.New("the first line is empty")
	}
	return s, nil
}

func hiddenPassword(ctx context.Context, r io.Reader, w io.Writer, label string) (string, error) {
	file, ok := r.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", errors.New("stdin is not a terminal")
	}
	fd := int(file.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(w, "%s (hidden): ", label)
	type answer struct {
		password []byte
		err      error
	}
	done := make(chan answer, 1)
	go func() {
		b, err := term.ReadPassword(fd)
		done <- answer{b, err}
	}()
	var a answer
	select {
	case <-ctx.Done():
		// On Ctrl-C, the terminal shows typed text again before mailmd
		// stops.
		_ = term.Restore(fd, state)
		fmt.Fprintln(w)
		return "", ctx.Err()
	case a = <-done:
	}
	fmt.Fprintln(w)
	if a.err != nil {
		return "", a.err
	}
	if len(a.password) == 0 {
		return "", errors.New("empty password")
	}
	return string(a.password), nil
}
