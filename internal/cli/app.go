// Package cli defines mailmd's commands.
package cli

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/auth"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/privatefs"
	rg "github.com/vincentsch/rungrad"
	"github.com/vincentsch/rungrad/manifest"
)

// Options lets tests replace the parts of mailmd that touch the outside
// world. The zero value is the real program.
type Options struct {
	Version string
	// ConfigRoot replaces the user config folder of the operating system.
	ConfigRoot string
	// HomeDir replaces the home folder, which holds the default archive.
	HomeDir string
	Getenv  func(string) string
	// PasswordPrompt asks for a password without showing it. It gives up
	// when the context ends, as on Ctrl-C.
	PasswordPrompt func(context.Context, io.Reader, io.Writer, string) (string, error)
	// Interactive turns questions on or off. Nil means mailmd asks only when
	// stdin and stderr are terminals.
	Interactive                   *bool
	HTTPClient                    *http.Client
	RootCAs                       *x509.CertPool
	GraphBaseURL, IdentityBaseURL string
	HandleAuthURL                 auth.AuthURLHandler
	// GmailWaitLimit is how long connect gmail waits for the browser when
	// it doesn't run in a terminal. Zero means 10 minutes.
	GmailWaitLimit   time.Duration
	OpenBrowser      auth.BrowserOpener
	HandleDeviceCode func(context.Context, auth.DeviceCodePrompt) error
	Open             func(context.Context, accounts.AccountConfig, accounts.OpenOptions) (backend.Backend, error)
	ConnectHooks     privatefs.Hooks
	Clock            func() time.Time
	Sleep            func(context.Context, time.Duration) error
	Random           func() float64
	UpdateFetcher    updateFetcher
}

type module struct{ opts Options }

