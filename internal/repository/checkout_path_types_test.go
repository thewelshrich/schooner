package repository

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestCheckoutPathTypeExternalRestore(t *testing.T) {
	for _, directoryToFile := range []bool{false, true} {
		name := "file-to-directory"
		if directoryToFile {
			name = "directory-to-file"
		}
		t.Run(name, func(t *testing.T) {
			target, before, incoming := checkoutPathTypeTransition(t, directoryToFile)
			capture, err := CaptureCheckout(t.Context(), target, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer capture.Release()
			backup, err := ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer backup.Release()
			if _, err = ApplyCheckoutIfUnchanged(t.Context(), target, incoming, before.RevalidationDigest); err != nil {
				t.Fatal(err)
			}
			applied, err := ObserveCheckout(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := RestoreCheckoutAfterFailedApply(t.Context(), target, backup, incoming)
			if directoryToFile {
				if ErrorCode(err) != CodeOutcomeUnknown || !strings.Contains(err.Error(), "directory metadata was not captured") || !strings.Contains(err.Error(), "manual recovery") {
					t.Fatalf("missing metadata error = %v", err)
				}
				if _, err = os.Stat(filepath.Join(backup.Directory, "files", "file.txt", "nested", "child")); err != nil {
					t.Fatalf("extracted backup lost: %v", err)
				}
				after, err := ObserveCheckout(t.Context(), target)
				if err != nil {
					t.Fatal(err)
				}
				if after.RevalidationDigest != applied.RevalidationDigest {
					t.Fatal("refused restore mutated the destination")
				}
				recoveryStaging := t.TempDir()
				_, err = restoreCheckoutTransaction(t.Context(), target, capture, incoming, recoveryStaging, &Error{Code: CodeConflict, Message: "interrupted application"})
				if entries, readErr := os.ReadDir(recoveryStaging); readErr != nil || len(entries) != 0 {
					t.Fatalf("failed recovery leaked extraction: %v, %v", entries, readErr)
				}
				if ErrorCode(err) != CodeOutcomeUnknown || !strings.Contains(err.Error(), capture.PayloadPath) {
					t.Fatalf("retained capture guidance = %v", err)
				}
				retained, err := ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
				if err != nil {
					t.Fatalf("captured backup was not retained: %v", err)
				}
				defer retained.Release()
				if retained.State.Digest != before.Digest {
					t.Fatal("retained capture is not the original backup")
				}
				after, err = ObserveCheckout(t.Context(), target)
				if err != nil {
					t.Fatal(err)
				}
				if after.RevalidationDigest != applied.RevalidationDigest {
					t.Fatal("failed transaction recovery mutated the destination")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if restored.RevalidationDigest != before.RevalidationDigest {
				t.Fatalf("restored digest = %s, want %s", restored.RevalidationDigest, before.RevalidationDigest)
			}
		})
	}
}

func TestCheckoutRestoreRewindProtectsIndependentCommit(t *testing.T) {
	target := checkoutTestRepository(t)
	before, err := ObserveCheckout(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, filepath.Join(target, "file.txt"), "independent commit\n", 0o644)
	runCheckoutGit(t, "-C", target, "commit", "-am", "independent")
	independent, err := ObserveCheckout(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = updateCheckoutHEADWithBranchRewind(t.Context(), target, before, before.HEAD); ErrorCode(err) != CodeConflict {
		t.Fatalf("stale rewind error = %v", err)
	}
	after, err := ObserveCheckout(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if after.RevalidationDigest != independent.RevalidationDigest {
		t.Fatal("rewind overwrote independent commit")
	}
}

func TestCheckoutTransitionStagingPreservesAdjacentFilesystem(t *testing.T) {
	destination := map[string]CheckoutFile{"mount/tree/old/leaf": {}, "mount/file": {}, "mount/stable/deep/leaf": {}}
	incoming := map[string]CheckoutFile{"mount/tree": {}, "mount/file/new/leaf": {}, "mount/stable/deep/leaf": {}}
	for _, test := range []struct{ path, parent string }{
		{"mount/stable/deep/leaf", "mount/stable/deep"},
		{"mount/tree/old/leaf", "mount"},
		{"mount/tree", "mount"},
		{"mount/file", "mount"},
		{"mount/file/new/leaf", "mount"},
	} {
		t.Run(test.path, func(t *testing.T) {
			path, err := checkoutTemporaryPath(checkoutStagingBase(test.path, destination, incoming))
			if err != nil {
				t.Fatal(err)
			}
			if parent := filepath.ToSlash(filepath.Dir(path)); parent != test.parent {
				t.Fatalf("temporary parent = %q, want %q", parent, test.parent)
			}
		})
	}
}

func TestCheckoutExternalRestoreEmptyTransitionDirectories(t *testing.T) {
	target, before, incoming := checkoutPathTypeTransition(t, false)
	capture, err := CaptureCheckout(t.Context(), target, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Release()
	backup, err := ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Release()
	if _, err = ApplyCheckoutIfUnchanged(t.Context(), target, incoming, before.RevalidationDigest); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(target, "file.txt", "nested", "child")); err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreCheckoutAfterFailedApply(t.Context(), target, backup, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if restored.RevalidationDigest != before.RevalidationDigest {
		t.Fatal("empty-directory recovery did not restore the backup")
	}
}

func TestCheckoutPreflightPagesExcludeUnrelatedSiblings(t *testing.T) {
	target := checkoutTestRepository(t)
	for _, path := range []string{"src/part/selected", "src/part/sibling", "src/other/file", "src/blocker", "src/tree/nested/child", "src/[literal]", "src/l"} {
		full := filepath.Join(target, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		writeCheckoutFile(t, full, "tracked\n", 0o644)
	}
	runCheckoutGit(t, "-C", target, "add", "src")
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, test := range []struct {
		path string
		want []string
	}{
		{"src/part/selected", []string{"src/part/selected"}},
		{"src/blocker/child", []string{"src/blocker"}},
		{"src/tree", []string{"src/tree/nested/child"}},
		{"src/[literal]", []string{"src/[literal]"}},
		{"src/new/child", nil},
	} {
		t.Run(test.path, func(t *testing.T) {
			page := []CheckoutFile{{Path: test.path, Kind: "file", Tracked: true}}
			paths, err := checkoutPreflightPaths(root, page)
			if err != nil {
				t.Fatal(err)
			}
			args := append([]string{"-C", target, "--literal-pathspecs", "ls-files", "-z", "--cached", "--"}, paths...)
			if got := parseNULPaths(runCheckoutGit(t, args...)); !slices.Equal(got, test.want) {
				t.Fatalf("page index paths = %q, want %q", got, test.want)
			}
			if _, err = PreflightCheckoutFiles(t.Context(), target, page); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckoutPreflightManySiblingTransitions(t *testing.T) {
	target := checkoutTestRepository(t)
	var incoming []CheckoutFile
	for i := 0; i < 128; i++ {
		dir := fmt.Sprintf("sibling-%03d", i)
		if err := os.Mkdir(filepath.Join(target, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		writeCheckoutFile(t, filepath.Join(target, dir, "child"), "tracked\n", 0o644)
		incoming = append(incoming, CheckoutFile{Path: dir, Kind: "file", Tracked: true})
	}
	runCheckoutGit(t, "-C", target, "add", ".")
	destination, err := ObserveCheckout(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err = PreflightCheckoutApplication(t.Context(), target, destination, incoming, nil); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, filepath.Join(target, "sibling-127", "ignored.env"), "preserve\n", 0o644)
	if err = PreflightCheckoutApplication(t.Context(), target, destination, incoming, nil); ErrorCode(err) != CodeConflict {
		t.Fatalf("bulk transition collision error = %v", err)
	}
}

func TestCheckoutTransactionRecoveryCaptureLifecycle(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain=%t", uncertain), func(t *testing.T) {
			target := checkoutTestRepository(t)
			staging := t.TempDir()
			capture, err := CaptureCheckout(t.Context(), target, staging)
			if err != nil {
				t.Fatal(err)
			}
			defer capture.Release()
			incoming, err := ExtractCheckoutPayload(capture.PayloadPath, staging)
			if err != nil {
				t.Fatal(err)
			}
			defer incoming.Release()
			code := CodeConflict
			if uncertain {
				code = CodeOutcomeUnknown
			}
			_, err = restoreCheckoutTransaction(t.Context(), target, capture, incoming, staging, &Error{Code: code, Message: "application failed"})
			if ErrorCode(err) != code {
				t.Fatalf("recovery error = %v, want %s", err, code)
			}
			_, statErr := os.Stat(capture.PayloadPath)
			if uncertain {
				if statErr != nil || !strings.Contains(err.Error(), capture.PayloadPath) {
					t.Fatalf("uncertain recovery lost backup or guidance: %v, %v", err, statErr)
				}
			} else if !os.IsNotExist(statErr) {
				t.Fatalf("completed rollback did not release capture: %v", statErr)
			}
		})
	}
}

func TestCheckoutUnknownRecoveryDoesNotInstallBackup(t *testing.T) {
	target, _, incoming := checkoutPathTypeTransition(t, true)
	staging := t.TempDir()
	capture, err := CaptureCheckout(t.Context(), target, staging)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Release()
	// Model an inner rollback that recreated the chain but failed metadata
	// restoration before reinstalling the private original leaf.
	leaf := filepath.Join(target, "file.txt", "nested", "child")
	if err = os.Remove(leaf); err != nil {
		t.Fatal(err)
	}
	recoveryStaging := t.TempDir()
	_, err = restoreCheckoutTransaction(t.Context(), target, capture, incoming, recoveryStaging, &Error{Code: CodeOutcomeUnknown, Message: "directory ownership restoration failed"})
	if ErrorCode(err) != CodeOutcomeUnknown || !strings.Contains(err.Error(), capture.PayloadPath) {
		t.Fatalf("recovery error = %v", err)
	}
	if _, err = os.Stat(leaf); !os.IsNotExist(err) {
		t.Fatalf("unknown recovery installed backup leaf: %v", err)
	}
	if _, err = os.Stat(capture.PayloadPath); err != nil {
		t.Fatalf("backup lost: %v", err)
	}
	if entries, err := os.ReadDir(recoveryStaging); err != nil || len(entries) != 0 {
		t.Fatalf("unknown recovery extracted payload: %v, %v", entries, err)
	}
}
