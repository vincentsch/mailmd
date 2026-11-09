package sync

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vincentsch/mailmd/internal/backend"
)

// Filter decides which folders to sync.
type Filter struct {
	// Folders lists the folders to sync. Empty means all folders except the
	// default exclusions.
	Folders []string
	// Exclude lists folders to leave out.
	Exclude []string
	// gmailAllMail is the path of All Mail on Gmail's IMAP server. Without
	// --folder only that folder is synced, see ForFolders.
	gmailAllMail string
}

// ForFolders returns the filter for one account's folders.
//
// Gmail's IMAP server shows each label as a folder. Archived mail is only in
// [Gmail]/All Mail, which also has every other message once. So for Gmail,
// mailmd syncs only All Mail by default and gets every message once,
// archived mail included. With --folder, it syncs the named label folders
// instead.
func (f Filter) ForFolders(folders []backend.Folder) Filter {
	f.gmailAllMail = ""
	for _, folder := range folders {
		if folder.Role == backend.RoleAll && folder.Fetchable && isGmailIMAP(folder.Path) {
			f.gmailAllMail = folder.Path
		}
	}
	return f
}

// isGmailIMAP reports whether a folder path is one of Gmail's own IMAP
// folders, which are [Gmail]/... or, in some countries, [Google Mail]/...
func isGmailIMAP(path string) bool {
	return strings.HasPrefix(path, "[Gmail]/") || strings.HasPrefix(path, "[Google Mail]/")
}

// GmailAllMail returns the All Mail folder that a sync of Gmail over IMAP
// uses, or "" when the folders are not Gmail's or --folder is given.
func (f Filter) GmailAllMail() string {
	if len(f.Folders) > 0 {
		return ""
	}
	return f.gmailAllMail
}

// CheckMatches returns an error when a --folder pattern matches none of the
// folders. Otherwise a typo would quietly sync nothing on each run.
func (f Filter) CheckMatches(folders []backend.Folder) error {
	for _, pattern := range f.Folders {
		found := false
		for _, folder := range folders {
			if folder.Fetchable && matchesAny([]string{pattern}, folder.Path) {
				found = true
				break
			}
		}
		if !found {
			var names []string
			for _, folder := range folders {
				if folder.Fetchable {
					names = append(names, folder.Path)
				}
			}
			return &UnknownFolderError{Pattern: pattern, Folders: names}
		}
	}
	return nil
}

// UnknownFolderError reports a --folder pattern that matches no folder.
type UnknownFolderError struct {
	Pattern string
	Folders []string
}

func (e *UnknownFolderError) Error() string {
	return fmt.Sprintf("--folder %q matches no folder", e.Pattern)
}

// fingerprint describes the filter for the state file. The case and the
// order of the patterns don't change it.
func (f Filter) fingerprint() string {
	if len(f.Folders) == 0 && len(f.Exclude) == 0 {
		return ""
	}
	norm := func(list []string) string {
		out := make([]string, len(list))
		for i, p := range list {
			out[i] = strings.ToLower(strings.Trim(p, "/"))
		}
		sort.Strings(out)
		return strings.Join(out, "\x00")
	}
	return "folders=" + norm(f.Folders) + "\nexclude=" + norm(f.Exclude)
}

// defaultExcluded are the folder roles that a sync leaves out unless
// --folder names them. These are spam and trash, and folders that only
// repeat messages of other folders, like Gmail's "All Mail" and "Important".
var defaultExcluded = map[backend.Role]bool{
	backend.RoleJunk:      true,
	backend.RoleTrash:     true,
	backend.RoleAll:       true,
	backend.RoleImportant: true,
}

// Reasons why a sync leaves out a folder. They are part of the JSON output.
const (
	// ReasonLeftOutByDefault is for a folder that a sync leaves out unless
	// you ask for it, like spam and trash.
	ReasonLeftOutByDefault = "left out by default"
	ReasonExcluded         = "left out by --exclude"
	ReasonNotNamed         = "not named by --folder"
)

// Validate checks the patterns.
func (f Filter) Validate() error {
	for _, p := range append(append([]string(nil), f.Folders...), f.Exclude...) {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("a folder pattern is empty")
		}
		if _, err := path.Match(strings.ToLower(p), ""); err != nil {
			return fmt.Errorf("the folder pattern %q is not valid", p)
		}
	}
	return nil
}

// Excludes reports whether a folder is left out, and why.
func (f Filter) Excludes(folder string, role backend.Role) (bool, string) {
	if matchesAny(f.Exclude, folder) {
		return true, ReasonExcluded
	}
	if len(f.Folders) > 0 {
		if matchesAny(f.Folders, folder) {
			return false, ""
		}
		return true, ReasonNotNamed
	}
	if f.gmailAllMail != "" && folder != f.gmailAllMail && !defaultExcluded[role] {
		return true, "its mail is in " + f.gmailAllMail
	}
	if folder == f.gmailAllMail {
		return false, ""
	}
	if defaultExcluded[role] {
		return true, ReasonLeftOutByDefault
	}
	return false, ""
}

// KeepMessage decides about a message that has several labels, like a Gmail
// message. It is left out when one of its labels is excluded. Without
// --folder, it is also left out when it is in spam or trash. With --folder,
// it is synced only when --folder names one of its labels.
func (f Filter) KeepMessage(labels []string, roles map[string]backend.Role) bool {
	named := len(f.Folders) == 0
	for _, folder := range labels {
		role := roles[folder]
		if matchesAny(f.Exclude, folder) || (len(f.Folders) == 0 && (role == backend.RoleJunk || role == backend.RoleTrash)) {
			return false
		}
		if matchesAny(f.Folders, folder) {
			named = true
		}
	}
	return named
}

// matchesAny reports whether a pattern matches the folder or one of its
// parents, ignoring case. So "Work" also matches "Work/Clients".
func matchesAny(patterns []string, folder string) bool {
	folder = strings.ToLower(folder)
	for _, p := range patterns {
		p = strings.ToLower(strings.Trim(p, "/"))
		for f := folder; f != "" && f != "."; f = path.Dir(f) {
			if ok, _ := path.Match(p, f); ok {
				return true
			}
		}
	}
	return false
}

// ParseSince reads a --since value: a date like 2025-01-31, or an age like
// 30d, 6w, 12m or 2y, counted back from now. It returns the start of that
// day in UTC.
func ParseSince(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.DateOnly, value); err == nil {
		return t, nil
	}
	if len(value) >= 2 {
		n, err := strconv.Atoi(value[:len(value)-1])
		if err == nil && n > 0 && n < 10000 {
			now = now.UTC()
			var t time.Time
			switch value[len(value)-1] {
			case 'd':
				t = now.AddDate(0, 0, -n)
			case 'w':
				t = now.AddDate(0, 0, -7*n)
			case 'm':
				t = now.AddDate(0, -n, 0)
			case 'y':
				t = now.AddDate(-n, 0, 0)
			default:
				return time.Time{}, sinceError(value)
			}
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
		}
	}
	return time.Time{}, sinceError(value)
}

func sinceError(value string) error {
	return fmt.Errorf("--since %q is not a date like 2025-01-31 or an age like 30d, 12m or 2y", value)
}
