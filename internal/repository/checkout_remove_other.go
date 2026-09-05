//go:build !linux && !darwin

package repository

import "os"

func removeCheckoutDirectory(root *os.Root, path string, expected checkoutDirectoryMetadata, preserveMetadata bool) (bool, error) {
	return false, &Error{Code: CodeUnsupported, Message: "identity-protected directory removal is unsupported on this platform"}
}

func makeCheckoutDirectoryNoFollow(root *os.Root, path string, mode os.FileMode) error {
	return &Error{Code: CodeUnsupported, Message: "identity-protected directory recreation is unsupported on this platform"}
}

func verifyCheckoutDirectoryIdentity(root *os.Root, path string, expected os.FileInfo) error {
	return &Error{Code: CodeUnsupported, Message: "identity-protected directory verification is unsupported on this platform"}
}
