package sync

import (
	"os"
	"sort"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend"
)

// StatusResult describes an account's archive. It comes from local files
// only, and the provider is never asked.
type StatusResult struct {
	Account string `json:"account"`
	Backend string `json:"backend"`
	Archive string `json:"archive"`
	// Synced is false when nothing was synced to this archive yet.
	Synced   bool `json:"synced"`
	Messages int  `json:"messages"`
	// Deleted counts the messages that were deleted on the server. Their files
	// stay.
	Deleted int            `json:"deleted_on_server"`
	Folders []FolderStatus `json:"folders"`
	// Missing lists provider folders that disappeared. Their files stay.
	Missing     []string                         `json:"missing_folders"`
	Diagnostics map[backend.DiagnosticReason]int `json:"diagnostics"`
	// Problems lists the most recent messages that were left out or saved
	// only in part.
	Problems []Diagnostic `json:"problem_messages"`
}

// FolderStatus counts the messages of one archive folder.
type FolderStatus struct {
	Path     string `json:"path"`
	Messages int    `json:"messages"`
}

// maxProblems is how many problem messages status lists.
const maxProblems = 20

// Status reads the archive of an account below root.
func Status(root string, a accounts.AccountConfig) (StatusResult, error) {
	r := StatusResult{Account: a.Name, Backend: a.Backend, Folders: []FolderStatus{}, Missing: []string{}, Diagnostics: map[backend.DiagnosticReason]int{}, Problems: []Diagnostic{}}
	dir, err := accountDir(root, a)
	if err != nil {
		return r, err
	}
	r.Archive = shownDir(root, a)
	state, saved, err := LoadState(dir, a.Backend)
	if err != nil {
		return r, err
	}
	r.Synced = saved != nil
	for _, f := range state.Folders {
		if f.Missing {
			r.Missing = append(r.Missing, f.Path)
		}
	}
	for _, d := range state.Diagnostics {
		r.Diagnostics[d.Reason]++
	}
	if n := len(state.Diagnostics); n > 0 {
		r.Problems = append(r.Problems, state.Diagnostics[max(0, n-maxProblems):]...)
	}
	entries, _, err := readIndex(dir)
	if err != nil && !os.IsNotExist(err) {
		return r, err
	}
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.Folder]++
		if e.Gone {
			r.Deleted++
		}
	}
	for folder, n := range counts {
		r.Folders = append(r.Folders, FolderStatus{Path: folder, Messages: n})
		r.Messages += n
	}
	sort.Slice(r.Folders, func(i, j int) bool { return r.Folders[i].Path < r.Folders[j].Path })
	return r, nil
}
