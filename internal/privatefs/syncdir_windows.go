package privatefs

import (
	"errors"
	"os"
)

// SyncDir does nothing on Windows. Windows can't flush a directory handle,
// and NTFS records renames in its own journal.
func SyncDir(string) error { return nil }

// SyncFile flushes a file that is already written and closed. Windows only
// flushes a file through a handle that may write.
func SyncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	return errors.Join(err, f.Close())
}

// TrustedLink reports whether mailmd may follow a symbolic link above its
// folders. Windows has no owner check like Unix, so it follows none.
func TrustedLink(os.FileInfo) bool { return false }
