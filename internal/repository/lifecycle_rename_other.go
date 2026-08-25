//go:build !linux && !darwin

package repository

import (
	"errors"
	"os"
	"path/filepath"
)

func renameNoReplace(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}

func renameNoReplaceAt(sourceDirectory *os.File, source string, destinationDirectory *os.File, destination string) error {
	return renameNoReplace(filepath.Join(sourceDirectory.Name(), source), filepath.Join(destinationDirectory.Name(), destination))
}
