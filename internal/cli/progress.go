package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vincentsch/mailmd/internal/accounts"
	mdsync "github.com/vincentsch/mailmd/internal/sync"
	rg "github.com/vincentsch/rungrad"
)

// progress prints one line per folder on stderr while a sync runs. It
// prints only to a terminal, and not with --quiet or machine output, so cron
// jobs and scripts stay quiet.
type progress struct {
	w       io.Writer
	account accounts.AccountConfig
	home    string
	live    bool
	width   int
}

func newProgress(f *rg.Factory, a accounts.AccountConfig, home string) *progress {
	p := &progress{w: f.Stderr, account: a, home: home}
	machine := f.Flags != nil && (f.Flags.JSON || f.Flags.JQ != "" || f.Flags.Template != "")
	quiet := f.Flags != nil && f.Flags.Quiet
	p.live = !machine && !quiet && isTerminal(f.Stderr)
	return p
}

func (p *progress) start(root string) {
	if !p.live {
		return
	}
	dir := filepath.Join(root, p.account.ArchiveDir)
	fmt.Fprintf(p.w, "Syncing %s to %s\n", p.account.Name, shortPath(dir, p.home))
}

func (p *progress) update(e mdsync.Progress) {
	if !p.live {
		return
	}
	if !e.Finished {
		line := fmt.Sprintf("  %-24s %s", e.Folder, count(e.Done))
		if e.Remaining > 0 {
			line += " of " + count(e.Done+e.Remaining)
		}
		p.overwrite(line)
		return
	}
	r := e.Result
	text := ""
	switch r.Status {
	case "missing":
		text = "no longer on the server"
	case "failed":
		text = "failed"
	default:
		text = "up to date"
		if r.New > 0 {
			text = count(r.New) + " new"
		}
	}
	p.overwrite(fmt.Sprintf("  %-24s %s", e.Folder, text))
	fmt.Fprintln(p.w)
	p.width = 0
}

// overwrite replaces the current line.
func (p *progress) overwrite(line string) {
	pad := ""
	if n := p.width - len(line); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	fmt.Fprint(p.w, "\r"+line+pad)
	p.width = len(line)
}

func (p *progress) finish(r mdsync.Result) {
	if !p.live {
		return
	}
	if p.width > 0 {
		fmt.Fprintln(p.w)
		p.width = 0
	}
	// Folders you left out with a filter are not listed. You know them.
	var left []string
	for _, f := range r.Folders {
		if f.Status == "excluded" && f.Reason == mdsync.ReasonLeftOutByDefault {
			left = append(left, f.Path)
		}
	}
	if len(left) > 0 {
		fmt.Fprintf(p.w, "  Left out: %s. To copy one of them, give it to mailmd connect with --include.\n", joinNames(left))
	}
}

func (m module) home() string {
	if m.opts.HomeDir != "" {
		return m.opts.HomeDir
	}
	home, _ := os.UserHomeDir()
	return home
}

func (m module) defaultRoot() (string, error) {
	if m.opts.HomeDir != "" {
		return filepath.Join(m.opts.HomeDir, "mail"), nil
	}
	return accounts.DefaultRoot()
}

// absPath expands a leading ~ and makes a path absolute.
func absPath(path, home string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home == "" {
			return "", newUserError(rg.ExitUsage, "Cannot find your home folder, so mailmd can't expand ~", "Give the full path instead.")
		}
		path = filepath.Join(home, path[1:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", newUserError(rg.ExitUsage, fmt.Sprintf("The path %q is not valid", path), "")
	}
	return abs, nil
}
