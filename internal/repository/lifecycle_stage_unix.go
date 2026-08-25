//go:build linux || darwin

package repository

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func mkdirOwnedStageParent(root, parent string) error {
	if filepath.Dir(parent) != root {
		return fmt.Errorf("operation staging directory must be a direct child of the Worktree root")
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(rootFD) }()
	name := filepath.Base(parent)
	if err = unix.Mkdirat(rootFD, name, 0o700); err != nil {
		return err
	}
	stageFD, err := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	return unix.Close(stageFD)
}

func openDestinationParent(root, parent string) (*os.File, error) {
	return openDirectory(root, parent, true)
}

func openExistingDirectory(root, parent string) (*os.File, error) {
	return openDirectory(root, parent, false)
}

func openDirectory(root, parent string, create bool) (*os.File, error) {
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("destination parent escapes the Worktree root")
	}
	current, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if relative == "." {
		return os.NewFile(uintptr(current), root), nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr == unix.ENOENT && create {
			if mkdirErr := unix.Mkdirat(current, component, 0o755); mkdirErr != nil && mkdirErr != unix.EEXIST {
				_ = unix.Close(current)
				return nil, mkdirErr
			}
			if syncErr := unix.Fsync(current); syncErr != nil {
				_ = unix.Close(current)
				return nil, syncErr
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		_ = unix.Close(current)
		if openErr != nil {
			return nil, openErr
		}
		current = next
	}
	return os.NewFile(uintptr(current), parent), nil
}