// New builds the mailmd command tree.
func New(opts Options) *rg.App {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Open == nil {
		opts.Open = accounts.OpenBackend
	}
	if opts.PasswordPrompt == nil {
		opts.PasswordPrompt = hiddenPassword
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	app := rg.New(rg.AppConfig{
		Name:           "mailmd",
		Short:          "Copy your mailbox into Markdown files",
		Long:           "mailmd copies your mailbox into a folder of Markdown files on your computer, and each sync adds the new mail. It works with any IMAP mailbox, and with Gmail and Outlook through their own APIs. It only reads from your mail provider.",
		Version:        opts.Version,
		AdvancedOutput: true,
		ErrorPolicy:    &rg.ErrorPolicy{Classify: classifyError, Render: renderError},
	})
	flags := app.Root().PersistentFlags()
	flags.String("config-root", opts.ConfigRoot, "Folder for mailmd's settings and logins (default: your user config folder)")
	flags.Bool("verbose", false, "Show the details when mailmd can't read an IMAP server's answer")
	// rungrad adds a --config flag, but mailmd has no config file.
	_ = flags.MarkHidden("config")
	app.AddModule(module{opts: opts})
	return app
}

func (module) Groups() []rg.Group { return nil }

// spec describes one command. The command tree and the generated docs both
// come from these values, and so does the catalog that rungrad checks the
// tree against.
type spec struct {
	path     string
	short    string
	long     string
	examples []string
	related  []string
	schema   string
	modes    []string
	mutates  bool
}

var (
	allModes    = []string{rg.OutputModeHuman, rg.OutputModeJSON, rg.OutputModePlain, rg.OutputModeJQ, rg.OutputModeTemplate}
	simpleModes = []string{rg.OutputModeHuman, rg.OutputModeJSON}
)

var specs = []spec{
	{
		path:    "connect",
		short:   "Connect a mailbox",
		long:    "Connect a mailbox once, so that later syncs can use it. mailmd checks the login and saves it in your user config folder, together with the folder for the archive.",
		related: []string{"sync", "list accounts"},
		examples: []string{
			"mailmd connect imap --email you@fastmail.com",
			"mailmd connect gmail --client-secret client_secret.json",
			"mailmd connect outlook --client-id 12345678-1234-1234-1234-123456789abc",
		},
	},
	{
		path:  "connect imap",
		short: "Connect an IMAP mailbox",
		long: "Connect any mailbox that offers IMAP, like Gmail or your own mail server. " +
			"Give your email address, and mailmd fills in the server settings of known providers. " +
			"In a terminal, it asks for anything that is missing, and then for the password in a hidden prompt.\n\n" +
			"Scripts can pipe the password in with --password-stdin, or set MAILMD_IMAP_PASSWORD.\n\n" +
			"To enter a new password for a connected account, the account name is enough, like mailmd connect imap --account work.\n\n" +
			"Use --dry-run to test the login without saving anything.",
		examples: []string{
			"mailmd connect imap --email you@fastmail.com",
			"mailmd connect imap --account work --host imap.example.com --username you@example.com",
			"printf '%s\\n' \"$PASSWORD\" | mailmd connect imap --email you@example.com --host imap.example.com --password-stdin",
			"mailmd connect imap --account work",
		},
		related: []string{"sync", "list accounts"},
		schema:  "mailmd-connect/1",
		modes:   simpleModes,
		mutates: true,
	},
	{
		path:  "connect gmail",
		short: "Connect Gmail through the Gmail API",
		long: "Connect Gmail through the Gmail API, with read-only access. You need your own OAuth client from Google Cloud, of the type \"Desktop app\". " +
			"The Gmail guide shows how to create one: https://github.com/vincentsch/mailmd/blob/main/docs/gmail.md\n\n" +
			"mailmd prints the Google sign-in address, and in a terminal it also opens the address in your browser. " +
			"On a computer without a browser, use --no-browser. After you sign in on another device, paste the address that the browser ends up on.\n\n" +
			"To connect an account again, for example after its login ended, the account name is enough. mailmd then uses the client it saved, and a new --client-secret replaces it.",
		examples: []string{
			"mailmd connect gmail --client-secret ~/Downloads/client_secret.json",
			"mailmd connect gmail --account personal --client-secret client_secret.json --no-browser",
			"mailmd connect gmail --account personal",
		},
		related: []string{"sync", "connect imap"},
		schema:  "mailmd-connect/1",
		modes:   simpleModes,
		mutates: true,
	},
	{
		path:  "connect outlook",
		short: "Connect Outlook or Microsoft 365",
		long: "Connect an Outlook.com or Microsoft 365 mailbox through Microsoft Graph, with read-only access. You need the client ID of your own app registration in Microsoft Entra. " +
			"The Outlook guide shows how to create one: https://github.com/vincentsch/mailmd/blob/main/docs/outlook.md\n\n" +
			"mailmd shows a code, and you enter it on Microsoft's sign-in page in any browser.\n\n" +
			"To connect an account again, the account name is enough. mailmd then uses the saved client ID and tenant.",
		examples: []string{
			"mailmd connect outlook --client-id 12345678-1234-1234-1234-123456789abc",
			"mailmd connect outlook --account work --client-id 12345678-1234-1234-1234-123456789abc --tenant organizations",
			"mailmd connect outlook --account work",
		},
		related: []string{"sync", "connect imap"},
		schema:  "mailmd-connect/1",
		modes:   simpleModes,
		mutates: true,
	},
	{
		path:  "sync",
		short: "Copy new mail into the archive",
		long: "Copy the new mail of your connected accounts into Markdown files. Without account names, mailmd syncs all accounts. " +
			"Each run fetches only what is new since the last one. If you stop a sync with Ctrl-C, the files saved so far stay.\n\n" +
			"Spam and trash are left out unless you name them with --folder. For Gmail over IMAP, mailmd syncs only [Gmail]/All Mail, which has every message once.\n\n" +
			"A message that was too large is saved with its headers only. When you raise --max-message-bytes above its size, the next sync fetches the full message. " +
			"With --quiet, it prints only warnings and errors.",
		examples: []string{
			"mailmd sync",
			"mailmd sync work",
			"mailmd sync work --since 12m --exclude Newsletters",
			"mailmd sync --dry-run",
			"mailmd sync --quiet",
		},
		related: []string{"status", "connect"},
		schema:  "mailmd-sync/1",
		modes:   allModes,
		mutates: true,
	},
	{
		path:     "status",
		short:    "Show what is in the archive",
		long:     "Show where each account's archive is and how many messages it has, and when the last sync ran. With one account name, it also lists the folders and the messages that mailmd couldn't save in full. It reads only local files.",
		examples: []string{"mailmd status", "mailmd status work --json"},
		related:  []string{"sync", "list accounts"},
		schema:   "mailmd-status/1",
		modes:    allModes,
	},
	{
		path:     "list",
		short:    "List parts of your mailmd setup",
		long:     "Show parts of your mailmd setup. Run mailmd list accounts to see your connected accounts with their provider and archive folder.",
		examples: []string{"mailmd list accounts"},
		related:  []string{"status"},
	},
	{
		path:     "list accounts",
		short:    "List connected accounts",
		long:     "List the connected accounts with their provider and archive folder. It reads only local files.",
		examples: []string{"mailmd list accounts", "mailmd list accounts --json"},
		related:  []string{"connect", "status"},
		schema:   "mailmd-account-list/1",
		modes:    allModes,
	},
	{
		path:  "demo",
		short: "Write a sample archive without any account",
		long: "Write a small sample archive from example mail that comes with mailmd. You see the files mailmd makes before you connect a mailbox. " +
			"It needs no account and makes no network connections.",
		examples: []string{"mailmd demo", "mailmd demo --out ./demo-mail"},
		related:  []string{"connect imap"},
		schema:   "mailmd-demo/1",
		modes:    simpleModes,
		mutates:  true,
	},
	{
		path:     "update",
		short:    "Check for a newer version",
		long:     "Check GitHub for a newer release of mailmd. It doesn't install anything and doesn't read your accounts.",
		examples: []string{"mailmd update --check"},
		related:  []string{"status"},
		schema:   "rungrad-update-result/1",
		modes:    simpleModes,
	},
}

func extensions(s spec) manifest.ExtensionSet {
	return manifest.ExtensionSet{"vincentsch.github.io/mailmd": manifest.ExtensionObject{"result_schema": s.schema}}
}

func (m module) Catalog() []rg.CommandSpec {
	var rows []rg.CommandSpec
	for _, s := range specs {
		rows = append(rows, rg.CommandSpec{
			Path:        s.path,
			Summary:     s.short,
			OutputModes: s.modes,
			Examples:    s.examples,
			Related:     s.related,
			Mutates:     s.mutates,
			Extensions:  extensions(s),
		})
	}
	return rows
}

func (m module) Commands() []*rg.Command {
	commands := map[string]*rg.Command{}
	for _, s := range specs {
		commands[s.path] = &rg.Command{
			Use:         lastWord(s.path),
			Short:       s.short,
			Long:        s.long,
			Examples:    s.examples,
			Related:     s.related,
			Mutates:     s.mutates,
			OutputModes: s.modes,
			Extensions:  extensions(s),
			Args:        cobra.NoArgs,
		}
	}
	m.connectCommands(commands)
	m.syncCommand(commands["sync"])
	m.statusCommand(commands["status"])
	m.listAccountsCommand(commands["list accounts"])
	m.demoCommand(commands["demo"])
	m.updateCommand(commands["update"])
	commands["connect"].AddCommand(commands["connect imap"], commands["connect gmail"], commands["connect outlook"])
	commands["list"].AddCommand(commands["list accounts"])
	return []*rg.Command{commands["connect"], commands["sync"], commands["status"], commands["list"], commands["demo"], commands["update"]}
}

func lastWord(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == ' ' {
			return path[i+1:]
		}
	}
	return path
}

