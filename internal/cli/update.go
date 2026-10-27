package cli

import (
	"errors"
	"regexp"

	"github.com/spf13/cobra"
	rg "github.com/vincentsch/rungrad"
	"github.com/vincentsch/rungrad/update"
)

type updateFetcher = update.Fetcher

func (m module) updateCommand(cmd *rg.Command) {
	fetcher := m.opts.UpdateFetcher
	if fetcher == nil {
		fetcher = update.GitHubFetcher{Owner: "vincentsch", Repo: "mailmd"}
	}
	u := update.Command(update.CommandConfig{CurrentVersion: m.opts.Version, Fetcher: safeFetcher{fetcher}, ToolName: "mailmd"})
	cmd.Configure = u.Configure
	cmd.Run = func(f *rg.Factory, c *cobra.Command, args []string) error {
		if check, _ := c.Flags().GetBool("check"); !check {
			return newUserError(rg.ExitUsage, "mailmd update only checks for a newer version. Add --check", "To install a newer version, run the install command from the README again.")
		}
		return u.Run(f, c, args)
	}
}

// safeFetcher hides the details of network errors from the output.
type safeFetcher struct{ update.Fetcher }

var releaseVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)

func (s safeFetcher) Latest() (update.Release, error) {
	r, err := s.Fetcher.Latest()
	if err != nil {
		return update.Release{}, errors.New("cannot reach GitHub to check for a release")
	}
	if !releaseVersion.MatchString(r.Version) {
		return update.Release{}, errors.New("GitHub returned an unexpected version number")
	}
	return update.Release{Version: r.Version}, nil
}
