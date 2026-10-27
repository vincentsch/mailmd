// Package accounts saves the list of connected accounts and their logins in
// the user's config folder.
package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/mailmd/internal/privatefs"
)

const Schema = "mailmd-accounts/1"

var ErrRegistry = errors.New("accounts: invalid registry")
var ErrNotFound = errors.New("accounts: account not found")
var ErrKindMismatch = errors.New("accounts: account provider cannot be changed")

// Settings are the provider settings of an account. They hold no secrets.
type Settings struct {
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	TLSMode  string `json:"tls_mode,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	Tenant   string `json:"tenant,omitempty"`
}

// AccountConfig is one connected account.
type AccountConfig struct {
	Name    string `json:"name"`
	Backend string `json:"backend"`
	// Email is the mailbox's address, as the provider or the user gave it at
	// connect. It is empty when connect didn't learn it.
	Email         string   `json:"email,omitempty"`
	ArchiveDir    string   `json:"archive_dir"`
	CredentialRef string   `json:"credential_ref"`
	Settings      Settings `json:"settings"`
	// Root is the folder that holds the account's archive directory. Empty
	// means DefaultRoot.
	Root string `json:"root,omitempty"`
	// Folders, Exclude and Since are the sync filters saved with connect.
	// Flags given to sync replace them for that run.
	Folders []string `json:"folders,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	Since   string   `json:"since,omitempty"`
	// KeepRaw also saves the original of each message as an .eml file.
	KeepRaw bool `json:"keep_raw,omitempty"`
	// PreferText uses a message's plain text part instead of its HTML part.
	PreferText bool `json:"prefer_text,omitempty"`
}

// Registry is the list of accounts in accounts.json.
type Registry struct {
	Schema   string          `json:"schema"`
	Accounts []AccountConfig `json:"accounts"`
}

// DefaultRoot returns the folder for archives when the user doesn't choose
// one. It is "mail" in the home folder.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("cannot find your home folder. Choose a folder with --out")
	}
	return filepath.Join(home, "mail"), nil
}

// ArchiveRoot returns the account's root folder, or DefaultRoot.
func (a AccountConfig) ArchiveRoot() (string, error) {
	if a.Root != "" {
		return a.Root, nil
	}
	return DefaultRoot()
}

func credentialName(kind string) string {
	switch kind {
	case "imap":
		return "imap.json"
	case "gmail":
		return "gmail-token.json"
	case "graph":
		return "graph-token.json"
	case "mock":
		return "mock.json"
	}
	return ""
}
func validName(s string) bool {
	return strings.TrimSpace(s) == s && s != "" && len(s) <= 4096 && !strings.ContainsAny(s, "\x00\r\n")
}
func ValidSegment(s string) bool {
	return s != "" && s == mail.SafePathSegment(s) && s != "." && s != ".." && !strings.ContainsAny(s, "/\\")
}
func validate(r Registry) error {
	if r.Schema != Schema {
		return ErrRegistry
	}
	// Names and directories must differ in more than case, because macOS
	// and Windows file systems ignore case.
	names := map[string]bool{}
	dirs := map[string]bool{}
	for _, a := range r.Accounts {
		file := credentialName(a.Backend)
		name, dir := strings.ToLower(a.Name), strings.ToLower(a.ArchiveDir)
		if !validName(a.Name) || file == "" || strings.ContainsAny(a.Email, "\x00\r\n") || !ValidSegment(a.ArchiveDir) || a.CredentialRef != a.ArchiveDir+"/"+file || names[name] || dirs[dir] {
			return ErrRegistry
		}
		if a.Root != "" && !filepath.IsAbs(a.Root) {
			return ErrRegistry
		}
		names[name] = true
		dirs[dir] = true
		if a.Backend == "imap" && (a.Settings.Host == "" || a.Settings.Username == "" || a.Settings.Port < 0 || a.Settings.Port > 65535 || (a.Settings.TLSMode != "implicit" && a.Settings.TLSMode != "starttls")) {
			return ErrRegistry
		}
		if a.Backend != "imap" && (a.Settings.Host != "" || a.Settings.Username != "" || a.Settings.TLSMode != "" || a.Settings.Port != 0) {
			return ErrRegistry
		}
		if a.Backend != "graph" && (a.Settings.ClientID != "" || a.Settings.Tenant != "") {
			return ErrRegistry
		}
	}
	return nil
}
func Load(root string) (Registry, error) {
	if strings.TrimSpace(root) == "" {
		return Registry{}, ErrRegistry
	}
	data, e := privatefs.Read(filepath.Join(root, "mailmd", "accounts.json"), 4<<20)
	if os.IsNotExist(e) {
		return Registry{Schema: Schema, Accounts: []AccountConfig{}}, nil
	}
	if e != nil {
		// Wrap the cause, so the user learns whether the file is damaged or
		// can't be opened.
		return Registry{}, fmt.Errorf("%w: %w", ErrRegistry, e)
	}
	var r Registry
	if privatefs.Decode(data, &r) != nil || validate(r) != nil {
		return Registry{}, ErrRegistry
	}
	sort.Slice(r.Accounts, func(i, j int) bool { return r.Accounts[i].Name < r.Accounts[j].Name })
	return r, nil
}

