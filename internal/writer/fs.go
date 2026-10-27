package writer

import (
	"context"
	"os"
	"path/filepath"
)

const writeChunkSize = 1 << 20

// fileSystem is what the planner and the writer use to touch the disk. Tests
// replace it to make a write or a rename fail.
type fileSystem interface {
	Lstat(name string) (os.FileInfo, error)
	ReadFile(name string) ([]byte, error)
	ReadDir(name string) ([]os.DirEntry, error)
	MkdirAll(path string, perm os.FileMode) error
	Chmod(path string, perm os.FileMode) error
	WriteTemp(ctx context.Context, dir string, data []byte, perm os.FileMode) (string, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
}

// realFileSystem uses the os package.
type realFileSystem struct{}

func (realFileSystem) Lstat(name string) (os.FileInfo, error) {
	return os.Lstat(name)
}

func (realFileSystem) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (realFileSystem) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(name)
}

func (realFileSystem) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (realFileSystem) Chmod(path string, perm os.FileMode) error {
	return os.Chmod(path, perm)
}

// WriteTemp writes into the target folder in chunks, so Ctrl-C can stop a
// large attachment before the final rename.
func (realFileSystem) WriteTemp(ctx context.Context, dir string, data []byte, perm os.FileMode) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}

	f, err := os.CreateTemp(dir, ".mailmd-tmp-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(name)
		}
	}()

	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return "", err
	}

	for off := 0; off < len(data); {
		if err := checkContext(ctx); err != nil {
			_ = f.Close()
			return "", err
		}
		end := off + writeChunkSize
		if end > len(data) {
			end = len(data)
		}
		if _, err := f.Write(data[off:end]); err != nil {
			_ = f.Close()
			return "", err
		}
		off = end
	}

	if err := checkContext(ctx); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	cleanup = false
	return filepath.Clean(name), nil
}

func (realFileSystem) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

func (realFileSystem) Remove(name string) error {
	return os.Remove(name)
}
