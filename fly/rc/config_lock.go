package rc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The stable lock file is separate from .flyrc because atomic writes replace its
// inode. The OS releases the lock if a fly process exits unexpectedly.
func withTargetsLock(fn func() error) error {
	f, err := os.OpenFile(flyrcPath()+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("lock fly configuration: %w", err)
	}
	defer f.Close()
	deadline := time.Now().Add(30 * time.Second)
	for {
		locked, err := tryConfigLock(f)
		if err != nil {
			return fmt.Errorf("lock fly configuration: %w", err)
		}
		if locked {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for fly configuration lock")
		}
		time.Sleep(25 * time.Millisecond)
	}
	defer unlockConfig(f)
	return fn()
}

func updateTargets(update func(Targets) error) error {
	return withTargetsLock(func() error {
		targets, err := LoadTargets()
		if err != nil {
			return err
		}
		if err := update(targets); err != nil {
			return err
		}
		return writeTargets(flyrcPath(), targets)
	})
}

func atomicWriteTargets(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".flyrc-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
