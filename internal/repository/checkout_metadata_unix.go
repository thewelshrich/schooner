//go:build linux || darwin

package repository

import (
	"fmt"
	"os"
	"syscall"
)

// Restore ownership before permissions: chown can clear setuid/setgid bits.
// Use the opened directory identity so a replacement cannot receive metadata.
func restoreCheckoutDirectoryMetadata(root *os.Root, path string, recreated, original os.FileInfo) (result error) {
	defer func() {
		if result != nil {
			result = &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("directory %q ownership or permissions could not be restored", path), Cause: result}
		}
	}()
	want, ok := original.Sys().(*syscall.Stat_t)
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
	mode := original.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
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
	return nil
}

// Captures do not preserve ACLs. Inspect the pinned directory, never a pathname
// handed to a subprocess, and refuse unsupported permissions before removal.
func checkCheckoutDirectoryPermissions(root *os.Root, path string, expected os.FileInfo) (result error) {
	defer func() {
		if result != nil {
			result = &Error{Code: CodeConflict, Message: fmt.Sprintf("directory %q extended permissions cannot be safely preserved", path), Cause: result}
		}
	}()
	directory, err := root.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, expected) {
		return fmt.Errorf("directory changed independently")
	}
	return rejectCheckoutDirectoryACL(directory)
}
