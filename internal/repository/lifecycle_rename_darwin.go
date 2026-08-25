//go:build darwin

package repository

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameNoReplace(source, destination string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCL)
}

func renameNoReplaceAt(sourceDirectory *os.File, source string, destinationDirectory *os.File, destination string) error {
	return unix.RenameatxNp(int(sourceDirectory.Fd()), source, int(destinationDirectory.Fd()), destination, unix.RENAME_EXCL)
}
