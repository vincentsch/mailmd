package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

// shortPath shows a path below the home directory as ~/...
func shortPath(path, home string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return "~" + string(filepath.Separator) + rel
	}
	return path
}

// count formats a number with thousands separators, like 12,345.
func count(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return s
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// plural returns "1 message" or "2 messages".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return count(n) + " " + many
}

// seconds formats a duration for a summary line.
func seconds(s float64) string {
	switch {
	case s < 1:
		return "less than a second"
	case s < 60:
		return fmt.Sprintf("%.0f s", s)
	case s < 3600:
		return fmt.Sprintf("%d min %d s", int(s)/60, int(s)%60)
	}
	return fmt.Sprintf("%d h %d min", int(s)/3600, int(s)%3600/60)
}

// ago describes a past time relative to now, in local time.
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute", "minutes") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour", "hours") + " ago"
	case d < 7*24*time.Hour:
		return plural(int(d.Hours()/24), "day", "days") + " ago"
	}
	return t.Local().Format("Jan 2, 2006 15:04")
}

// joinNames joins a few names for a sentence: "A", "A and B", "A, B and 3 more".
func joinNames(names []string) string {
	switch {
	case len(names) == 0:
		return ""
	case len(names) == 1:
		return names[0]
	case len(names) <= 4:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	return strings.Join(names[:3], ", ") + " and " + plural(len(names)-3, "more", "more")
}

func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
}

// isTerminal reports whether a reader or writer is a terminal.
func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func providerName(kind string) string {
	switch kind {
	case "imap":
		return "IMAP"
	case "gmail":
		return "Gmail"
	case "graph":
		return "Outlook"
	}
	return kind
}
