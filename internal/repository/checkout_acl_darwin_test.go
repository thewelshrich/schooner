package repository

import (
	"os"
	"os/exec"
	"testing"
)

func installCheckoutTestACL(t *testing.T, path string) {
	t.Helper()
	output, err := exec.Command("/bin/chmod", "+a", "everyone allow readattr", path).CombinedOutput()
	if err != nil {
		t.Fatalf("set directory ACL: %v: %s", err, output)
	}
}

func TestCheckoutProvenanceMismatchPreservesBackup(t *testing.T) {
	for _, phase := range []string{"before-removal", "rollback"} {
		t.Run(phase, func(t *testing.T) {
			target, before, incoming := checkoutPathTypeTransition(t, true)
			root, err := os.OpenRoot(target)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			prepared, err := prepareCheckoutFiles(root, before, incoming)
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Release()
			if phase == "rollback" {
				if err = prepared.Apply(); err != nil {
					t.Fatal(err)
				}
			}
			path := "file.txt"
			if phase == "before-removal" {
				path = "file.txt/nested"
			}
			original := prepared.removedDirs[path]
			changed := original
			// Corrupt only the in-memory expectation; never write OS security metadata.
			changed.provenance = append(append([]byte(nil), original.provenance...), 1)
			prepared.removedDirs[path] = changed
			if phase == "before-removal" {
				err = prepared.removeReplacedDirectories("file.txt")
				if ErrorCode(err) != CodeOutcomeUnknown {
					t.Fatalf("provenance refusal = %v", err)
				}
				if prepared.mutated {
					t.Fatal("provenance mismatch mutated destination")
				}
				return
			}
			if err = prepared.Rollback(); ErrorCode(err) != CodeOutcomeUnknown {
				t.Fatalf("provenance recovery error = %v", err)
			}
			prepared.Release()
			for _, record := range prepared.entries {
				if record.previous != nil {
					if _, err = root.Lstat(record.backupTemp); err != nil {
						t.Fatalf("recovery backup lost: %v", err)
					}
				}
			}
			prepared.removedDirs[path] = original
			if err = prepared.Rollback(); err != nil {
				t.Fatalf("retry with original expectation: %v", err)
			}
		})
	}
}
