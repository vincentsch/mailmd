//go:build !windows

package imap

import (
	"errors"
	"syscall"
)

func connectionRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
