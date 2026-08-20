//go:build linux

package hangar

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameNoReplaceAt(parent *os.File, oldName, newName string) error {
	return renameNoReplaceBetween(parent, oldName, parent, newName)
}

func renameNoReplaceBetween(oldParent *os.File, oldName string, newParent *os.File, newName string) error {
	return unix.Renameat2(int(oldParent.Fd()), oldName, int(newParent.Fd()), newName, unix.RENAME_NOREPLACE)
}
