// Package privatefs writes mailmd's private files, like the account list and
// the logins, so that only the user can read them. A write replaces a file
// completely or leaves the old version, even after a crash. Symbolic links
// below mailmd's folders are refused.
package privatefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrIntegrity = errors.New("private storage: integrity failure")
var ErrWrite = errors.New("private storage: durable write failed")

// Hooks let tests make one step fail, like a rename or a flush to disk. A nil
// field uses the real function.
type Hooks struct {
	Chmod    func(string, os.FileMode) error
	SyncFile func(*os.File) error
	SyncDir  func(string) error
	Rename   func(string, string) error
	Remove   func(string) error
}

func (h Hooks) defaults() Hooks {
	if h.Chmod == nil {
		h.Chmod = os.Chmod
	}
	if h.SyncFile == nil {
		h.SyncFile = (*os.File).Sync
	}
	if h.SyncDir == nil {
		h.SyncDir = SyncDir
	}
	if h.Rename == nil {
		h.Rename = os.Rename
	}
	if h.Remove == nil {
		h.Remove = os.Remove
	}
	return h
}

// ResolveRoot returns the real path of a folder the user chose, like the
// archive root or the config folder. The folder, or one of its parents, may
// be a symbolic link, for example to an external disk or because /home links
// to /var/home. Below the returned path, mailmd accepts no symbolic links.
func ResolveRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", ErrIntegrity
	}
	existing, rest := abs, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", ErrIntegrity
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", ErrIntegrity
	}
	return filepath.Join(real, rest), nil
}

// CheckPath fails when the path or one of its parents is a symbolic link.
// Links that only root can change are fine.
func CheckPath(path string) error {
	p, e := filepath.Abs(path)
	if e != nil {
		return ErrIntegrity
	}
	for {
		info, e := os.Lstat(p)
		if e != nil && !os.IsNotExist(e) {
			return ErrIntegrity
		}
		if e == nil && info.Mode()&os.ModeSymlink != 0 && !TrustedLink(info) {
			return ErrIntegrity
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return nil
}

// EnsureDir creates dir and any missing folders between root and dir, and
// makes them private to the user (0700).
func EnsureDir(root, dir string, hooks Hooks) error {
	h := hooks.defaults()
	root, e := filepath.Abs(root)
	if e != nil {
		return ErrIntegrity
	}
	dir, e = filepath.Abs(dir)
	if e != nil {
		return ErrIntegrity
	}
	rel, e := filepath.Rel(root, dir)
	if e != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return ErrIntegrity
	}
	if e = CheckPath(dir); e != nil {
		return e
	}
	var missing []string
	for p := dir; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if os.IsNotExist(e) {
			missing = append(missing, p)
		} else if e != nil || !info.IsDir() {
			return ErrIntegrity
		} else {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if e = os.Mkdir(missing[i], 0o700); e != nil {
			return errors.Join(ErrWrite, e)
		}
		if e = h.Chmod(missing[i], 0o700); e != nil {
			return errors.Join(ErrWrite, e)
		}
		if e = h.SyncDir(filepath.Dir(missing[i])); e != nil {
			return errors.Join(ErrWrite, e)
		}
	}
	// The root belongs to the user, so its permissions stay as they are. Only
	// the folders below it are made private.
	for p := dir; p != root; p = filepath.Dir(p) {
		if e = h.Chmod(p, 0o700); e != nil {
			return errors.Join(ErrWrite, e)
		}
		if e = h.SyncDir(p); e != nil {
			return errors.Join(ErrWrite, e)
		}
		if e = h.SyncDir(filepath.Dir(p)); e != nil {
			return errors.Join(ErrWrite, e)
		}
	}
	return nil
}

// Read reads a regular file of at most max bytes. It fails when the path
// contains a symbolic link.
func Read(path string, max int64) ([]byte, error) {
	if e := CheckPath(path); e != nil {
		return nil, e
	}
	info, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > max {
		return nil, ErrIntegrity
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, ErrIntegrity
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, ErrIntegrity
	}
	return b, nil
}

// Decode reads JSON more strictly than encoding/json does. A key that appears
// twice is an error, and so is a field that dst doesn't have.
func Decode(data []byte, dst any) error {
	keys := json.NewDecoder(bytes.NewReader(data))
	if uniqueJSON(keys, 0) != nil {
		return ErrIntegrity
	}
	if _, err := keys.Token(); err != io.EOF {
		return ErrIntegrity
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return ErrIntegrity
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrIntegrity
	}
	return nil
}

func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 128 {
		return ErrIntegrity
	}
	token, e := d.Token()
	if e != nil {
		return ErrIntegrity
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return ErrIntegrity
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return ErrIntegrity
			}
			seen[name] = true
			if uniqueJSON(d, depth+1) != nil {
				return ErrIntegrity
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrIntegrity
		}
	case '[':
		for d.More() {
			if uniqueJSON(d, depth+1) != nil {
				return ErrIntegrity
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return ErrIntegrity
		}
	default:
		return ErrIntegrity
	}
	return nil
}

