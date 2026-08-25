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
