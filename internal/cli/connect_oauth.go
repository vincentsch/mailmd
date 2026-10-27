package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/auth"
	"github.com/vincentsch/mailmd/internal/backend"
	rg "github.com/vincentsch/rungrad"
	"github.com/vincentsch/rungrad/output"
)

func (m module) connectGmail(f *rg.Factory, c *cobra.Command, args []string) error {
	if err := checkGlobals(f); err != nil {
		return err
	}
	opts, err := m.readConnectOptions(c)
	if err != nil {
		return err
	}
	name := oauthAccountName(c, opts.root, "gmail")
	// A name that another provider uses is the first thing to fix.
	planned, err := plannedAccount(opts.root, name, "gmail")
	if err != nil {
		return err
	}
	secret := strings.TrimSpace(flagString(c, "client-secret"))
	// Connecting an account again uses the client saved with its login, so
	// the downloaded file is needed only the first time.
	savedClientOf := ""
	if secret == "" {
		old, ok := savedAccount(opts.root, name, "gmail")
		if !ok {
			return newUserError(rg.ExitUsage, "Missing --client-secret",
				"Give the OAuth client JSON file that you downloaded from Google Cloud. The Gmail guide shows how to create it: https://github.com/vincentsch/mailmd/blob/main/docs/gmail.md")
		}
		if err := auth.CheckSavedGmailClient(opts.root, old.ArchiveDir); err != nil {
			return newUserError(rg.ExitUsage, fmt.Sprintf("Cannot use the OAuth client saved for %s, because %s", name, savedLoginProblem(err)),
				"Give the client JSON file again with --client-secret. The Gmail guide shows how to create a new client: https://github.com/vincentsch/mailmd/blob/main/docs/gmail.md")
		}
		savedClientOf = old.ArchiveDir
	} else {
		if secret, err = absPath(secret, m.home()); err != nil {
			return err
		}
		if _, err := auth.CheckGmailClient(secret); err != nil {
			return connectError(err, "gmail")
		}
	}
	if f.DryRun() {
		text := "The client file is a valid Desktop app client."
		if savedClientOf != "" {
			text = "mailmd can use the OAuth client saved for " + name + "."
		}
		return m.writeOAuthDryRun(f, c, planned, opts, text+" Nothing was saved because of --dry-run. Without --dry-run, mailmd shows the Google sign-in address.")
	}
	interactive := m.interactive(f)
	// A person at a terminal can paste the address or press Ctrl-C. A
	// script or an agent that runs connect would otherwise wait forever
	// when nobody opens the address.
	var waitLimit time.Duration
	if !interactive {
		waitLimit = m.opts.GmailWaitLimit
		if waitLimit == 0 {
			waitLimit = 10 * time.Minute
		}
	}
	var folders []backend.Folder
	setup := func(ctx context.Context, stage string) (accounts.Settings, string, error) {
		handler := m.opts.HandleAuthURL
		if handler == nil {
			handler = func(ctx context.Context, url string) error {
				fmt.Fprintf(f.Stderr, "Sign in to Google and allow mailmd to read your Gmail:\n\n  %s\n\n", url)
				return nil
			}
		}
		// A browser opens only for a person at a terminal. When a script
		// or an agent runs connect, mailmd only prints the address.
		var opener auth.BrowserOpener
		if interactive && !flagBool(c, "no-browser") {
			opener = m.opts.OpenBrowser
			if opener == nil {
				opener = f.OpenBrowser
			}
		}
		token, err := auth.ConnectGmail(ctx, auth.GmailOptions{
			ConfigRoot:      opts.root,
			ArchiveDir:      stage,
			OAuthClientPath: secret,
			SavedClientOf:   savedClientOf,
			HTTPClient:      m.opts.HTTPClient,
			HandleAuthURL: func(ctx context.Context, url string) error {
				if err := handler(ctx, url); err != nil {
					return err
				}
				if opener != nil {
					fmt.Fprintln(f.Stderr, "mailmd opens this address in your browser now.")
				}
				if !interactive {
					limit := waitLimit.String()
					if waitLimit >= time.Minute {
						limit = plural(int(waitLimit.Minutes()), "minute", "minutes")
					}
					fmt.Fprintf(f.Stderr, "Waiting up to %s for you to allow access in the browser.\n", limit)
					return nil
				}
				fmt.Fprintln(f.Stderr, "Waiting for you to allow access in the browser. Press Ctrl-C to cancel.")
				fmt.Fprintln(f.Stderr)
				fmt.Fprintln(f.Stderr, "On a computer without a browser, open the address on another device. After you allow access there,")
				fmt.Fprintln(f.Stderr, "the browser tries to load a page on 127.0.0.1 and fails. Copy that page's address and paste it here:")
				return nil
			},
			OpenBrowser: opener,
			WaitLimit:   waitLimit,
			ReadRedirect: func(ctx context.Context) (string, error) {
				if !interactive {
					return "", errNoAccounts
				}
				return readLine(ctx, bufio.NewReader(f.Stdin))
			},
		})
		if err != nil {
			return accounts.Settings{}, "", err
		}
		for _, value := range token.SecretValues {
			f.RegisterSecret(value)
		}
		account := accounts.AccountConfig{Name: name, Backend: "gmail", ArchiveDir: stage, CredentialRef: stage + "/gmail-token.json"}
		var address string
		folders, address, err = m.probe(ctx, account, accounts.OpenOptions{ConfigRoot: opts.root, TokenSource: token.TokenSource})
		return accounts.Settings{}, address, err
	}
	account, err := accounts.Connect(c.Context(), opts.root, name, "gmail", setup, m.opts.ConnectHooks)
	if err != nil {
		return connectError(err, "gmail")
	}
	f.Infof("If your Google Cloud app is in testing mode, Google ends this login after 7 days. The Gmail guide shows how to avoid that: https://github.com/vincentsch/mailmd/blob/main/docs/gmail.md#the-7-day-limit-in-testing-mode")
	return m.finishConnect(f, c, account, opts, folders)
}

