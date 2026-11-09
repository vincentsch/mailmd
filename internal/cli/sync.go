package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/spf13/cobra"
	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/auth"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/privatefs"
	mdsync "github.com/vincentsch/mailmd/internal/sync"
	rg "github.com/vincentsch/rungrad"
	"github.com/vincentsch/rungrad/output"
)

// SyncOutput is the JSON result of mailmd sync.
type SyncOutput struct {
	Schema   string          `json:"schema"`
	Accounts []mdsync.Result `json:"accounts"`
}

func (m module) syncCommand(cmd *rg.Command) {
	cmd.Use = "sync [account...]"
	cmd.Args = cobra.ArbitraryArgs
	cmd.Configure = func(c *cobra.Command) {
		c.Flags().String("out", "", "Write to this folder for this run instead of the account's saved folder")
		addFilterFlags(c, false)
		c.Flags().Int64("max-message-bytes", backend.DefaultMaxMessageBytes, "Save only the headers of a message larger than this many bytes")
	}
	cmd.Run = m.sync
}

// addFilterFlags adds the flags that choose what a sync fetches. connect
// saves them with the account, and sync uses them for one run.
func addFilterFlags(c *cobra.Command, saved bool) {
	if saved {
		c.Flags().StringArray("folder", nil, "Sync only this folder and its subfolders, like INBOX or 'Work/*'. mailmd saves it with the account (repeat for more)")
		c.Flags().StringArray("exclude", nil, "Leave out this folder and its subfolders. mailmd saves it with the account (repeat for more)")
	} else {
		c.Flags().StringArray("folder", nil, "Sync only this folder and its subfolders in this run, like INBOX or Junk. It isn't saved (repeat for more)")
		c.Flags().StringArray("exclude", nil, "Leave out this folder and its subfolders in this run. It isn't saved (repeat for more)")
	}
	c.Flags().String("since", "", "Fetch only mail received on or after this day, like 2025-01-31, or within an age like 30d, 12m or 2y")
	c.Flags().Bool("keep-raw", false, "Also save each original message as an .eml file next to its Markdown file")
	c.Flags().Bool("prefer-text", false, "Use the plain text part of a message instead of converting its HTML part")
}

func (m module) sync(f *rg.Factory, c *cobra.Command, args []string) error {
	if err := checkGlobals(f); err != nil {
		return err
	}
	root, err := m.configRoot(c)
	if err != nil {
		return err
	}
	maxMessage, _ := c.Flags().GetInt64("max-message-bytes")
	if maxMessage <= 0 || maxMessage >= 1<<40 {
		return newUserError(rg.ExitUsage, "--max-message-bytes must be a positive number of bytes", "")
	}
	// A batch holds at least two messages of the largest size.
	maxBatch := max(backend.DefaultMaxBatchBytes, 2*maxMessage)
	selected, err := selectAccounts(root, args)
	if err != nil {
		return err
	}
	type plan struct {
		account accounts.AccountConfig
		opts    mdsync.Options
	}
	var plans []plan
	for _, a := range selected {
		opts, err := m.syncOptions(f, c, root, a)
		if err != nil {
			return err
		}
		opts.MaxMessageBytes, opts.MaxBatchBytes = maxMessage, maxBatch
		plans = append(plans, plan{a, opts})
	}

	home := m.home()
	out := SyncOutput{Schema: "mailmd-sync/1", Accounts: []mdsync.Result{}}
	var failure error
	for _, p := range plans {
		printer := newProgress(f, p.account, home)
		p.opts.Progress = printer.update
		printer.start(p.opts.Root)
		result, err := mdsync.Run(c.Context(), p.opts)
		printer.finish(result)
		if !p.opts.DryRun {
			info := accounts.LastSync{Started: result.Started, Finished: m.opts.Clock(), Outcome: result.Outcome, New: result.New, Checked: checkedFolders(result)}
			if result.Error != nil {
				info.Error = result.Error.Message
			}
			_ = accounts.SaveLastSync(root, p.account, info)
		}
		out.Accounts = append(out.Accounts, result)
		if err != nil && failure == nil {
			failure = accountError(p.account, result, len(plans) > 1)
		}
		if err == nil && result.Outcome == mdsync.OutcomeFolderErrors && failure == nil {
			failure = &userError{code: rg.ExitAPI, info: mdsync.ErrorInfo{
				Category: "folder_errors",
				Message:  fmt.Sprintf("Some folders of %s could not be synced", p.account.Name),
				Hint:     "The other folders were synced. Run the sync again, and it continues where it stopped.",
			}}
		}
		if c.Context().Err() != nil {
			break
		}
	}
	quiet := f.Flags != nil && f.Flags.Quiet
	render := func(w io.Writer) { renderSync(w, out, home, len(plans) > 1, quiet) }
	if err := f.WriteOutput(rg.Output{Model: out, Human: render, Plain: render, Meta: output.Meta{Extra: map[string]any{"command": c.CommandPath()}}}); err != nil {
		return err
	}
	return failure
}

