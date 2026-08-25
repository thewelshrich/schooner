//go:build !linux && !darwin

package repository

import (
	"fmt"
	"os"
	"path/filepath"
)

func mkdirOwnedStageParent(root, parent string) error {
	if filepath.Dir(parent) != root {
		return fmt.Errorf("operation staging directory must be a direct child of the Worktree root")
	}
	return os.Mkdir(parent, 0o700)
}

func openDestinationParent(root, parent string) (*os.File, error) {
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return nil, fmt.Errorf("destination parent changed during validation")
	}
	return os.Open(parent)
}

func openExistingDirectory(root, parent string) (*os.File, error) {
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return nil, fmt.Errorf("directory changed during validation")
	}
	return os.Open(parent)
}