// writeOAuthDryRun prints the result of an OAuth connect with --dry-run.
// The settings were checked, and nothing else happened.
func (m module) writeOAuthDryRun(f *rg.Factory, c *cobra.Command, a accounts.AccountConfig, opts connectOptions, text string) error {
	root := opts.archive
	if root == "" {
		root, _ = m.defaultRoot()
	}
	result := ConnectResult{Schema: "mailmd-connect/1", Account: a.Name, Backend: a.Backend, Provider: providerName(a.Backend),
		Archive: filepath.Join(root, a.ArchiveDir), DryRun: true, LeftOut: []string{}, Next: "mailmd sync " + a.Name}
	render := func(w io.Writer) { fmt.Fprintln(w, text) }
	return f.WriteOutput(rg.Output{Model: result, Human: render, Plain: render, Meta: output.Meta{Extra: map[string]any{"command": c.CommandPath()}}})
}

func (m module) connectOutlook(f *rg.Factory, c *cobra.Command, args []string) error {
	if err := checkGlobals(f); err != nil {
		return err
	}
	opts, err := m.readConnectOptions(c)
	if err != nil {
		return err
	}
	name := oauthAccountName(c, opts.root, "outlook")
	planned, err := plannedAccount(opts.root, name, "graph")
	if err != nil {
		return err
	}
	clientID := strings.TrimSpace(flagString(c, "client-id"))
	tenant := flagString(c, "tenant")
	// Connecting an account again uses its saved client ID, and its saved
	// tenant unless --tenant is given. A new client ID starts from --tenant.
	if old, ok := savedAccount(opts.root, name, "graph"); ok && clientID == "" {
		clientID = old.Settings.ClientID
		if !c.Flags().Changed("tenant") && old.Settings.Tenant != "" {
			tenant = old.Settings.Tenant
		}
	}
	if clientID == "" {
		return newUserError(rg.ExitUsage, "Missing --client-id",
			"Give the application (client) ID of your app registration in Microsoft Entra. The Outlook guide shows how to create one: https://github.com/vincentsch/mailmd/blob/main/docs/outlook.md")
	}
	if err := auth.CheckGraphSettings(clientID, tenant); err != nil {
		return connectError(err, "graph")
	}
	if f.DryRun() {
		return m.writeOAuthDryRun(f, c, planned, opts,
			"The client ID and tenant look right. Nothing was saved because of --dry-run. Without --dry-run, mailmd shows a sign-in code.")
	}
	var folders []backend.Folder
	setup := func(ctx context.Context, stage string) (accounts.Settings, string, error) {
		handler := m.opts.HandleDeviceCode
		if handler == nil {
			handler = func(ctx context.Context, p auth.DeviceCodePrompt) error {
				fmt.Fprintf(f.Stderr, "Open %s and enter the code %s to allow read-only access to your mail.\n", p.VerificationURI, p.UserCode)
				fmt.Fprintln(f.Stderr, "Waiting for you to sign in. Press Ctrl-C to cancel.")
				return nil
			}
		}
		token, err := auth.ConnectGraph(ctx, auth.GraphConnectOptions{
			ConfigRoot: opts.root, ArchiveDir: stage, ClientID: clientID, Tenant: tenant,
			HTTPClient: m.opts.HTTPClient, Clock: m.opts.Clock, IdentityBaseURL: m.opts.IdentityBaseURL, GraphBaseURL: m.opts.GraphBaseURL,
			HandleDeviceCode: handler,
		})
		if err != nil {
			return accounts.Settings{}, "", err
		}
		for _, value := range token.SecretValues {
			f.RegisterSecret(value)
		}
		settings := accounts.Settings{ClientID: token.ClientID, Tenant: token.Tenant}
		account := accounts.AccountConfig{Name: name, Backend: "graph", ArchiveDir: stage, CredentialRef: stage + "/graph-token.json", Settings: settings}
		var address string
		folders, address, err = m.probe(ctx, account, accounts.OpenOptions{ConfigRoot: opts.root, TokenSource: token.TokenSource})
		if address == "" {
			// Without the User.Read permission, Microsoft doesn't tell the
			// address. The name from the sign-in is then the best guess.
			address = cleanAddress(token.SignInName)
		}
		return settings, address, err
	}
	account, err := accounts.Connect(c.Context(), opts.root, name, "graph", setup, m.opts.ConnectHooks)
	if err != nil {
		return connectError(err, "graph")
	}
	return m.finishConnect(f, c, account, opts, folders)
}

