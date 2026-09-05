//go:build !linux && !darwin

package repository

import (
	"fmt"
	"os"
)

func restoreCheckoutDirectoryMetadata(root *os.Root, path string, recreated os.FileInfo, original checkoutDirectoryMetadata) (*os.File, error) {
	return nil, &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("directory %q ownership cannot be restored on this platform", path)}
}

func checkCheckoutDirectoryPermissions(root *os.Root, path string, expected os.FileInfo) (checkoutDirectoryMetadata, error) {
	return checkoutDirectoryMetadata{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("directory %q extended permissions cannot be inspected on this platform", path)}
}

func checkoutDirectoryProvenanceEqual(left, right checkoutDirectoryMetadata) bool {
	return !left.hasProvenance && !right.hasProvenance
}

func (prepared *preparedCheckoutFiles) restoreBackupAt(record *preparedCheckoutFile, parent *os.File, ancestors []string) error {
	return &Error{Code: CodeOutcomeUnknown, Message: "descriptor-relative checkout recovery is unsupported on this platform"}
}
