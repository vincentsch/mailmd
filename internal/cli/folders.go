package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/auth"
	"github.com/vincentsch/mailmd/internal/backend"
	mdsync "github.com/vincentsch/mailmd/internal/sync"
	rg "github.com/vincentsch/rungrad"
	"github.com/vincentsch/rungrad/output"
)

// FolderList is the JSON result of mailmd list folders.
type FolderList struct {
	Schema   string `json:"schema"`
	Account  string `json:"account"`
	Provider string `json:"provider"`
	Address  string `json:"address"`
	// Included counts the folders that a sync copies.
	Included int          `json:"included"`
	Folders  []FolderItem `json:"folders"`
	// connect is the connect command of the account, like imap.
	connect string
}

// FolderItem is one folder on the server.
type FolderItem struct {
	Path string `json:"path"`
	// Role is the folder's special use when the provider names one, like
	// inbox or junk.
	Role string `json:"role,omitempty"`
	// Messages is null when the provider can't count the folder's messages.
	Messages *int `json:"messages"`
	// Included says whether a sync copies the folder. Reason says why not,
	// in the same words as mailmd sync --json.
	Included bool   `json:"included"`
	Reason   string `json:"reason,omitempty"`
}

func (m module) listFoldersCommand(cmd *rg.Command) {
	cmd.Use = "folders [account]"
	cmd.Args = cobra.MaximumNArgs(1)
	cmd.Run = m.listFolders
}

func (m module) listFolders(f *rg.Factory, c *cobra.Command, args []string) error {
	if err := checkGlobals(f); err != nil {
		return err
	}
	root, err := m.configRoot(c)
	if err != nil {
		return err
	}
	a, err := oneAccount(root, args)
	if err != nil {
		return err
	}
	var log io.Writer
	if flagBool(c, "verbose") {
		log = f.Stderr
	}
	ctx := c.Context()
	b, err := m.open(ctx, root, a, auth.PersistTokens, log)
	if err != nil {
		_ = backend.Close(b)
		return accountProblem(err, a)
	}
	defer backend.Close(b)
	folders, err := b.Folders(ctx)
	if err != nil {
		return accountProblem(err, a)
	}
	counts := map[string]int{}
	if counter, ok := b.(backend.Counter); ok {
		if counts, err = counter.Count(ctx, folders); err != nil {
			return accountProblem(err, a)
		}
	}
	list := FolderList{Schema: "mailmd-folder-list/1", Account: a.Name, Provider: providerName(a.Backend), Address: a.Email, Folders: []FolderItem{}, connect: connectCommand(a.Backend)}
	filter := savedFilter(a).ForFolders(folders)
	for _, folder := range folders {
		if !folder.Fetchable {
			continue
		}
		item := FolderItem{Path: folder.Path, Role: string(folder.Role)}
		if n, ok := counts[folder.Path]; ok {
			item.Messages = &n
		}
		out, reason := filter.Excludes(folder.Path, folder.Role)
		item.Included, item.Reason = !out, reason
		if item.Included {
			list.Included++
		}
		list.Folders = append(list.Folders, item)
	}
	render := func(w io.Writer) { renderFolders(w, list) }
	return f.WriteOutput(rg.Output{Model: list, Human: render, Plain: render, Meta: output.Meta{Extra: map[string]any{"command": c.CommandPath()}}})
}

func renderFolders(w io.Writer, list FolderList) {
	title := list.Account + " (" + list.Provider
	if list.Address != "" {
		title += ", " + list.Address
	}
	fmt.Fprintln(w, title+")")
	if len(list.Folders) == 0 {
		fmt.Fprintln(w, "The server lists no folders.")
		return
	}
	fmt.Fprintln(w)
	t := newTable(w)
	fmt.Fprintln(t, "  FOLDER\tMESSAGES\tSYNC")
	byDefault := ""
	for _, folder := range list.Folders {
		messages := "-"
		if folder.Messages != nil {
			messages = count(*folder.Messages)
		}
		sync := "yes"
		if !folder.Included {
			sync = "no, " + folder.Reason
		}
		if folder.Reason == mdsync.ReasonLeftOutByDefault && byDefault == "" {
			byDefault = folder.Path
		}
		fmt.Fprintf(t, "  %s\t%s\t%s\n", folder.Path, messages, sync)
	}
	_ = t.Flush()
	fmt.Fprintln(w)
	fmt.Fprintf(w, "A sync copies %d of these %s.\n", list.Included, plural(len(list.Folders), "folder", "folders"))
	if byDefault != "" {
		fmt.Fprintf(w, "To copy %s on every sync, run: mailmd connect %s --account %s --include %s\n", byDefault, list.connect, shellWord(list.Account), shellWord(byDefault))
		fmt.Fprintf(w, "To copy it once, run: mailmd sync %s --folder %s\n", shellWord(list.Account), shellWord(byDefault))
	}
}

// connectCommand returns the name of the connect command for a provider.
func connectCommand(kind string) string {
	if kind == "graph" {
		return "outlook"
	}
	return kind
}

// oneAccount returns the named account. Without a name, it returns the only
// connected account.
func oneAccount(root string, args []string) (accounts.AccountConfig, error) {
	selected, err := selectAccounts(root, args)
	if err != nil {
		return accounts.AccountConfig{}, err
	}
	if len(selected) > 1 {
		var names []string
		for _, a := range selected {
			names = append(names, a.Name)
		}
		return accounts.AccountConfig{}, newUserError(rg.ExitUsage, "Name the account whose folders you want to see",
			"Connected accounts: "+strings.Join(names, ", ")+". For example: mailmd list folders "+shellWord(names[0]))
	}
	return selected[0], nil
}

// open connects to the provider of an account.
func (m module) open(ctx context.Context, root string, a accounts.AccountConfig, policy auth.TokenPersistence, log io.Writer) (backend.Backend, error) {
	return m.opts.Open(ctx, a, accounts.OpenOptions{
		ConfigRoot:      root,
		Persistence:     policy,
		HTTPClient:      m.opts.HTTPClient,
		RootCAs:         m.opts.RootCAs,
		Clock:           m.opts.Clock,
		GraphBaseURL:    m.opts.GraphBaseURL,
		IdentityBaseURL: m.opts.IdentityBaseURL,
		Log:             log,
	})
}

// accountProblem explains a failed provider call for an account.
func accountProblem(err error, a accounts.AccountConfig) error {
	info := mdsync.Describe(err, a)
	return &userError{code: exitCode(info.Category), info: info}
}

// shellWord quotes a word for a command line when it needs it, like a
// folder name with a space.
func shellWord(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._/-@", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
