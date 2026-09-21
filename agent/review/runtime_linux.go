//go:build linux

package review

import (
	"errors"
	"golang.org/x/sys/unix"
)

func requireMemoryRuntime(dir string) error {
	var st unix.Statfs_t
	if dir == "" || unix.Statfs(dir, &st) != nil || st.Type != unix.TMPFS_MAGIC {
		return errors.New("runtime directory must be a private tmpfs mount (for example /dev/shm)")
	}
	return nil
}
