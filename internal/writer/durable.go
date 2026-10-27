package writer

import (
	"context"
	"os"
	"path/filepath"

	"github.com/vincentsch/mailmd/internal/privatefs"
)

func hasPrivateMode(info os.FileInfo, perm os.FileMode) bool {
	return privatefs.HasMode(info.Mode(), perm)
}

// directoriesNeedRepair reports whether a folder between root and target is
// missing or not private. It changes nothing. The caller has already refused
// symbolic links and files in the place of these folders.
func directoriesNeedRepair(fs fileSystem, root, target string) (bool, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	current := root
	for _, part := range splitRelPath(rel) {
		current = filepath.Join(current, part)
		info, err := fs.Lstat(current)
		if os.IsNotExist(err) {
			return true, nil
		}
		if err != nil {
			return false, &PathError{Op: "open", Path: current, Err: err}
		}
		if !hasPrivateMode(info, 0o700) {
			return true, nil
		}
	}
	return false, nil
}

// ensureManagedDirs creates the folders below root with private
// permissions. If root already exists, its permissions stay as they are. Each
// new folder goes into the batch together with its parent, so the batch
// flush writes it to disk.
func ensureManagedDirs(ctx context.Context, fs fileSystem, root, target string, batch *Batch) error {
	var missingRoots []string
	for p := root; ; p = filepath.Dir(p) {
		info, err := fs.Lstat(p)
		if os.IsNotExist(err) {
			missingRoots = append(missingRoots, p)
		} else if err != nil {
			return &PathError{Op: "open", Path: p, Err: err}
		} else {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return &PathError{Op: "open", Path: p, Err: ErrUnsafePath}
			}
			break
		}
	}
	for i := len(missingRoots) - 1; i >= 0; i-- {
		p := missingRoots[i]
		if err := fs.MkdirAll(p, 0o700); err != nil {
			return &PathError{Op: "mkdir", Path: p, Err: err}
		}
		if err := fs.Chmod(p, 0o700); err != nil {
			return &PathError{Op: "chmod", Path: p, Err: err}
		}
		batch.addDir(p)
		batch.addDir(filepath.Dir(p))
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	current := root
	for _, part := range splitRelPath(rel) {
		if err := checkContext(ctx); err != nil {
			return err
		}
		current = filepath.Join(current, part)
		info, err := fs.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return &PathError{Op: "open", Path: current, Err: err}
		}
		missing := os.IsNotExist(err)
		if missing {
			if err := fs.MkdirAll(current, 0o700); err != nil {
				return &PathError{Op: "mkdir", Path: current, Err: err}
			}
			batch.addDir(current)
			batch.addDir(filepath.Dir(current))
		} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return &PathError{Op: "open", Path: current, Err: ErrUnsafePath}
		}
		if missing || !hasPrivateMode(info, 0o700) {
			if err := fs.Chmod(current, 0o700); err != nil {
				return &PathError{Op: "chmod", Path: current, Err: err}
			}
		}
	}
	return nil
}
