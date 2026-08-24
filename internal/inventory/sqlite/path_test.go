package sqlite

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultPathPreservesDarwinLocationBeforeXDG(t *testing.T) {
	home := t.TempDir()
	state := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", state)
	got, err := DefaultPath()
	want := filepath.Join(state, "schooner", "state.db")
	if runtime.GOOS == "darwin" {
		want = filepath.Join(home, "Library", "Application Support", "Schooner", "state.db")
	}
	if err != nil || got != want {
		t.Fatalf("DefaultPath() = %q, %v; want %q", got, err, want)
	}
}

func TestDestroyIsIdempotentAndPreservesBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.WriteFile(candidate, []byte("state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backup := path + ".pre-v2-backup"
	if err := os.WriteFile(backup, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	destroyed, err := Destroy(path)
	if err != nil || !destroyed {
		t.Fatalf("Destroy() = %v, %v", destroyed, err)
	}
	if _, err = os.Stat(backup); err != nil {
		t.Fatalf("backup was not preserved: %v", err)
	}
	destroyed, err = Destroy(path)
	if err != nil || destroyed {
		t.Fatalf("second Destroy() = %v, %v", destroyed, err)
	}
}