// savedAccount returns the connected account of that name, if it uses the
// given provider.
func savedAccount(root, name, kind string) (accounts.AccountConfig, bool) {
	r, err := accounts.Load(root)
	if err != nil {
		return accounts.AccountConfig{}, false
	}
	a, err := r.Find(name)
	return a, err == nil && a.Backend == kind
}

// savedLoginProblem says in a few words why a saved login can't be used.
func savedLoginProblem(err error) string {
	switch {
	case errors.Is(err, auth.ErrCredentialNotFound):
		return "its login file is missing"
	case errors.Is(err, auth.ErrInsecurePermission):
		return "other users on this computer can read its login file"
	}
	return "its login file is damaged"
}

// plannedAccount is the account that a connect would save. That is the
// existing one of that name, or a new one. A name that another provider uses
// is an error, also in a dry run.
func plannedAccount(root, name, kind string) (accounts.AccountConfig, error) {
	registry, err := accounts.Load(root)
	if err != nil {
		return accounts.AccountConfig{}, registryError(err)
	}
	if old, err := registry.Find(name); err == nil {
		if old.Backend != kind {
			return accounts.AccountConfig{}, newUserError(rg.ExitUsage, fmt.Sprintf("The account %s already exists and uses %s", old.Name, providerName(old.Backend)), "Choose another name with --account.")
		}
		return old, nil
	}
	dir, err := accounts.ArchiveDir(name, registry)
	if err != nil {
		return accounts.AccountConfig{}, newUserError(rg.ExitUsage, fmt.Sprintf("%q can't be used as an account name", name), "Use letters and digits. Dots, hyphens and underscores also work.")
	}
	return accounts.AccountConfig{Name: name, Backend: kind, ArchiveDir: dir}, nil
}

// oauthAccountName returns --account, or the provider name, numbered when
// that name is taken by another provider.
func oauthAccountName(c *cobra.Command, root, base string) string {
	if name := strings.TrimSpace(flagString(c, "account")); name != "" {
		return name
	}
	r, err := accounts.Load(root)
	if err != nil {
		return base
	}
	kind := map[string]string{"gmail": "gmail", "outlook": "graph"}[base]
	name := base
	for i := 2; ; i++ {
		a, err := r.Find(name)
		if err != nil || a.Backend == kind {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}