func accountError(a accounts.AccountConfig, result mdsync.Result, several bool) error {
	info := *result.Error
	if several {
		info.Message = a.Name + ": " + lowerFirst(info.Message)
	}
	return &userError{code: exitCode(info.Category), info: info}
}

func lowerFirst(s string) string {
	if s == "" || strings.HasPrefix(s, "IMAP") {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// selectAccounts returns the named accounts, or all of them.
func selectAccounts(root string, names []string) ([]accounts.AccountConfig, error) {
	r, err := accounts.Load(root)
	if err != nil {
		return nil, registryError(err)
	}
	if len(r.Accounts) == 0 {
		return nil, newUserError(rg.ExitNotFound, "No accounts are connected yet", "Connect one first, for example: mailmd connect imap --email you@example.com")
	}
	if len(names) == 0 {
		return r.Accounts, nil
	}
	var out []accounts.AccountConfig
	for _, name := range names {
		a, err := r.Find(name)
		if err != nil {
			var known []string
			for _, a := range r.Accounts {
				known = append(known, a.Name)
			}
			return nil, newUserError(rg.ExitNotFound, fmt.Sprintf("There is no account named %q", name), "Connected accounts: "+strings.Join(known, ", "))
		}
		out = append(out, a)
	}
	return out, nil
}

func registryError(err error) error {
	if errors.Is(err, privatefs.ErrIntegrity) || errors.Is(err, fs.ErrPermission) {
		return newUserError(rg.ExitAPI, "Cannot open the account list accounts.json in mailmd's config folder",
			"mailmd does not follow symbolic links inside its config folder. Check that the folder and the file belong to you.")
	}
	return newUserError(rg.ExitAPI, "The account list accounts.json in mailmd's config folder is damaged", "Fix or remove the file, then connect your accounts again.")
}

// syncOptions combines the account's saved settings with this run's flags.
func (m module) syncOptions(f *rg.Factory, c *cobra.Command, root string, a accounts.AccountConfig) (mdsync.Options, error) {
	archive := flagString(c, "out")
	if archive == "" {
		archive = a.Root
	}
	if archive == "" {
		var err error
		if archive, err = m.defaultRoot(); err != nil {
			return mdsync.Options{}, newUserError(rg.ExitUsage, upperFirst(err.Error()), "")
		}
	}
	archive, err := absPath(archive, m.home())
	if err != nil {
		return mdsync.Options{}, err
	}
	filter := mdsync.Filter{Folders: a.Folders, Exclude: a.Exclude}
	if c.Flags().Changed("folder") || c.Flags().Changed("exclude") {
		filter = mdsync.Filter{Folders: flagStrings(c, "folder"), Exclude: flagStrings(c, "exclude")}
	}
	if err := filter.Validate(); err != nil {
		return mdsync.Options{}, newUserError(rg.ExitUsage, upperFirst(err.Error()), "")
	}
	sinceText := a.Since
	if c.Flags().Changed("since") {
		sinceText = flagString(c, "since")
	}
	since, err := mdsync.ParseSince(sinceText, m.opts.Clock())
	if err != nil {
		return mdsync.Options{}, newUserError(rg.ExitUsage, upperFirst(err.Error()), "")
	}
	dry := f.DryRun()
	var log io.Writer
	if flagBool(c, "verbose") {
		log = f.Stderr
	}
	return mdsync.Options{
		Account:    a,
		Root:       archive,
		DryRun:     dry,
		Filter:     filter,
		Since:      since,
		KeepRaw:    a.KeepRaw || flagBool(c, "keep-raw"),
		PreferText: a.PreferText || flagBool(c, "prefer-text"),
		Clock:      m.opts.Clock,
		Sleep:      m.opts.Sleep,
		Random:     m.opts.Random,
		Open: func(ctx context.Context) (backend.Backend, error) {
			policy := auth.PersistTokens
			if dry {
				policy = auth.MemoryOnlyTokens
			}
			return m.open(ctx, root, a, policy, log)
		},
	}, nil
}

// renderSync prints one summary per account. With --quiet it prints only
// warnings, so a scheduled sync stays silent unless something needs a look.
func renderSync(w io.Writer, out SyncOutput, home string, several, quiet bool) {
	printed := false
	for _, r := range out.Accounts {
		if quiet {
			for _, line := range warnings(r) {
				if several {
					line = r.Account + ": " + line
				}
				fmt.Fprintln(w, line)
			}
			continue
		}
		// An account that failed before it saved anything has no lines
		// here. Its error comes at the end.
		var b strings.Builder
		renderSyncResult(&b, r, home, several)
		if b.Len() == 0 {
			continue
		}
		if printed && several {
			fmt.Fprintln(w)
		}
		fmt.Fprint(w, b.String())
		printed = true
	}
}

// checkedFolders returns the folders that a sync checked for new mail.
func checkedFolders(r mdsync.Result) []string {
	out := []string{}
	for _, f := range r.Folders {
		if f.Status == "synced" {
			out = append(out, f.Path)
		}
	}
	return out
}

// checkedText says which folders a sync checked, like "4 folders". It
// names them when --folder chose them, like "Junk", so a sync of a few
// folders says which ones.
func checkedText(r mdsync.Result) string {
	named := false
	for _, f := range r.Folders {
		named = named || f.Reason == mdsync.ReasonNotNamed
	}
	return folderWords(checkedFolders(r), named)
}

// folderWords names one folder, or several when named is set, and counts
// them otherwise.
func folderWords(paths []string, named bool) string {
	switch {
	// Gmail syncs all its labels as one scope, so a folder count would
	// always say 1 while the files land in Inbox, Sent and the rest.
	case len(paths) == 1 && paths[0] == mdsync.AllMailPath:
		return "all mail"
	case len(paths) == 1 || named && len(paths) > 0:
		return joinNames(paths)
	}
	return plural(len(paths), "folder", "folders")
}

func renderSyncResult(w io.Writer, r mdsync.Result, home string, several bool) {
	where := shortPath(r.Archive, home)
	checked := checkedText(r)
	newMail := plural(r.New, "new message", "new messages")
	prefix := ""
	if several {
		prefix = r.Account + ": "
	}
	switch {
	case r.Outcome == mdsync.OutcomeInterrupted:
		fmt.Fprintf(w, "%sStopped after %s. The next sync continues from there.\n", prefix, newMail)
	case r.Error != nil:
		if r.New > 0 {
			fmt.Fprintf(w, "%sSaved %s to %s before the sync stopped.\n", prefix, newMail, where)
		}
	case r.DryRun && r.New > 0:
		fmt.Fprintf(w, "%smailmd checked %s and would save %s to %s. Nothing was written because of --dry-run.\n", prefix, checked, newMail, where)
	case r.New > 0:
		fmt.Fprintf(w, "%smailmd checked %s and saved %s to %s in %s.\n", prefix, checked, newMail, where, seconds(r.Seconds))
	case r.DryRun:
		fmt.Fprintf(w, "%s has no new mail. mailmd checked %s in %s. Nothing was written because of --dry-run.\n", r.Account, checked, seconds(r.Seconds))
	default:
		fmt.Fprintf(w, "%s is up to date. mailmd checked %s in %s.\n", r.Account, checked, seconds(r.Seconds))
	}
	if r.Updated > 0 {
		files := map[bool]string{true: "its file", false: "their files"}[r.Updated == 1]
		if r.DryRun {
			fmt.Fprintf(w, "%s changed, and mailmd would update %s.\n", plural(r.Updated, "message", "messages"), files)
		} else {
			fmt.Fprintf(w, "%s changed, and mailmd updated %s.\n", plural(r.Updated, "message", "messages"), files)
		}
	}
	for _, line := range warnings(r) {
		fmt.Fprintln(w, line)
	}
}

// warnings lists what a user should know about a finished sync.
func warnings(r mdsync.Result) []string {
	var out []string
	tooLarge := r.Diagnostics[backend.ReasonTooLarge]
	unreadable := r.Diagnostics[backend.ReasonParseFailure] + r.Diagnostics[backend.ReasonIdentityFailure] + r.Diagnostics[backend.ReasonUnsupportedContent]
	if r.DryRun {
		// Nothing was saved, so status can't list these messages yet.
		if tooLarge > 0 {
			out = append(out, fmt.Sprintf("%s larger than the size limit. A sync would save only the headers.",
				map[bool]string{true: "1 message is", false: count(tooLarge) + " messages are"}[tooLarge == 1]))
		}
		if unreadable > 0 {
			out = append(out, fmt.Sprintf("%s be read. A sync would save the headers it can read, and the original as an .eml file.",
				map[bool]string{true: "1 message can't", false: count(unreadable) + " messages can't"}[unreadable == 1]))
		}
		tooLarge, unreadable = 0, 0
	}
	if n := tooLarge; n > 0 {
		out = append(out, fmt.Sprintf("%s larger than the size limit, so mailmd saved only the headers. To see which, run: mailmd status %s",
			map[bool]string{true: "1 message was", false: count(n) + " messages were"}[n == 1], r.Account))
	}
	if n := unreadable; n > 0 {
		out = append(out, fmt.Sprintf("%s not be read. mailmd saved the headers it could read, and the original as an .eml file. To see which, run: mailmd status %s",
			map[bool]string{true: "1 message could", false: count(n) + " messages could"}[n == 1], r.Account))
	}
	if r.Deleted > 0 {
		out = append(out, fmt.Sprintf("%s deleted on the server. %s in the archive.",
			map[bool]string{true: "1 message was", false: count(r.Deleted) + " messages were"}[r.Deleted == 1],
			map[bool]string{true: "Its file stays", false: "Their files stay"}[r.Deleted == 1]))
	}
	if r.Excluded > 0 {
		out = append(out, fmt.Sprintf("%s left out because %s only in folders this account does not sync, such as Trash or Spam.", map[bool]string{true: "1 message was", false: count(r.Excluded) + " messages were"}[r.Excluded == 1], map[bool]string{true: "it is", false: "they are"}[r.Excluded == 1]))
	}
	for _, f := range r.Folders {
		switch f.Status {
		case "missing":
			out = append(out, fmt.Sprintf("The folder %s is no longer on the server. Its files stay in the archive.", f.Path))
		case "failed":
			out = append(out, fmt.Sprintf("The folder %s could not be synced: %s", f.Path, strings.TrimSuffix(f.Reason, ".")+"."))
		}
	}
	return out
}
