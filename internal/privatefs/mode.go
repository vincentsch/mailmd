package privatefs

import (
	"os"
	"runtime"
)

// HasMode reports whether a file or folder has exactly the permissions perm,
// such as 0600. Windows doesn't use Unix permission bits. There the files get
// the protection of the user's profile folder, so HasMode is always true.
func HasMode(mode os.FileMode, perm os.FileMode) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return mode.Perm() == perm && mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}
