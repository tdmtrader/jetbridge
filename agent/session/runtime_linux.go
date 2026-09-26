//go:build linux

package session

import (
	"errors"

	"golang.org/x/sys/unix"
)

// RequireMemoryRuntime refuses any runtime directory that is not tmpfs, so
// credentials and provider state never reach a disk-backed file system.
func RequireMemoryRuntime(dir string) error {
	var st unix.Statfs_t
	if dir == "" || unix.Statfs(dir, &st) != nil || st.Type != unix.TMPFS_MAGIC {
		return errors.New("runtime directory must be a private tmpfs mount (for example /dev/shm)")
	}
	return nil
}
