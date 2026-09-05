//go:build !linux && !darwin

package repository

import "os"

func removeCheckoutDirectory(root *os.Root, path string, expected checkoutDirectoryMetadata, preserveMetadata bool) (bool, error) {
	return false, &Error{Code: CodeUnsupported, Message: "identity-protected directory removal is unsupported on this platform"}
}
