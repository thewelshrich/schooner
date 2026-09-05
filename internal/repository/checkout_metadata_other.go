//go:build !linux && !darwin

package repository

import (
	"fmt"
	"os"
)

func restoreCheckoutDirectoryMetadata(root *os.Root, path string, recreated, original os.FileInfo) error {
	return &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("directory %q ownership cannot be restored on this platform", path)}
}
