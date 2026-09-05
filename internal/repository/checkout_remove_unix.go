//go:build linux || darwin

package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Quarantine first, then verify: a swap at the source name must never be unlinked.
// The private directory and its open descriptor isolate verification/removal
// from further changes to the shared Worktree namespace.
func removeCheckoutDirectory(root *os.Root, path string, expected checkoutDirectoryMetadata, preserveMetadata bool) (mutated bool, result error) {
	parent, err := openCheckoutDirectoryNoFollow(root, filepath.Dir(path))
	if err != nil {
		return false, err
	}
	defer parent.Close()
	temporary, err := checkoutTemporaryPath(path)
	if err != nil {
		return false, err
	}
	name := filepath.Base(temporary)
	if err = unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		return false, err
	}
	var created unix.Stat_t
	if err = unix.Fstatat(int(parent.Fd()), name, &created, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("directory quarantine identity is unavailable at %q", filepath.Join(root.Name(), temporary)), Cause: err}
	}
	empty := true
	defer func() {
		if !empty {
			return
		}
		cleanupErr := removeEmptyCheckoutQuarantine(parent, name, created)
		if cleanupErr != nil {
			result = &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("directory quarantine cleanup could not be verified at %q", filepath.Join(root.Name(), temporary)), Cause: errors.Join(result, cleanupErr)}
		}
	}()
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	quarantine := os.NewFile(uintptr(fd), temporary)
	defer quarantine.Close()
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o777 != 0o700 || stat.Dev != created.Dev || stat.Ino != created.Ino {
		return false, &Error{Code: CodeConflict, Message: "directory quarantine is not private", Cause: err}
	}
	if err = renameNoReplaceAt(parent, filepath.Base(path), quarantine, "entry"); err != nil {
		return false, err
	}
	empty = false
	empty, err = removeQuarantinedCheckoutDirectory(parent, quarantine, path, filepath.Join(root.Name(), temporary, "entry"), expected, preserveMetadata)
	return true, err
}

func removeQuarantinedCheckoutDirectory(parent, quarantine *os.File, path, recoveryPath string, expected checkoutDirectoryMetadata, preserveMetadata bool) (bool, error) {
	moved, err := unix.Openat(int(quarantine.Fd()), "entry", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == nil {
		directory := os.NewFile(uintptr(moved), "entry")
		info, inspectErr := directory.Stat()
		err = inspectErr
		if err == nil && !os.SameFile(info, expected.info) {
			err = fmt.Errorf("directory identity changed")
		}
		if err == nil && preserveMetadata {
			metadata, inspectErr := inspectCheckoutDirectoryPermissions(directory, expected.info)
			err = inspectErr
			if err == nil && !checkoutDirectoryProvenanceEqual(metadata, expected) {
				err = fmt.Errorf("directory provenance changed")
			}
		}
		directory.Close()
	}
	if err == nil {
		// AT_REMOVEDIR cannot unlink a file or symlink even if inspection failed to
		// observe an unexpected entry type. Nonempty directories are never removed.
		err = unix.Unlinkat(int(quarantine.Fd()), "entry", unix.AT_REMOVEDIR)
		if err == nil {
			return true, nil
		}
	}
	restoreErr := renameNoReplaceAt(quarantine, "entry", parent, filepath.Base(path))
	if restoreErr == nil {
		return true, &Error{Code: CodeConflict, Message: fmt.Sprintf("directory %q changed or is no longer empty", path), Cause: err}
	}
	return false, &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("directory %q changed; displaced content preserved at %q", path, recoveryPath), Cause: errors.Join(err, restoreErr)}
}

func removeEmptyCheckoutQuarantine(parent *os.File, name string, expected unix.Stat_t) error {
	var current unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if current.Dev != expected.Dev || current.Ino != expected.Ino || current.Uid != uint32(os.Geteuid()) || current.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("quarantine identity changed")
	}
	return unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
}

// Each open is relative to the preceding pinned directory, so O_NOFOLLOW
// protects every component, including intermediate ancestors.
func openCheckoutDirectoryNoFollow(root *os.Root, path string) (*os.File, error) {
	if path != "." {
		if err := validateCheckoutPath(filepath.ToSlash(path)); err != nil {
			return nil, err
		}
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	if path == "." {
		return directory, nil
	}
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		fd, err := unix.Openat(int(directory.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		directory.Close()
		if err != nil {
			return nil, err
		}
		directory = os.NewFile(uintptr(fd), component)
	}
	return directory, nil
}

func makeCheckoutDirectoryNoFollow(root *os.Root, path string, mode os.FileMode) (info os.FileInfo, result error) {
	parent, err := openCheckoutDirectoryNoFollow(root, filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	temporary, err := checkoutTemporaryPath(path)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(temporary)
	if err = unix.Mkdirat(int(parent.Fd()), name, uint32(mode.Perm())); err != nil {
		return nil, err
	}
	var created unix.Stat_t
	if err = unix.Fstatat(int(parent.Fd()), name, &created, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("private directory retained at %q", filepath.Join(root.Name(), temporary)), Cause: err}
	}
	published := false
	defer func() {
		if !published {
			if err := removeEmptyCheckoutQuarantine(parent, name, created); err != nil {
				result = &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("private directory retained at %q", filepath.Join(root.Name(), temporary)), Cause: errors.Join(result, err)}
			}
		}
	}()
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), temporary)
	defer directory.Close()
	info, err = directory.Stat()
	if err != nil {
		return nil, err
	}
	var opened unix.Stat_t
	if err = unix.Fstat(fd, &opened); err != nil || opened.Dev != created.Dev || opened.Ino != created.Ino {
		return nil, &Error{Code: CodeConflict, Message: "private directory changed before publication", Cause: err}
	}
	if err = renameNoReplaceAt(parent, name, parent, filepath.Base(path)); err != nil {
		return nil, err
	}
	published = true
	// Return the descriptor's pre-publication identity, never trust a lookup
	// through the newly published name as the identity of our creation.
	return info, nil
}

func verifyCheckoutDirectoryIdentity(root *os.Root, path string, expected os.FileInfo) error {
	directory, err := openCheckoutDirectoryNoFollow(root, filepath.FromSlash(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, expected) {
		return fmt.Errorf("directory identity changed")
	}
	return nil
}
