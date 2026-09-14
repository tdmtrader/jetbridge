//go:build !windows

package rc

import (
	"errors"
	"os"
	"syscall"
)

func tryConfigLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}
func unlockConfig(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