// Find returns the account with this name. Case does not matter.
func (r Registry) Find(name string) (AccountConfig, error) {
	for _, a := range r.Accounts {
		if strings.EqualFold(a.Name, name) {
			return a, nil
		}
	}
	return AccountConfig{}, ErrNotFound
}
func ArchiveDir(name string, r Registry) (string, error) {
	if !validName(name) {
		return "", ErrRegistry
	}
	base := mail.SafePathSegment(name)
	used := map[string]bool{}
	for _, a := range r.Accounts {
		if strings.EqualFold(a.Name, name) {
			return a.ArchiveDir, nil
		}
		used[strings.ToLower(a.ArchiveDir)] = true
	}
	if !used[strings.ToLower(base)] {
		return base, nil
	}
	digest := sha256.Sum256([]byte("mailmd-archive-dir-v1\x00" + name))
	suffix := "-" + hex.EncodeToString(digest[:4])
	for len(base)+len(suffix) > 120 {
		_, n := utf8.DecodeLastRuneInString(base)
		base = base[:len(base)-n]
	}
	base += suffix
	if used[strings.ToLower(base)] || !ValidSegment(base) {
		return "", ErrRegistry
	}
	return base, nil
}

// Setup signs in and checks the login. It gets the name of a temporary folder
// below the config folder, may write only the provider's login file there,
// and returns the account's settings without secrets. It also returns the
// mailbox's email address, or "" when it doesn't know it.
type Setup func(context.Context, string) (Settings, string, error)

