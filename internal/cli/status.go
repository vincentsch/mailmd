package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend"
	mdsync "github.com/vincentsch/mailmd/internal/sync"
	rg "github.com/vincentsch/rungrad"
	"github.com/vincentsch/rungrad/output"
)

// StatusOutput is the JSON result of mailmd status.
type StatusOutput struct {
	Schema   string          `json:"schema"`
	Accounts []AccountStatus `json:"accounts"`
}

// AccountStatus is the status of one account.
type AccountStatus struct {
	mdsync.StatusResult
	Provider string             `json:"provider"`
	Address  string             `json:"address"`
	LastSync *accounts.LastSync `json:"last_sync"`
	Error    *mdsync.ErrorInfo  `json:"error"`
}

func (m module) statusCommand(cmd *rg.Command) {
	cmd.Use = "status [account...]"
	cmd.Args = cobra.ArbitraryArgs
	cmd.Configure = func(c *cobra.Command) {
		c.Flags().String("out", "", "Look in this folder instead of the account's saved folder")
	}
	cmd.Run = m.status
}

func (m module) status(f *rg.Factory, c *cobra.Command, args []string) error {
	if err := checkGlobals(f); err != nil {
		return err
	}
	root, err := m.configRoot(c)
	if err != nil {
		return err
	}
	selected, err := selectAccounts(root, args)
	if err != nil {
		return err
	}
	out := StatusOutput{Schema: "mailmd-status/1", Accounts: []AccountStatus{}}
	var failure error
	for _, a := range selected {
		archive := flagString(c, "out")
		if archive == "" {
			archive = a.Root
		}
		if archive == "" {
			if archive, err = m.defaultRoot(); err != nil {
				return newUserError(rg.ExitUsage, upperFirst(err.Error()), "")
			}
		}
		if archive, err = absPath(archive, m.home()); err != nil {
			return err
		}
		s := AccountStatus{Provider: providerName(a.Backend), Address: a.Email}
		s.StatusResult, err = mdsync.Status(archive, a)
		if s.Archive == "" {
			s.Archive = filepath.Join(archive, a.ArchiveDir)
		}
		if err != nil {
			info := mdsync.Describe(err, a)
			s.Error = &info
			if failure == nil {
				failure = &userError{code: exitCode(info.Category), info: info}
			}
		}
		if last, ok := accounts.LoadLastSync(root, a); ok {
			s.LastSync = &last
		}
		out.Accounts = append(out.Accounts, s)
	}
	now := m.opts.Clock()
	home := m.home()
	render := func(w io.Writer) { renderStatus(w, out, home, now, len(args) == 1) }
	if err := f.WriteOutput(rg.Output{Model: out, Human: render, Plain: render, Meta: output.Meta{Extra: map[string]any{"command": c.CommandPath()}}}); err != nil {
		return err
	}
	return failure
}

func renderStatus(w io.Writer, out StatusOutput, home string, now time.Time, detail bool) {
	for i, s := range out.Accounts {
		if i > 0 {
			fmt.Fprintln(w)
		}
		title := s.Account + " (" + s.Provider
		if s.Address != "" {
			title += ", " + s.Address
		}
		fmt.Fprintln(w, title+")")
		t := newTable(w)
		fmt.Fprintf(t, "  Archive\t%s\n", shortPath(s.Archive, home))
		switch {
		case s.Error != nil:
			fmt.Fprintf(t, "  Problem\t%s\n", s.Error.Message)
		case !s.Synced:
			fmt.Fprintf(t, "  Messages\tnone yet. Run: mailmd sync %s\n", s.Account)
		default:
			fmt.Fprintf(t, "  Messages\t%s in %s\n", count(s.Messages), plural(len(s.Folders), "folder", "folders"))
		}
		if s.LastSync != nil {
			last := ago(s.LastSync.Finished, now) + ", " + outcomeText(s.LastSync.Outcome)
			if s.LastSync.New > 0 {
				last += ", " + count(s.LastSync.New) + " new"
			}
			fmt.Fprintf(t, "  Last sync\t%s\n", last)
			// The outcome already says that a sync was interrupted.
			if s.LastSync.Error != "" && s.LastSync.Outcome != mdsync.OutcomeInterrupted {
				fmt.Fprintf(t, "  \t%s\n", s.LastSync.Error)
			}
		}
		if s.Deleted > 0 {
			fmt.Fprintf(t, "  Deleted\t%s on the server, %s\n", plural(s.Deleted, "message", "messages"), map[bool]string{true: "the file stays", false: "the files stay"}[s.Deleted == 1])
		}
		if len(s.Missing) > 0 {
			fmt.Fprintf(t, "  Gone\t%s (no longer on the server, the files stay)\n", joinNames(s.Missing))
		}
		for i, line := range problemLines(s.Diagnostics) {
			label := "Problems"
			if i > 0 {
				label = ""
			}
			fmt.Fprintf(t, "  %s\t%s\n", label, line)
		}
		_ = t.Flush()
		if detail && len(s.Folders) > 0 {
			fmt.Fprintln(w)
			t = newTable(w)
			fmt.Fprintln(t, "  FOLDER\tMESSAGES")
			for _, folder := range s.Folders {
				fmt.Fprintf(t, "  %s\t%s\n", folder.Path, count(folder.Messages))
			}
			_ = t.Flush()
		}
		if detail && len(s.Problems) > 0 {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "  Messages that were left out or saved in part:")
			t = newTable(w)
			for _, p := range s.Problems {
				which := "message " + p.Ref
				if p.Path != "" {
					which = p.Path
				}
				fmt.Fprintf(t, "  %s\t%s\t%s\n", p.Date, which, reasonText(p.Reason))
			}
			_ = t.Flush()
		}
	}
}

