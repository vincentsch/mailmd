package sync

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/vincentsch/mailmd/internal/privatefs"
)

// ErrLocked means another mailmd process is syncing the same archive.
var ErrLocked = errors.New("sync: another sync of this archive is running")

// lockArchive locks the account's archive folder, so two syncs can't write
// the same files at the same time. The operating system releases the lock
// when the process ends, also after a crash.
func lockArchive(dir string) (func(), error) {
	meta := filepath.Join(dir, ".mailmd")
	if err := privatefs.EnsureDir(filepath.Dir(dir), meta, privatefs.Hooks{}); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(meta, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}, nil
}
