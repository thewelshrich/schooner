//go:build linux || darwin

package repository

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// Restore ownership before permissions: chown can clear setuid/setgid bits.
// Use the opened directory identity so a replacement cannot receive metadata.
func restoreCheckoutDirectoryMetadata(root *os.Root, path string, recreated os.FileInfo, original checkoutDirectoryMetadata) (result error) {
	defer func() {
		if result != nil {
			result = &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("directory %q ownership, permissions, or provenance could not be restored", path), Cause: result}
		}
	}()
	want, ok := original.info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("captured directory ownership is unavailable")
	}
	directory, err := root.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() || !os.SameFile(info, recreated) {
		return fmt.Errorf("recreated directory changed independently")
	}
	got, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("current directory ownership is unavailable")
	}
	if got.Uid != want.Uid || got.Gid != want.Gid {
		if err = directory.Chown(int(want.Uid), int(want.Gid)); err != nil {
			return err
		}
	}
	mode := original.info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	if err = directory.Chmod(mode); err != nil {
		return err
	}
	info, err = directory.Stat()
	if err != nil {
		return err
	}
	got, ok = info.Sys().(*syscall.Stat_t)
	if !ok || got.Uid != want.Uid || got.Gid != want.Gid || info.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != mode {
		return fmt.Errorf("directory ownership or permission bits did not match the backup")
	}
	metadata, err := checkoutDirectoryExtendedMetadata(directory)
	if err != nil {
		return err
	}
	if !checkoutDirectoryProvenanceEqual(metadata, original) {
		return fmt.Errorf("recreated directory provenance differs from the backup")
	}
	return nil
}

// Captures do not preserve ACLs or extended attributes. Inspect the pinned
// directory descriptor and refuse unsupported metadata before removal.
func checkCheckoutDirectoryPermissions(root *os.Root, path string, expected os.FileInfo) (metadata checkoutDirectoryMetadata, result error) {
	defer func() {
		if result != nil {
			result = &Error{Code: CodeConflict, Message: fmt.Sprintf("directory %q extended permissions or attributes cannot be safely preserved", path), Cause: result}
		}
	}()
	directory, err := root.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return metadata, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return metadata, err
	}
	if !os.SameFile(info, expected) {
		return metadata, fmt.Errorf("directory changed independently")
	}
	want, wantOK := expected.Sys().(*syscall.Stat_t)
	got, gotOK := info.Sys().(*syscall.Stat_t)
	if !wantOK || !gotOK || got.Uid != want.Uid || got.Gid != want.Gid || info.Mode() != expected.Mode() {
		return metadata, fmt.Errorf("directory ownership or mode changed independently")
	}
	metadata, err = checkoutDirectoryExtendedMetadata(directory)
	metadata.info = info
	return metadata, err
}

// Only Darwin creation provenance may survive automatically. Retain and compare
// presence and bytes; never write it or accept any other extended attribute.
func checkoutDirectoryExtendedMetadata(directory *os.File) (metadata checkoutDirectoryMetadata, err error) {
	size, err := unix.Flistxattr(int(directory.Fd()), nil)
	if err != nil {
		return metadata, err
	}
	if size != 0 {
		const name = "com.apple.provenance"
		if runtime.GOOS != "darwin" || size != len(name)+1 {
			return metadata, fmt.Errorf("directory has unsupported extended attributes")
		}
		names := make([]byte, size)
		n, err := unix.Flistxattr(int(directory.Fd()), names)
		if err != nil {
			return metadata, err
		}
		if n != len(names) || string(names) != name+"\x00" {
			return metadata, fmt.Errorf("directory has unsupported extended attributes")
		}
		size, err = unix.Fgetxattr(int(directory.Fd()), name, nil)
		if err != nil {
			return metadata, err
		}
		if size < 0 || size > 65536 {
			return metadata, fmt.Errorf("directory provenance exceeds supported size")
		}
		metadata.provenance = make([]byte, size)
		n, err = unix.Fgetxattr(int(directory.Fd()), name, metadata.provenance)
		if err != nil {
			return metadata, err
		}
		if n != size {
			return metadata, fmt.Errorf("directory provenance changed during inspection")
		}
		metadata.hasProvenance = true
	}
	return metadata, rejectCheckoutDirectoryACL(directory)
}

func checkoutDirectoryProvenanceEqual(left, right checkoutDirectoryMetadata) bool {
	return left.hasProvenance == right.hasProvenance && bytes.Equal(left.provenance, right.provenance)
}
