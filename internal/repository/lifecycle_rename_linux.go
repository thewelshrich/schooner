//go:build linux

package repository

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameNoReplace(source, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}

func renameNoReplaceAt(sourceDirectory *os.File, source string, destinationDirectory *os.File, destination string) error {
	return unix.Renameat2(int(sourceDirectory.Fd()), source, int(destinationDirectory.Fd()), destination, unix.RENAME_NOREPLACE)
}
