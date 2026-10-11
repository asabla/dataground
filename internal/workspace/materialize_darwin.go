package workspace

import (
	"os"

	"golang.org/x/sys/unix"
)

func materializationSupported() bool { return true }
func renameExclusive(parent *os.File, source, target string) error {
	return unix.RenameatxNp(int(parent.Fd()), source, int(parent.Fd()), target, unix.RENAME_EXCL)
}
