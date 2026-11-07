//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/concourse/concourse/agent/session"
)

// openAuthFile opens the owner's auth.json from a private file a task wrote
// from its credential manager, the way a gate Run receives ((codex-auth)).
// The file must be absolute, a regular file and not a link, readable only by
// its owner, and on a tmpfs, so the credential never touches a disk. It is
// removed as soon as it is open: the worker holds the only reference, and
// nothing of it outlives the session. No error here quotes the file's
// content, and none reads it.
func openAuthFile(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("--auth-file must be an absolute path")
	}
	if err := session.RequireMemoryRuntime(filepath.Dir(path)); err != nil {
		return nil, errors.New("--auth-file must be on a private tmpfs mount (for example /dev/shm)")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("--auth-file cannot be opened: it must exist and not be a symbolic link")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		file.Close()
		return nil, errors.New("--auth-file must be a regular file readable only by its owner (mode 0600)")
	}
	if err := os.Remove(path); err != nil {
		file.Close()
		return nil, errors.New("--auth-file could not be removed once open")
	}
	return file, nil
}
