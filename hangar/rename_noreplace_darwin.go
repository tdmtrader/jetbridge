//go:build darwin

package hangar

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameNoReplaceBetween(oldParent *os.File, oldName string, newParent *os.File, newName string) error {
	return unix.RenameatxNp(int(oldParent.Fd()), oldName, int(newParent.Fd()), newName, unix.RENAME_EXCL)
}
