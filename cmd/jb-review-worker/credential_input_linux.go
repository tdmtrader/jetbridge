package main

import (
	"os"
	"syscall"
)

// Inherited stdin may be a blocking pipe outside Go's poller. Closing that
// descriptor from another goroutine does not interrupt an already blocked
// read, so an abandoned credential handoff can outlive the worker's deadline.
// NewFile registers a nonblocking duplicate with the poller; Close then wakes
// the credential read. No credential bytes are copied to another file.
func credentialInput() (*os.File, error) {
	fd, err := syscall.Dup(int(os.Stdin.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "review-credentials"), nil
}
