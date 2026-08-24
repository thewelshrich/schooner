package sqlite

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Schooner", "state.db"), nil
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "schooner", "state.db"), nil
}

// Destroy removes the active SQLite database and its transient sidecar files.
// It deliberately leaves provider resources, credential-store entries, SSH
// identities, and recoverable migration backups untouched.
func Destroy(path string) (bool, error) {
	removed := false
	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		err := os.Remove(candidate)
		if err == nil {
			removed = true
			continue
		}
		if os.IsNotExist(err) {
			continue
		}
		return removed, fmt.Errorf("remove local inventory %s: %w", filepath.Base(candidate), err)
	}
	return removed, nil
}
