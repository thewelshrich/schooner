package repository

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func checkoutPathTypeTransition(t *testing.T, directoryToFile bool) (string, CheckoutState, ExtractedCheckout) {
	t.Helper()
	return checkoutPathTypeTransitionAt(t, directoryToFile, "file.txt")
}

func checkoutPathTypeTransitionAt(t *testing.T, directoryToFile bool, relative string) (string, CheckoutState, ExtractedCheckout) {
	t.Helper()
	source := checkoutTestRepository(t)
	if relative != "file.txt" {
		if err := os.MkdirAll(filepath.Join(source, filepath.Dir(relative)), 0o755); err != nil {
			t.Fatal(err)
		}
		runCheckoutGit(t, "-C", source, "mv", "file.txt", relative)
		runCheckoutGit(t, "-C", source, "commit", "-m", "nest transition")
	}
	replace := func(directory bool) {
		t.Helper()
		runCheckoutGit(t, "-C", source, "rm", "-r", relative)
		path := filepath.Join(source, relative)
		if directory {
			path = filepath.Join(path, "nested", "child")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestCheckoutTransitionRollbackWithExistingIncomingLeaf(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"file.txt", "outer/file.txt"} {
		for _, directoryToFile := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/directory-to-file=%t", path, directoryToFile), func(t *testing.T) {
				target, before, incoming := checkoutPathTypeTransitionAt(t, directoryToFile, path)
				// An ordinary changed leaf must still replace its matching incoming value
				// during rollback; only newly restored parents use no-replace installation.
				content := []byte("ignored.env\n# incoming\n")
				if err := os.WriteFile(filepath.Join(incoming.Directory, "files", ".gitignore"), content, 0o644); err != nil {
					t.Fatal(err)
				}
				for i := range incoming.State.Files {
					if incoming.State.Files[i].Path == ".gitignore" {
						incoming.State.Files[i].Size = int64(len(content))
						incoming.State.Files[i].SHA256 = fmt.Sprintf("%x", sha256.Sum256(content))
					}
				}
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
				if err = prepared.Apply(); err != nil {
					t.Fatal(err)
				}
				if data, err := os.ReadFile(filepath.Join(target, ".gitignore")); err != nil || string(data) != string(content) {
					t.Fatal("ordinary incoming leaf not installed")
				}
				if err = prepared.Rollback(); err != nil {
					t.Fatal(err)
				}
				prepared.Release()
				after, err := ObserveCheckout(t.Context(), target)
				if err != nil {
					t.Fatal(err)
				}
				if after.RevalidationDigest != before.RevalidationDigest {
					t.Fatal("rollback did not restore the original paths and leaves")
				}
			})
		}
	}
}

func TestCheckoutAbsentDirectoryReplacement(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"file.txt", "outer/file.txt"} {
		for _, rollback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rollback=%t", path, rollback), func(t *testing.T) {
				target, before, incoming := checkoutPathTypeTransitionAt(t, true, path)
				if err := os.Remove(filepath.Join(incoming.State.Worktree, path)); err != nil {
					t.Fatal(err)
				}
				capture, err := CaptureCheckout(t.Context(), incoming.State.Worktree, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer capture.Release()
				absent, err := ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer absent.Release()
				if !slices.Contains(absent.State.AbsentPaths, path) {
					t.Fatal("fixture missing absent leaf")
				}
				if rollback {
					root, err := os.OpenRoot(target)
					if err != nil {
						t.Fatal(err)
					}
					defer root.Close()
					prepared, err := prepareCheckoutFiles(root, before, absent)
					if err != nil {
						t.Fatal(err)
					}
					defer prepared.Release()
					if err = prepared.Apply(); err != nil {
						t.Fatal(err)
					}
					if _, err = root.Lstat(path); !os.IsNotExist(err) {
						t.Fatalf("absent replacement remains: %v", err)
					}
					if err = prepared.Rollback(); err != nil {
						t.Fatal(err)
					}
					prepared.Release()
					restored, err := ObserveCheckout(t.Context(), target)
					if err != nil {
						t.Fatal(err)
					}
					if restored.Digest != before.Digest {
						t.Fatal("absent transition rollback changed checkout")
					}
				} else {
					applied, err := ApplyCheckoutIfUnchanged(t.Context(), target, absent, before.RevalidationDigest)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = os.Lstat(filepath.Join(target, path)); !os.IsNotExist(err) {
						t.Fatalf("absent replacement remains: %v", err)
					}
					restored, err := ApplyCheckoutIfUnchanged(t.Context(), target, incoming, applied.RevalidationDigest)
					if err != nil {
						t.Fatal(err)
					}
					if restored.Digest != incoming.State.Digest {
						t.Fatal("subsequent transfer did not restore leaf")
					}
				}
			})
		}
	}
}

