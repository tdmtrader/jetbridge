//go:build darwin

package hangar

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameNoReplaceAt(parent *os.File, oldName, newName string) error {
	return unix.RenameatxNp(int(parent.Fd()), oldName, int(parent.Fd()), newName, unix.RENAME_EXCL)
}
