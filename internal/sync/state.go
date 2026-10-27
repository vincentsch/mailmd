// Package sync copies mail from a provider into the archive. It saves a
// position for each folder, so each run fetches only what is new.
package sync

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/privatefs"
	"github.com/vincentsch/mailmd/internal/writer"
)

const stateSchema = "mailmd-state/1"

// maxDiagnostics is how many problem messages the state file lists. Older
// ones only count in DroppedDiagnostics.
const maxDiagnostics = 1000

var (
	ErrInvalidState = errors.New("sync: the state file is damaged")
	ErrStateWrite   = errors.New("sync: cannot write the state file")
)

// State is the sync position of one account. It lives in the archive at
// .mailmd/state.json and changes only when the archive changes.
type State struct {
	Schema             string        `json:"schema"`
	Backend            string        `json:"backend"`
	Folders            []FolderState `json:"folders"`
	Diagnostics        []Diagnostic  `json:"diagnostics,omitempty"`
	DroppedDiagnostics int           `json:"dropped_diagnostics,omitempty"`
}

// FolderState is the position in one provider folder.
type FolderState struct {
	Key     string `json:"key"`
	Path    string `json:"path"`
	Missing bool   `json:"missing,omitempty"`
	// Cursor is the backend's position, in base64. Only the backend reads it.
	Cursor string `json:"cursor,omitempty"`
	// Since is the --since day the cursor started from, as YYYY-MM-DD.
	Since string `json:"since,omitempty"`
	// Filter is the folder filter the cursor was made with, for a position
	// that covers several folders. See Filter.fingerprint.
	Filter string `json:"filter,omitempty"`
}

// Diagnostic records one message that was left out or saved only in part.
type Diagnostic struct {
	Folder string                   `json:"folder"`
	Ref    string                   `json:"ref"`
	Reason backend.DiagnosticReason `json:"reason"`
	Date   string                   `json:"date"`
	// Subject and Path name the message's file, when it has one.
	Subject string `json:"subject,omitempty"`
	Path    string `json:"path,omitempty"`
}

func freshState(kind string) State {
	return State{Schema: stateSchema, Backend: kind, Folders: []FolderState{}}
}

// statePath returns the state file of an account's archive folder.
func statePath(dir string) string {
	return filepath.Join(dir, ".mailmd", "state.json")
}

// accountDir returns the absolute archive folder of an account. shownDir is
// the folder as the user named it, before symbolic links are resolved. The
// output shows this path.
func shownDir(root string, a accounts.AccountConfig) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return filepath.Join(root, a.ArchiveDir)
}

func accountDir(root string, a accounts.AccountConfig) (string, error) {
	if root == "" || !accounts.ValidSegment(a.ArchiveDir) {
		return "", ErrInvalidState
	}
	abs, err := privatefs.ResolveRoot(root)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(abs, a.ArchiveDir)
	if privatefs.CheckPath(dir) != nil {
		return "", privatefs.ErrIntegrity
	}
	return dir, nil
}

// LoadState reads the state of an archive folder. A missing file means the
// account was never synced to this folder.
func LoadState(dir, kind string) (State, []byte, error) {
	data, err := privatefs.Read(statePath(dir), 64<<20)
	if os.IsNotExist(err) {
		return freshState(kind), nil, nil
	}
	if err != nil {
		return State{}, nil, ErrInvalidState
	}
	var s State
	if privatefs.Decode(data, &s) != nil || validateState(s, kind) != nil {
		return State{}, nil, ErrInvalidState
	}
	return s, data, nil
}

func validateState(s State, kind string) error {
	if s.Schema != stateSchema || s.Backend != kind {
		return ErrInvalidState
	}
	keys := map[string]bool{}
	for _, f := range s.Folders {
		if f.Key == "" || keys[f.Key] || len(f.Cursor) > backend.MaxEncodedCursorBytes {
			return ErrInvalidState
		}
		if _, err := base64.StdEncoding.Strict().DecodeString(f.Cursor); err != nil {
			return ErrInvalidState
		}
		if f.Since != "" {
			if _, err := time.Parse(time.DateOnly, f.Since); err != nil {
				return ErrInvalidState
			}
		}
		keys[f.Key] = true
	}
	return nil
}

// saveState writes the state when it differs from what is on disk. previous
// holds the bytes last read or written.
func saveState(dir string, s State, previous []byte) ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return previous, ErrStateWrite
	}
	data = append(data, '\n')
	if bytes.Equal(data, previous) {
		return previous, nil
	}
	path := statePath(dir)
	if err := privatefs.EnsureDir(filepath.Dir(dir), filepath.Dir(path), privatefs.Hooks{}); err != nil {
		return previous, errors.Join(ErrStateWrite, err)
	}
	if err := privatefs.Replace([]privatefs.Replacement{{Path: path, Data: data}}, privatefs.Hooks{}); err != nil {
		return previous, errors.Join(ErrStateWrite, err)
	}
	return data, nil
}

func (s *State) folder(key string) *FolderState {
	for i := range s.Folders {
		if s.Folders[i].Key == key {
			return &s.Folders[i]
		}
	}
	return nil
}

// addDiagnostics records problem messages and returns the ones it didn't
// know. A message that comes back under a new ref, because its folder got a
// new UIDVALIDITY, keeps its entry with the new ref. When the list is full,
// the newest ones stay. files maps a message's ref to its file.
func (s *State) addDiagnostics(folder string, items []backend.Diagnostic, now time.Time, files map[string]writer.IndexEntry) []backend.Diagnostic {
	seen := map[[3]string]bool{}
	byPath := map[[2]string]int{}
	for i, d := range s.Diagnostics {
		seen[[3]string{d.Folder, d.Ref, string(d.Reason)}] = true
		if d.Path != "" {
			byPath[[2]string{d.Path, string(d.Reason)}] = i
		}
	}
	var fresh []backend.Diagnostic
	for _, d := range items {
		key := [3]string{folder, d.Ref, string(d.Reason)}
		if seen[key] {
			continue
		}
		seen[key] = true
		file := files[d.Ref]
		if i, ok := byPath[[2]string{file.Path, string(d.Reason)}]; ok && file.Path != "" {
			s.Diagnostics[i].Folder, s.Diagnostics[i].Ref = folder, d.Ref
			continue
		}
		s.Diagnostics = append(s.Diagnostics, Diagnostic{Folder: folder, Ref: d.Ref, Reason: d.Reason, Date: now.UTC().Format(time.DateOnly), Subject: file.Subject, Path: file.Path})
		fresh = append(fresh, d)
	}
	if extra := len(s.Diagnostics) - maxDiagnostics; extra > 0 {
		s.Diagnostics = s.Diagnostics[extra:]
		s.DroppedDiagnostics += extra
	}
	return fresh
}

// dropFetchedInFull removes the too-large entries of messages that were
// fetched again and now have their full file. files maps a message's ref to
// its file.
func (s *State) dropFetchedInFull(files map[string]writer.IndexEntry) {
	kept := s.Diagnostics[:0]
	for _, d := range s.Diagnostics {
		if e, ok := files[d.Ref]; ok && d.Reason == backend.ReasonTooLarge && e.Omitted == 0 && e.Path == d.Path {
			continue
		}
		kept = append(kept, d)
	}
	s.Diagnostics = kept
}

func (s *State) sortFolders() {
	sort.Slice(s.Folders, func(i, j int) bool { return s.Folders[i].Key < s.Folders[j].Key })
}