func TestCheckoutExternalRestoreAbsentReplacement(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("incoming-leaves-gone=%t", empty), func(t *testing.T) {
			target, _, incoming := checkoutPathTypeTransition(t, false)
			if err := os.Remove(filepath.Join(target, "file.txt")); err != nil {
				t.Fatal(err)
			}
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
			if _, err = ApplyCheckoutIfUnchanged(t.Context(), target, incoming, backup.State.RevalidationDigest); err != nil {
				t.Fatal(err)
			}
			if empty {
				if err = os.Remove(filepath.Join(target, "file.txt/nested/child")); err != nil {
					t.Fatal(err)
				}
			}
			restored, err := RestoreCheckoutAfterFailedApply(t.Context(), target, backup, incoming)
			if err != nil {
				t.Fatal(err)
			}
			if restored.Digest != backup.State.Digest {
				t.Fatal("external restore changed absent checkout")
			}
			if _, err = os.Lstat(filepath.Join(target, "file.txt")); !os.IsNotExist(err) {
				t.Fatalf("external restore left directory: %v", err)
			}
		})
	}
}

func TestCheckoutUntrackedChildReplacesTrackedSymlink(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"tracked", "local-ignore", "untracked-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			source := checkoutTestRepository(t)
			external := t.TempDir()
			writeCheckoutFile(t, filepath.Join(external, ".gitignore"), "*\n", 0o600)
			runCheckoutGit(t, "-C", source, "rm", "file.txt")
			if err := os.Mkdir(filepath.Join(source, "parent"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, filepath.Join(source, "parent/link")); err != nil {
				t.Fatal(err)
			}
			runCheckoutGit(t, "-C", source, "add", ".")
			runCheckoutGit(t, "-C", source, "commit", "-m", "track symlink")
			target := filepath.Join(t.TempDir(), "target")
			runCheckoutGit(t, "clone", source, target)
			target, err := filepath.EvalSymlinks(target)
			if err != nil {
				t.Fatal(err)
			}
			runCheckoutGit(t, "-C", source, "rm", "parent/link")
			if err = os.MkdirAll(filepath.Join(source, "parent/link/nested"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeCheckoutFile(t, filepath.Join(source, "parent/link/nested/child"), "incoming\n", 0o755)
			if scenario == "local-ignore" {
				writeCheckoutFile(t, filepath.Join(target, "parent/.gitignore"), "link/\n", 0o600)
			}
			if scenario == "untracked-symlink" {
				runCheckoutGit(t, "-C", target, "rm", "--cached", "parent/link")
			}
			capture, err := CaptureCheckout(t.Context(), source, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer capture.Release()
			payload, err := ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer payload.Release()
			before, err := ObserveCheckout(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			child := checkoutFileMap(payload.State.Files)["parent/link/nested/child"]
			_, pageErr := PreflightCheckoutFiles(t.Context(), target, []CheckoutFile{child})
			applied, applyErr := ApplyCheckoutIfUnchanged(t.Context(), target, payload, before.RevalidationDigest)
			if scenario == "tracked" {
				if pageErr != nil || applyErr != nil {
					t.Fatalf("valid replacement rejected: page=%v apply=%v", pageErr, applyErr)
				}
				if applied.Digest != payload.State.Digest {
					t.Fatal("applied checkout differs")
				}
			} else {
				if pageErr == nil || applyErr == nil {
					t.Fatalf("unsafe replacement accepted: page=%v apply=%v", pageErr, applyErr)
				}
				after, err := ObserveCheckout(t.Context(), target)
				if err != nil {
					t.Fatal(err)
				}
				if after.RevalidationDigest != before.RevalidationDigest {
					t.Fatal("rejected transfer mutated destination")
				}
			}
			contents, err := os.ReadFile(filepath.Join(external, ".gitignore"))
			if err != nil || string(contents) != "*\n" {
				t.Fatal("external ignore changed")
			}
			if _, err = os.Lstat(filepath.Join(external, "nested")); !os.IsNotExist(err) {
				t.Fatal("external symlink target modified")
			}
		})
	}
}

func TestCheckoutTransitionWithAbsentTrackedDescendants(t *testing.T) {
	t.Parallel()
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%t", rollback), func(t *testing.T) {
			target, _, incoming := checkoutPathTypeTransition(t, true)
			if err := os.Remove(filepath.Join(target, "file.txt/nested/child")); err != nil {
				t.Fatal(err)
			}
			before, err := ObserveCheckout(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(before.AbsentPaths, "file.txt/nested/child") {
				t.Fatal("missing absent descendant")
			}
			if _, err = PreflightCheckoutFiles(t.Context(), target, incoming.State.Files); err != nil {
				t.Fatal(err)
			}
			if err = PreflightCheckoutApplication(t.Context(), target, before, incoming.State.Files, incoming.State.AbsentPaths); err != nil {
				t.Fatal(err)
			}
			if rollback {
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
				if err = prepared.Apply(); err != nil {
					t.Fatal(err)
				}
				if err = prepared.Rollback(); err != nil {
					t.Fatal(err)
				}
				prepared.Release()
				for _, path := range []string{"file.txt", "file.txt/nested"} {
					info, err := root.Lstat(path)
					if err != nil || !info.IsDir() {
						t.Fatalf("original empty directory missing: %s: %v", path, err)
					}
				}
				restored, err := ObserveCheckout(t.Context(), target)
				if err != nil {
					t.Fatal(err)
				}
				if restored.Digest != before.Digest {
					t.Fatal("absent tracked state not restored")
				}
			} else {
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
				applied, err := ApplyCheckoutIfUnchanged(t.Context(), target, incoming, before.RevalidationDigest)
				if err != nil {
					t.Fatal(err)
				}
				if applied.Digest != incoming.State.Digest {
					t.Fatal("incoming file not applied")
				}
				// Persistent captures cannot recreate the original empty directory metadata.
				if _, err = RestoreCheckoutAfterFailedApply(t.Context(), target, backup, incoming); ErrorCode(err) != CodeOutcomeUnknown {
					t.Fatalf("external restore = %v", err)
				}
				after, err := ObserveCheckout(t.Context(), target)
				if err != nil {
					t.Fatal(err)
				}
				if after.Digest != applied.Digest {
					t.Fatal("failed external restore mutated checkout")
				}
			}
		})
	}
}