// WriteFile replaces one private file. It writes the new content to a
// temporary file next to it and flushes that to disk. Then it renames the
// temporary file into place, so a reader sees the old or the new content and
// never a mix. WriteFile has no size limit, because it makes no backup like
// Replace does.
func WriteFile(path string, data []byte, hooks Hooks) error {
	h := hooks.defaults()
	if CheckPath(path) != nil {
		return ErrIntegrity
	}
	if info, e := os.Lstat(path); e == nil && !info.Mode().IsRegular() {
		return ErrIntegrity
	} else if e != nil && !os.IsNotExist(e) {
		return errors.Join(ErrWrite, e)
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".mailmd-private-*")
	if e != nil {
		return errors.Join(ErrWrite, e)
	}
	name := f.Name()
	if e = f.Chmod(0o600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = h.SyncFile(f)
	}
	if e = errors.Join(e, f.Close()); e == nil {
		e = h.Rename(name, path)
	}
	if e != nil {
		_ = h.Remove(name)
		return errors.Join(ErrWrite, e)
	}
	if e = h.SyncDir(filepath.Dir(path)); e != nil {
		return errors.Join(ErrWrite, e)
	}
	return nil
}

type Replacement struct {
	Path string
	Data []byte
}

// Replace replaces several files, one after the other, and each file in one
// step. If a later file fails, Replace puts back the old content of the files
// it already replaced. For that, it first writes a backup of each old file to
// disk, so an old file can be at most 64 MiB.
func Replace(files []Replacement, hooks Hooks) error {
	h := hooks.defaults()
	type staged struct {
		target, tmp, backup string
		oldData             []byte
		oldTime             time.Time
		old                 bool
		promoted            bool
	}
	items := make([]staged, 0, len(files))
	cleanup := func() {
		for _, s := range items {
			if s.tmp != "" {
				_ = h.Remove(s.tmp)
			}
			if s.backup != "" {
				_ = h.Remove(s.backup)
			}
		}
	}
	defer cleanup()
	write := func(path string, b []byte) (string, error) {
		f, e := os.CreateTemp(filepath.Dir(path), ".mailmd-private-*")
		if e != nil {
			return "", e
		}
		name := f.Name()
		if e = f.Chmod(0o600); e == nil {
			var n int
			n, e = f.Write(b)
			if e == nil && n != len(b) {
				e = io.ErrShortWrite
			}
		}
		if e == nil {
			e = h.SyncFile(f)
		}
		e = errors.Join(e, f.Close())
		if e != nil {
			_ = h.Remove(name)
			return "", e
		}
		return name, nil
	}
	restoreTime := func(path string, modified time.Time) error {
		if e := os.Chtimes(path, modified, modified); e != nil {
			return e
		}
		// On Windows, only a handle that may write can flush a file.
		f, e := os.OpenFile(path, os.O_RDWR, 0)
		if e != nil {
			return e
		}
		e = h.SyncFile(f)
		return errors.Join(e, f.Close())
	}
	for _, r := range files {
		if CheckPath(r.Path) != nil {
			return ErrIntegrity
		}
		s := staged{target: r.Path}
		old, e := Read(r.Path, 64<<20)
		if e == nil {
			s.old = true
			s.oldData = old
			info, statErr := os.Lstat(r.Path)
			if statErr != nil || !HasMode(info.Mode(), 0o600) {
				return ErrIntegrity
			}
			s.oldTime = info.ModTime()
			s.backup, e = write(r.Path, old)
			if e == nil {
				e = restoreTime(s.backup, s.oldTime)
			}
			if e != nil && s.backup != "" {
				_ = h.Remove(s.backup)
			}
		} else if os.IsNotExist(e) {
			e = nil
		}
		if e != nil {
			return errors.Join(ErrWrite, e)
		}
		s.tmp, e = write(r.Path, r.Data)
		items = append(items, s)
		if e != nil {
			return errors.Join(ErrWrite, e)
		}
	}
	rollback := func(cause error) error {
		var errs []error
		for i := len(items) - 1; i >= 0; i-- {
			s := items[i]
			if !s.promoted {
				continue
			}
			var e error
			if s.old {
				backup := s.backup
				if backup == "" {
					backup, e = write(s.target, s.oldData)
					if e == nil {
						e = restoreTime(backup, s.oldTime)
					}
				}
				if e == nil {
					e = h.Rename(backup, s.target)
				}
			} else {
				e = h.Remove(s.target)
			}
			if e == nil {
				e = h.SyncDir(filepath.Dir(s.target))
			}
			errs = append(errs, e)
		}
		return errors.Join(append([]error{ErrWrite, cause}, errs...)...)
	}
	for i := range items {
		s := &items[i]
		if e := h.Rename(s.tmp, s.target); e != nil {
			return rollback(e)
		}
		s.promoted = true
		if e := h.SyncDir(filepath.Dir(s.target)); e != nil {
			return rollback(e)
		}
	}
	// Remove the backups, then flush the folders, so the backups don't come
	// back after a crash.
	for i := range items {
		s := &items[i]
		if s.backup != "" {
			if e := h.Remove(s.backup); e != nil {
				return rollback(e)
			}
			s.backup = ""
		}
	}
	for _, s := range items {
		if e := h.SyncDir(filepath.Dir(s.target)); e != nil {
			return rollback(e)
		}
	}
	return nil
}