// configRoot returns the folder that holds mailmd's settings.
func configRoot(c *cobra.Command) (string, error) {
	root, _ := c.Flags().GetString("config-root")
	if root == "" {
		var err error
		if root, err = os.UserConfigDir(); err != nil || root == "" {
			return "", rg.NewError(rg.ExitUsage, "Cannot find your user config folder. Set one with --config-root.")
		}
	}
	// The config folder can be a symbolic link, as dotfile managers often
	// set up.
	resolved, err := privatefs.ResolveRoot(root)
	if err != nil {
		return "", newUserError(rg.ExitUsage, fmt.Sprintf("Cannot open the config folder %s", root), "Check that the folder exists and that you can read it.")
	}
	// mailmd puts its files in a "mailmd" folder inside the root. When you
	// name that folder itself, its parent is meant.
	if filepath.Base(resolved) == "mailmd" {
		if _, err := os.Lstat(filepath.Join(resolved, "accounts.json")); err == nil {
			return filepath.Dir(resolved), nil
		}
	}
	return resolved, nil
}

// checkGlobals rejects global flags mailmd does not use.
func checkGlobals(f *rg.Factory) error {
	if f.Flags != nil && f.Flags.Config != "" {
		return rg.NewError(rg.ExitUsage, "mailmd has no config file, so --config is not supported.")
	}
	return nil
}

func flagString(c *cobra.Command, name string) string {
	value, _ := c.Flags().GetString(name)
	return value
}

func flagStrings(c *cobra.Command, name string) []string {
	value, _ := c.Flags().GetStringArray(name)
	return value
}

func flagBool(c *cobra.Command, name string) bool {
	value, _ := c.Flags().GetBool(name)
	return value
}

var errNoAccounts = errors.New("no accounts")
