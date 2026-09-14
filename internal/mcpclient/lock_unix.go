//go:build !windows

package mcpclient

import (
	"errors"
	"os"
	"syscall"
)

func tryStateLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}
func unlockState(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
