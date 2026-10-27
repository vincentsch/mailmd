//go:build !windows

package privatefs

import (
	"errors"
	"os"
	"syscall"
)

// SyncDir flushes a directory, so a rename or new file in it survives a
// crash.
func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	return errors.Join(err, f.Close())
}

// SyncFile flushes a file that is already written and closed.
func SyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	return errors.Join(err, f.Close())
}

// TrustedLink reports whether a symbolic link belongs to the system
// administrator, like /var on macOS or /home on Fedora Silverblue. Only
// root can change such a link, so mailmd may follow it.
func TrustedLink(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}
