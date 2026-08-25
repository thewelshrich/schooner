//go:build linux || darwin

package repository

import (
	"fmt"
	"path/filepath"

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