func outcomeText(outcome string) string {
	switch outcome {
	case mdsync.OutcomeCompleted:
		return "completed"
	case mdsync.OutcomeWarnings:
		return "completed with warnings"
	case mdsync.OutcomeFolderErrors:
		return "some folders failed"
	case mdsync.OutcomeInterrupted:
		return "interrupted"
	}
	return "failed"
}

func reasonText(reason backend.DiagnosticReason) string {
	switch reason {
	case backend.ReasonTooLarge:
		return "too large, headers saved"
	case backend.ReasonParseFailure, backend.ReasonUnsupportedContent, backend.ReasonIdentityFailure:
		return "could not be read, original saved"
	case backend.ReasonVanished, backend.ReasonRemoteRemoved:
		return "deleted on the server while syncing"
	}
	return string(reason)
}

func problemLines(diagnostics map[backend.DiagnosticReason]int) []string {
	merged := map[string]int{}
	for reason, n := range diagnostics {
		if reason == backend.ReasonVanished || reason == backend.ReasonRemoteRemoved {
			continue
		}
		merged[reasonText(reason)] += n
	}
	var lines []string
	for text, n := range merged {
		lines = append(lines, fmt.Sprintf("%s %s", plural(n, "message", "messages"), text))
	}
	sort.Strings(lines)
	return lines
}

// AccountList is the JSON result of mailmd list accounts.
type AccountList struct {
	Schema   string        `json:"schema"`
	Accounts []AccountItem `json:"accounts"`
}

// AccountItem is one connected account.
type AccountItem struct {
	Name     string             `json:"name"`
	Provider string             `json:"provider"`
	Backend  string             `json:"backend"`
	Address  string             `json:"address"`
	Archive  string             `json:"archive"`
	LastSync *accounts.LastSync `json:"last_sync"`
}

func (m module) listAccountsCommand(cmd *rg.Command) {
	cmd.Run = func(f *rg.Factory, c *cobra.Command, args []string) error {
		if err := checkGlobals(f); err != nil {
			return err
		}
		root, err := m.configRoot(c)
		if err != nil {
			return err
		}
		r, err := accounts.Load(root)
		if err != nil {
			return registryError(err)
		}
		list := AccountList{Schema: "mailmd-account-list/1", Accounts: []AccountItem{}}
		for _, a := range r.Accounts {
			archive := a.Root
			if archive == "" {
				archive, _ = m.defaultRoot()
			}
			item := AccountItem{Name: a.Name, Provider: providerName(a.Backend), Backend: a.Backend, Address: a.Email, Archive: filepath.Join(archive, a.ArchiveDir)}
			if last, ok := accounts.LoadLastSync(root, a); ok {
				item.LastSync = &last
			}
			list.Accounts = append(list.Accounts, item)
		}
		now, home := m.opts.Clock(), m.home()
		render := func(w io.Writer) {
			if len(list.Accounts) == 0 {
				fmt.Fprintln(w, "No accounts are connected yet. Connect one with: mailmd connect imap --email you@example.com")
				return
			}
			t := newTable(w)
			fmt.Fprintln(t, "NAME\tPROVIDER\tADDRESS\tARCHIVE\tLAST SYNC")
			for _, a := range list.Accounts {
				last := "never"
				if a.LastSync != nil {
					last = ago(a.LastSync.Finished, now)
				}
				addr := a.Address
				if addr == "" {
					addr = "-"
				}
				fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n", a.Name, a.Provider, addr, shortPath(a.Archive, home), last)
			}
			_ = t.Flush()
		}
		return f.WriteOutput(rg.Output{Model: list, Human: render, Plain: render, Meta: output.Meta{Extra: map[string]any{"command": c.CommandPath()}}})
	}
}
