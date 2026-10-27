package imap

import (
	"errors"

	"golang.org/x/sys/windows"
)

// connectionRefused checks the Windows error code, which differs from the
// Unix one that syscall.ECONNREFUSED stands for.
func connectionRefused(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }
