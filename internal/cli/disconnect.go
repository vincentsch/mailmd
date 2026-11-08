package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/vincentsch/mailmd/internal/accounts"
	rg "github.com/vincentsch/rungrad"
	"github.com/vincentsch/rungrad/output"
)

// DisconnectResult is the JSON result of mailmd disconnect.
type DisconnectResult struct {
	Schema   string `json:"schema"`
	Account  string `json:"account"`
	Backend  string `json:"backend"`
	Provider string `json:"provider"`
	Address  string `json:"address"`
	DryRun   bool   `json:"dry_run"`
	// Deleted lists the files mailmd deleted, or would delete with --dry-run.
	Deleted []string `json:"deleted"`
	// Archive is the account's archive folder. mailmd leaves it as it is.
	Archive string `json:"archive"`
	// Note says where to take away the app's access at the provider.
	Note string `json:"note,omitempty"`
}

func (m module) disconnectCommand(cmd *rg.Command) {
	cmd.Use = "disconnect <account>"
	cmd.Args = cobra.ExactArgs(1)
	cmd.Run = m.disconnect
}

func (m module) disconnect(f *rg.Factory, c *cobra.Command, args []string) error {
	if err := checkGlobals(f); err != nil {
		return err
	}
	root, err := configRoot(c)
	if err != nil {
		return err
	}
	// This explains an unknown name and lists the connected accounts.
	if _, err := selectAccounts(root, args); err != nil {
		return err
	}
	dry := f.DryRun()
	a, deleted, err := accounts.Remove(root, args[0], dry)
	if err != nil {
		return disconnectError(args[0], err)
	}
	archive := a.Root
	if archive == "" {
		archive, _ = m.defaultRoot()
	}
	result := DisconnectResult{
		Schema: "mailmd-disconnect/1", Account: a.Name, Backend: a.Backend, Provider: providerName(a.Backend),
		Address: a.Email, DryRun: dry, Deleted: append([]string{}, deleted...),
		Archive: filepath.Join(archive, a.ArchiveDir), Note: accessNote(a.Backend),
	}
	home := m.home()
	render := func(w io.Writer) {
		who := a.Name
		if a.Email != "" {
			who += " (" + a.Email + ")"
		}
		switch {
		case dry && len(deleted) > 0:
			fmt.Fprintf(w, "mailmd would remove %s from its account list and delete these files:\n", who)
		case dry:
			fmt.Fprintf(w, "mailmd would remove %s from its account list. There is no saved login to delete.\n", who)
		case len(deleted) > 0:
			fmt.Fprintf(w, "Disconnected %s. mailmd removed it from its account list and deleted these files:\n", who)
		default:
			fmt.Fprintf(w, "Disconnected %s. mailmd removed it from its account list. There was no saved login to delete.\n", who)
		}
		for _, path := range deleted {
			fmt.Fprintln(w, "  "+shortPath(path, home))
		}
		line := fmt.Sprintf("The archive in %s stays as it is.", shortPath(result.Archive, home))
		if dry {
			line += " Nothing was changed because of --dry-run."
		}
		fmt.Fprintln(w, line)
		if result.Note != "" {
			fmt.Fprintln(w)
			fmt.Fprintln(w, result.Note)
		}
	}
	return f.WriteOutput(rg.Output{Model: result, Human: render, Plain: render, Meta: output.Meta{Extra: map[string]any{"command": c.CommandPath()}}})
}

// accessNote says where the user can take away the app's access, because
// deleting the saved login doesn't tell the provider anything.
func accessNote(backend string) string {
	switch backend {
	case "gmail":
		return "Google still lists the app you signed in with as allowed to read your mail. " +
			"To remove it, open \"Linked apps\" in your Google account: https://myaccount.google.com/connections"
	case "graph":
		return "Microsoft still lists the app you signed in with as allowed to read your mail. " +
			"To remove it, open My Apps at https://myapps.microsoft.com for a work or school account. " +
			"For a personal account, open https://account.live.com/consent/Manage"
	case "imap":
		return "If you made an app password for mailmd, you can delete it in your mail provider's settings."
	}
	return ""
}

func disconnectError(name string, err error) error {
	if errors.Is(err, accounts.ErrRegistry) {
		return registryError(err)
	}
	message := fmt.Sprintf("Cannot delete the files of %s in mailmd's config folder", name)
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		message += fmt.Sprintf(" (%s: %v)", pathErr.Path, pathErr.Err)
	}
	return newUserError(rg.ExitAPI, message,
		"mailmd does not follow symbolic links inside its config folder. Check that the folder and its files belong to you, then run the command again.")
}
