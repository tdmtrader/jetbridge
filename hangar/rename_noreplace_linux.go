//go:build linux

package hangar

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameNoReplaceAt(parent *os.File, oldName, newName string) error {
	return unix.Renameat2(int(parent.Fd()), oldName, int(parent.Fd()), newName, unix.RENAME_NOREPLACE)
}
