package cli

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/demo"
	mdsync "github.com/vincentsch/mailmd/internal/sync"
	rg "github.com/vincentsch/rungrad"
	"github.com/vincentsch/rungrad/output"
)

// DemoResult is the JSON result of mailmd demo.
type DemoResult struct {
	Schema  string   `json:"schema"`
	Archive string   `json:"archive"`
	DryRun  bool     `json:"dry_run"`
	New     int      `json:"new"`
	Files   []string `json:"files"`
}

func (m module) demoCommand(cmd *rg.Command) {
	cmd.Configure = func(c *cobra.Command) {
		c.Flags().String("out", "mailmd-demo", "Folder for the sample archive")
	}
	cmd.Run = func(f *rg.Factory, c *cobra.Command, args []string) error {
		if err := checkGlobals(f); err != nil {
			return err
		}
		root, err := absPath(flagString(c, "out"), m.home())
		if err != nil {
			return err
		}
		service, err := demo.Service()
		if err != nil {
			return err
		}
		account := accounts.AccountConfig{Name: "demo", Backend: "mock", ArchiveDir: "demo", CredentialRef: "demo/mock.json"}
		dir := filepath.Join(root, account.ArchiveDir)
		dry := f.DryRun()
		result, err := mdsync.Run(c.Context(), mdsync.Options{
			Account:         account,
			Root:            root,
			DryRun:          dry,
			MaxMessageBytes: backend.DefaultMaxMessageBytes,
			MaxBatchBytes:   backend.DefaultMaxBatchBytes,
			Clock:           m.opts.Clock,
			Open:            func(ctx context.Context) (backend.Backend, error) { return service.Open(ctx) },
		})
		if err != nil {
			return err
		}
		out := DemoResult{Schema: "mailmd-demo/1", Archive: dir, DryRun: dry, New: result.New, Files: []string{}}
		if !dry {
			out.Files = listFiles(dir)
		}
		home := m.home()
		show := func(path string) string {
			if wd, err := os.Getwd(); err == nil {
				if rel, err := filepath.Rel(wd, path); err == nil && !strings.HasPrefix(rel, "..") {
					return "." + string(filepath.Separator) + rel
				}
			}
			return shortPath(path, home)
		}
		render := func(w io.Writer) {
			if dry {
				fmt.Fprintf(w, "mailmd demo would write %s of sample mail to %s. Nothing was written because of --dry-run.\n", plural(result.New, "message", "messages"), show(dir))
				return
			}
			if result.New > 0 {
				fmt.Fprintf(w, "Wrote %s of sample mail to %s:\n\n", plural(result.New, "message", "messages"), show(dir))
			} else {
				fmt.Fprintf(w, "The sample mail is already in %s:\n\n", show(dir))
			}
			for _, file := range out.Files {
				fmt.Fprintln(w, "  "+file)
			}
			fmt.Fprintln(w)
			fmt.Fprintln(w, "mailmd left out the spam folder, like a real sync does. Try these:")
			fmt.Fprintf(w, "  cat %s\n", show(filepath.Join(dir, firstMessage(out.Files))))
			fmt.Fprintf(w, "  grep -ril roadmap %s\n", show(dir))
			fmt.Fprintln(w)
			fmt.Fprintln(w, "To copy your own mailbox, run: mailmd connect imap --email you@example.com")
		}
		return f.WriteOutput(rg.Output{Model: out, Human: render, Plain: render, Meta: output.Meta{Extra: map[string]any{"command": c.CommandPath()}}})
	}
}

// listFiles lists the files of an archive, relative to it, without
// mailmd's own state folder.
func listFiles(dir string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".mailmd" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(dir, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func firstMessage(files []string) string {
	for _, f := range files {
		if strings.HasPrefix(f, "INBOX/") && strings.HasSuffix(f, ".md") {
			return filepath.FromSlash(f)
		}
	}
	return "index.jsonl"
}