func TestCheckoutExternalRecoveryPreservesChangedLeaf(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"file", "symlink", "unchanged"} {
		t.Run(kind, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err = root.Mkdir("incoming", 0o755); err != nil {
				t.Fatal(err)
			}
			writeCheckoutFile(t, filepath.Join(root.Name(), "incoming/child"), "incoming/child", 0o600)
			expected, _, err := checkoutFileOnRoot(root, "incoming/child")
			if err != nil {
				t.Fatal(err)
			}
			// Replace the leaf after the external-restore validation, before removal.
			if kind != "unchanged" {
				if err = root.Remove("incoming/child"); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					err = root.Symlink("private-target", "incoming/child")
				} else {
					err = os.WriteFile(filepath.Join(root.Name(), "incoming/child"), []byte("concurrent edit"), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			info, err := root.Lstat("incoming/child")
			if err != nil {
				t.Fatal(err)
			}
			changed, _, err := checkoutFileOnRoot(root, "incoming/child")
			if err != nil {
				t.Fatal(err)
			}
			prepared := preparedCheckoutFiles{root: root}
			err = prepared.removeRecoveryFile("incoming/child", expected)
			if kind == "unchanged" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = root.Lstat("incoming/child"); !os.IsNotExist(err) {
					t.Fatal("expected incoming leaf remains")
				}
			} else {
				if ErrorCode(err) != CodeConflict {
					t.Fatalf("recovery deletion = %v", err)
				}
				after, err := root.Lstat("incoming/child")
				if err != nil || !os.SameFile(info, after) {
					t.Fatal("concurrent identity lost")
				}
				current, present, err := checkoutFileOnRoot(root, "incoming/child")
				if err != nil || !present || !checkoutFileContentEqual(current, changed) {
					t.Fatal("concurrent content lost")
				}
			}
		})
	}
}

func TestCheckoutIgnorePageExcludesUnrelatedRules(t *testing.T) {
	t.Parallel()
	target := checkoutTestRepository(t)
	for _, dir := range []string{"src/one", "src/two", "other"} {
		if err := os.MkdirAll(filepath.Join(target, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		writeCheckoutFile(t, filepath.Join(target, dir, ".gitignore"), "ignored\n", 0o600)
	}
	writeCheckoutFile(t, filepath.Join(target, "src/.gitignore"), "ignored\n", 0o600)
	writeCheckoutFile(t, filepath.Join(target, "src/blocker"), "outgoing", 0o600)
	runCheckoutGit(t, "-C", target, "add", ".")
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, leaf := range []string{"src/one/new", "src/blocker/new"} {
		paths, _, err := checkoutIgnorePaths(root, []CheckoutFile{{Path: leaf, Kind: "file"}})
		if err != nil {
			t.Fatal(err)
		}
		args := append([]string{"-C", target, "--literal-pathspecs", "ls-files", "-z", "--cached", "--"}, paths...)
		output := parseNULPaths(runCheckoutGit(t, args...))
		for _, unrelated := range []string{"src/two/.gitignore", "other/.gitignore"} {
			if slices.Contains(output, unrelated) {
				t.Fatalf("page enumerated unrelated rule %s", unrelated)
			}
		}
		for _, required := range []string{".gitignore", "src/.gitignore"} {
			if !slices.Contains(output, required) {
				t.Fatalf("page omitted ancestor rule %s", required)
			}
		}
		if leaf == "src/blocker/new" && !slices.Contains(output, "src/blocker") {
			t.Fatal("page omitted non-directory blocker")
		}
	}
}

func TestCheckoutRollbackAbsentMarkerPreservesUnexpectedLeaf(t *testing.T) {
	t.Parallel()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	writeCheckoutFile(t, filepath.Join(root.Name(), "absent"), "independent", 0o600)
	prepared := preparedCheckoutFiles{root: root, mutated: true, entries: []preparedCheckoutFile{{path: "absent", absent: true}}}
	if err = prepared.Rollback(); ErrorCode(err) != CodeConflict {
		t.Fatalf("absent marker rollback = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(root.Name(), "absent"))
	if err != nil || string(contents) != "independent" {
		t.Fatal("absent marker deleted independent leaf")
	}
}