// Connect signs in through setup and saves the account. The new login goes
// into a temporary folder first, so a failed sign-in leaves the old login and
// accounts.json as they were.
func Connect(ctx context.Context, root, name, kind string, setup Setup, hooks privatefs.Hooks) (AccountConfig, error) {
	r, e := Load(root)
	if e != nil {
		return AccountConfig{}, e
	}
	if setup == nil || credentialName(kind) == "" {
		return AccountConfig{}, ErrRegistry
	}
	old, e := r.Find(name)
	if e == nil && old.Backend != kind {
		return AccountConfig{}, ErrKindMismatch
	}
	if e == nil {
		name = old.Name
	}
	dir, e := ArchiveDir(name, r)
	if e != nil {
		return AccountConfig{}, e
	}
	if e = privatefs.EnsureDir(root, filepath.Join(root, "mailmd"), hooks); e != nil {
		return AccountConfig{}, e
	}
	random := make([]byte, 16)
	if _, e = rand.Read(random); e != nil {
		return AccountConfig{}, privatefs.ErrWrite
	}
	stage := "connect-stage-" + hex.EncodeToString(random)
	stagePath := filepath.Join(root, "mailmd", stage)
	if !ValidSegment(stage) {
		return AccountConfig{}, ErrRegistry
	}
	if e = os.Mkdir(stagePath, 0o700); e != nil {
		return AccountConfig{}, privatefs.ErrWrite
	}
	if e = os.Chmod(stagePath, 0o700); e != nil {
		_ = os.Remove(stagePath)
		return AccountConfig{}, privatefs.ErrWrite
	}
	defer func() { _ = os.RemoveAll(stagePath); _ = privatefs.SyncDir(filepath.Dir(stagePath)) }()
	settings, email, e := setup(ctx, stage)
	if e != nil {
		return AccountConfig{}, e
	}
	if e = ctx.Err(); e != nil {
		return AccountConfig{}, e
	}
	file := credentialName(kind)
	data, e := privatefs.Read(filepath.Join(stagePath, file), 4<<20)
	if e != nil {
		return AccountConfig{}, privatefs.ErrIntegrity
	}
	if email == "" {
		email = old.Email
	}
	a := AccountConfig{Name: name, Backend: kind, Email: email, ArchiveDir: dir, CredentialRef: dir + "/" + file, Settings: settings,
		Root: old.Root, Folders: old.Folders, Exclude: old.Exclude, Since: old.Since, KeepRaw: old.KeepRaw, PreferText: old.PreferText}
	found := false
	for i := range r.Accounts {
		if r.Accounts[i].Name == name {
			r.Accounts[i] = a
			found = true
		}
	}
	if !found {
		r.Accounts = append(r.Accounts, a)
	}
	sort.Slice(r.Accounts, func(i, j int) bool { return r.Accounts[i].Name < r.Accounts[j].Name })
	if validate(r) != nil {
		return AccountConfig{}, ErrRegistry
	}
	registry, _ := json.MarshalIndent(r, "", "  ")
	registry = append(registry, '\n')
	targetDir := filepath.Join(root, "mailmd", dir)
	_, statErr := os.Lstat(targetDir)
	newDir := os.IsNotExist(statErr)
	if e = privatefs.EnsureDir(root, targetDir, hooks); e != nil {
		return AccountConfig{}, e
	}
	e = privatefs.Replace([]privatefs.Replacement{{Path: filepath.Join(targetDir, file), Data: data}, {Path: filepath.Join(root, "mailmd", "accounts.json"), Data: registry}}, hooks)
	if e != nil && newDir {
		_ = os.Remove(targetDir)
		_ = privatefs.SyncDir(filepath.Dir(targetDir))
	}
	if e != nil {
		return AccountConfig{}, e
	}
	return a, nil
}

// Update changes the saved settings of one account, such as its root
// folder or its filters.
func Update(root, name string, change func(*AccountConfig)) (AccountConfig, error) {
	r, err := Load(root)
	if err != nil {
		return AccountConfig{}, err
	}
	for i := range r.Accounts {
		if strings.EqualFold(r.Accounts[i].Name, name) {
			change(&r.Accounts[i])
			if validate(r) != nil {
				return AccountConfig{}, ErrRegistry
			}
			data, _ := json.MarshalIndent(r, "", "  ")
			path := filepath.Join(root, "mailmd", "accounts.json")
			if err := privatefs.Replace([]privatefs.Replacement{{Path: path, Data: append(data, '\n')}}, privatefs.Hooks{}); err != nil {
				return AccountConfig{}, err
			}
			return r.Accounts[i], nil
		}
	}
	return AccountConfig{}, ErrNotFound
}

// LastSync records how the last sync of an account went. It is saved in the
// config folder, so the archive doesn't change when a sync finds nothing new.
type LastSync struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Outcome  string    `json:"outcome"`
	New      int       `json:"new"`
	Error    string    `json:"error,omitempty"`
}

func lastSyncPath(root string, a AccountConfig) string {
	return filepath.Join(root, "mailmd", a.ArchiveDir, "last-sync.json")
}

// SaveLastSync stores the result of a sync.
func SaveLastSync(root string, a AccountConfig, info LastSync) error {
	if !ValidSegment(a.ArchiveDir) {
		return ErrRegistry
	}
	dir := filepath.Join(root, "mailmd", a.ArchiveDir)
	if err := privatefs.EnsureDir(root, dir, privatefs.Hooks{}); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(info, "", "  ")
	return privatefs.Replace([]privatefs.Replacement{{Path: lastSyncPath(root, a), Data: append(data, '\n')}}, privatefs.Hooks{})
}

// LoadLastSync returns the stored result of the last sync. ok is false when
// the account was never synced.
func LoadLastSync(root string, a AccountConfig) (info LastSync, ok bool) {
	if !ValidSegment(a.ArchiveDir) {
		return LastSync{}, false
	}
	data, err := privatefs.Read(lastSyncPath(root, a), 1<<20)
	if err != nil || json.Unmarshal(data, &info) != nil {
		return LastSync{}, false
	}
	return info, true
}
