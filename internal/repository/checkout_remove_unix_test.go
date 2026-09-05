//go:build linux || darwin

package repository

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCheckoutDirectoryQuarantinePreservesReplacement(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "directory", "new-child"} {
		t.Run(kind, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err = root.Mkdir("directory", 0o755); err != nil {
				t.Fatal(err)
			}
			info, err := root.Lstat("directory")
			if err != nil {
				t.Fatal(err)
			}
			expected, err := checkCheckoutDirectoryPermissions(root, "directory", info)
			if err != nil {
				t.Fatal(err)
			}
			// Model a source swap after metadata verification and before displacement.
			if kind != "new-child" {
				if err = root.Rename("directory", "original"); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "file":
				writeCheckoutFile(t, filepath.Join(root.Name(), "directory"), "concurrent", 0o644)
			case "symlink":
				if err = root.Symlink("kept-target", "directory"); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err = root.Mkdir("directory", 0o755); err != nil {
					t.Fatal(err)
				}
			case "new-child":
				writeCheckoutFile(t, filepath.Join(root.Name(), "directory", "child"), "concurrent", 0o644)
			}
			concurrent, err := root.Lstat("directory")
			if err != nil {
				t.Fatal(err)
			}
			mutated, err := removeCheckoutDirectory(root, "directory", expected, true)
			if !mutated || ErrorCode(err) != CodeConflict {
				t.Fatalf("removal = %t, %v", mutated, err)
			}
			after, err := root.Lstat("directory")
			if err != nil || !os.SameFile(after, concurrent) {
				t.Fatalf("concurrent entry lost: %v", err)
			}
			if kind == "file" || kind == "new-child" {
				path := filepath.Join(root.Name(), "directory")
				if kind == "new-child" {
					path = filepath.Join(path, "child")
				}
				if data, err := os.ReadFile(path); err != nil || string(data) != "concurrent" {
					t.Fatal("concurrent contents lost")
				}
			}
			entries, err := os.ReadDir(root.Name())
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".schooner-file-") {
					t.Fatal("restored displacement leaked quarantine")
				}
			}
		})
	}
}

func TestCheckoutDirectoryQuarantineRetainsCollision(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.Mkdir("original", 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := root.Lstat("original")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := checkCheckoutDirectoryPermissions(root, "original", info)
	if err != nil {
		t.Fatal(err)
	}
	if err = root.Mkdir("quarantine", 0o700); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, filepath.Join(root.Name(), "quarantine", "entry"), "displaced concurrent", 0o644)
	writeCheckoutFile(t, filepath.Join(root.Name(), "destination"), "new concurrent", 0o644)
	parent, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	quarantine, err := root.Open("quarantine")
	if err != nil {
		t.Fatal(err)
	}
	defer quarantine.Close()
	recovery := filepath.Join(root.Name(), "quarantine", "entry")
	empty, err := removeQuarantinedCheckoutDirectory(parent, quarantine, "destination", recovery, expected, true)
	if empty || ErrorCode(err) != CodeOutcomeUnknown || !strings.Contains(err.Error(), recovery) {
		t.Fatalf("collision recovery = %t, %v", empty, err)
	}
	for path, want := range map[string]string{"quarantine/entry": "displaced concurrent", "destination": "new concurrent"} {
		if data, err := os.ReadFile(filepath.Join(root.Name(), path)); err != nil || string(data) != want {
			t.Fatalf("lost %s", path)
		}
	}
}

func TestCheckoutDirectoryQuarantineEarlyFailureCleansUp(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.Mkdir("directory", 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := root.Lstat("directory")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := checkCheckoutDirectoryPermissions(root, "directory", info)
	if err != nil {
		t.Fatal(err)
	}
	// The quarantine cannot be opened by an unprivileged process; root instead
	// reaches the privacy-mode refusal. Both paths must clean the owned container.
	var mutated bool
	func() {
		old := unix.Umask(0o777)
		defer unix.Umask(old)
		mutated, err = removeCheckoutDirectory(root, "directory", expected, true)
	}()
	if err == nil || mutated {
		t.Fatalf("early failure = %t, %v", mutated, err)
	}
	entries, err := os.ReadDir(root.Name())
	if err != nil || len(entries) != 1 || entries[0].Name() != "directory" {
		t.Fatalf("quarantine leaked after early failure: %v, %v", entries, err)
	}
}

func TestCheckoutReleasePreservesCreatedDirectoryReplacement(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.Mkdir("created", 0o755); err != nil {
		t.Fatal(err)
	}
	prepared := preparedCheckoutFiles{root: root, createdDirs: []string{"created"}}
	if err = prepared.pinCreatedDirectories(prepared.createdDirs); err != nil {
		t.Fatal(err)
	}
	if err = root.Rename("created", "original"); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, filepath.Join(root.Name(), "created"), "concurrent", 0o644)
	prepared.Release()
	if data, err := os.ReadFile(filepath.Join(root.Name(), "created")); err != nil || string(data) != "concurrent" {
		t.Fatal("cleanup removed concurrent replacement")
	}
}

func TestCheckoutCreatedDirectoryCleanupDoesNotRequireMetadataSupport(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.Mkdir("created", 0o755); err != nil {
		t.Fatal(err)
	}
	installCheckoutTestACL(t, filepath.Join(root.Name(), "created"))
	prepared := preparedCheckoutFiles{root: root, createdDirs: []string{"created"}}
	if err = prepared.pinCreatedDirectories(prepared.createdDirs); err != nil {
		t.Fatalf("ordinary created directory rejected: %v", err)
	}
	prepared.Release()
	if _, err = root.Lstat("created"); !os.IsNotExist(err) {
		t.Fatalf("safe empty-directory cleanup = %v", err)
	}
}

func TestCheckoutDirectoryRemovalRejectsIntermediateSymlink(t *testing.T) {
	for _, ancestor := range []string{"outer", "outer/inner"} {
		t.Run(ancestor, func(t *testing.T) {
			target := t.TempDir()
			if err := os.MkdirAll(filepath.Join(target, "outer/inner/deep/leaf"), 0o700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(target)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			info, err := root.Lstat("outer/inner/deep/leaf")
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := checkCheckoutDirectoryPermissions(root, "outer/inner/deep/leaf", info)
			if err != nil {
				t.Fatal(err)
			}
			moved := ancestor + "-moved"
			if err = root.Rename(ancestor, moved); err != nil {
				t.Fatal(err)
			}
			if err = root.Symlink(filepath.Base(moved), ancestor); err != nil {
				t.Fatal(err)
			}
			changed, err := removeCheckoutDirectory(root, "outer/inner/deep/leaf", metadata, true)
			if err == nil || changed {
				t.Fatalf("symlink ancestor removal: changed=%t err=%v", changed, err)
			}
			remaining, err := root.Lstat(filepath.Join(moved, strings.TrimPrefix("outer/inner/deep/leaf", ancestor+"/")))
			if err != nil || !os.SameFile(remaining, info) {
				t.Fatal("moved directory mutated")
			}
		})
	}
}
