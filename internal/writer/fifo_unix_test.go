//go:build !windows

package writer_test

import "syscall"

func makeFIFO(path string) error { return syscall.Mkfifo(path, 0o644) }
