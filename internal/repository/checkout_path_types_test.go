package repository

import (
	"os"
	"path/filepath"
	"testing"
)

func checkoutPathTypeTransition(t *testing.T, directoryToFile bool) (string, CheckoutState, ExtractedCheckout) {
	t.Helper()
	source := checkoutTestRepository(t)
	replace := func(directory bool) {
		t.Helper()
		runCheckoutGit(t, "-C", source, "rm", "-r", "file.txt")
		path := filepath.Join(source, "file.txt")
		if directory {
			path = filepath.Join(path, "nested", "child")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeCheckoutFile(t, path, "replacement\n", 0o644)
		runCheckoutGit(t, "-C", source, "add", ".")
		runCheckoutGit(t, "-C", source, "commit", "-m", "replace path type")
	}
	if directoryToFile {
		replace(true)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	runCheckoutGit(t, "clone", source, destination)
	var err error
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	before, err := ObserveCheckout(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	replace(!directoryToFile)
	capture, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(capture.Release)
	payload, err := ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(payload.Release)
	return destination, before, payload
}

func TestCheckoutPathTypeTransitions(t *testing.T) {
	for _, directoryToFile := range []bool{false, true} {
		name := "file-to-directory"
		if directoryToFile {
			name = "directory-to-file"
		}
		t.Run(name, func(t *testing.T) {
			target, before, payload := checkoutPathTypeTransition(t, directoryToFile)
			// Exercise manifest pages as well as the full application preflight.
			for _, entry := range payload.State.Files {
				if _, err := PreflightCheckoutFiles(t.Context(), target, []CheckoutFile{entry}); err != nil {
					t.Fatal(err)
				}
			}
			applied, err := ApplyCheckoutIfUnchanged(t.Context(), target, payload, before.RevalidationDigest)
			if err != nil {
				t.Fatal(err)
			}
			if applied.Digest != payload.State.Digest {
				t.Fatalf("applied digest = %s, want %s", applied.Digest, payload.State.Digest)
			}
		})
	}
}

func TestCheckoutPathTypeTransitionRollback(t *testing.T) {
	for _, directoryToFile := range []bool{false, true} {
		name := "file-to-directory"
		if directoryToFile {
			name = "directory-to-file"
		}
		for _, interruption := range []string{"after-deletions", "during-addition", "after-files"} {
			t.Run(name+"/"+interruption, func(t *testing.T) {
				target, before, payload := checkoutPathTypeTransition(t, directoryToFile)
				var backup ExtractedCheckout
				if interruption == "after-files" {
					capture, err := CaptureCheckout(t.Context(), target, t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					defer capture.Release()
					backup, err = ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					defer backup.Release()
				}
				root, err := os.OpenRoot(target)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				prepared, err := prepareCheckoutFiles(root, before, payload)
				if err != nil {
					t.Fatal(err)
				}
				defer prepared.Release()
				entries := prepared.entries
				if interruption == "after-deletions" {
					for i, record := range entries {
						if record.incoming != nil {
							prepared.entries = entries[:i]
							break
						}
					}
				}
				if interruption == "during-addition" {
					for _, record := range entries {
						if record.incoming != nil && record.previous == nil {
							if err = root.Remove(record.incomingTemp); err != nil {
								t.Fatal(err)
							}
							break
						}
					}
				}
				err = prepared.Apply()
				prepared.entries = entries
				if (err != nil) != (interruption == "during-addition") {
					t.Fatalf("apply error = %v", err)
				}
				if !prepared.mutated {
					t.Fatal("interruption did not reach mutation")
				}
				if err = prepared.Rollback(); err != nil {
					t.Fatal(err)
				}
				prepared.Release()
				if interruption == "after-files" {
					// The transaction's captured-backup fallback runs even when
					// the inner file rollback has restored the original shape.
					if _, err = RestoreCheckoutAfterFailedApply(t.Context(), target, backup, payload); err != nil {
						t.Fatal(err)
					}
				}
				after, err := ObserveCheckout(t.Context(), target)
				if err != nil {
					t.Fatal(err)
				}
				if after.RevalidationDigest != before.RevalidationDigest {
					t.Fatalf("rollback digest = %s, want %s", after.RevalidationDigest, before.RevalidationDigest)
				}
			})
		}
	}
}

func TestCheckoutPathTypeTransitionPreservesIgnoredCollision(t *testing.T) {
	target, before, payload := checkoutPathTypeTransition(t, true)
	ignored := filepath.Join(target, "file.txt", "nested", "ignored.env")
	writeCheckoutFile(t, ignored, "keep\n", 0o644)
	if err := PreflightCheckoutApplication(t.Context(), target, before, payload.State.Files, payload.State.AbsentPaths); ErrorCode(err) != CodeConflict {
		t.Fatalf("full preflight error = %v", err)
	}
	for _, entry := range payload.State.Files {
		if entry.Path == "file.txt" {
			if _, err := PreflightCheckoutFiles(t.Context(), target, []CheckoutFile{entry}); ErrorCode(err) != CodeConflict {
				t.Fatalf("paged preflight error = %v", err)
			}
		}
	}
	if data, err := os.ReadFile(ignored); err != nil || string(data) != "keep\n" {
		t.Fatalf("ignored content = %q, err = %v", data, err)
	}
}

func TestCheckoutPathTypeTransitionPreservesConcurrentFile(t *testing.T) {
	target, before, payload := checkoutPathTypeTransition(t, true)
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	prepared, err := prepareCheckoutFiles(root, before, payload)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	ignored := filepath.Join(target, "file.txt", "nested", "ignored.env")
	writeCheckoutFile(t, ignored, "concurrent\n", 0o644)
	if err = prepared.Apply(); ErrorCode(err) != CodeConflict {
		t.Fatalf("apply error = %v", err)
	}
	if err = prepared.Rollback(); ErrorCode(err) != CodeConflict {
		t.Fatalf("rollback error = %v", err)
	}
	prepared.Release()
	if data, err := os.ReadFile(ignored); err != nil || string(data) != "concurrent\n" {
		t.Fatalf("concurrent content = %q, err = %v", data, err)
	}
	for _, record := range prepared.entries {
		if record.previous != nil {
			if _, err = root.Lstat(record.backupTemp); err != nil {
				t.Fatalf("rollback material lost: %v", err)
			}
		}
	}
}
