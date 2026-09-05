//go:build linux || darwin

package repository

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCheckoutTransitionRollbackPreservesDirectoryMode(t *testing.T) {
	target, before, incoming := checkoutPathTypeTransition(t, true)
	mode := os.FileMode(0o755) | os.ModeSetgid | os.ModeSticky
	// macOS inherits a temporary directory's group; use our own group so
	// chmod can retain setgid both before replacement and after recreation.
	if err := os.Chown(target, -1, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"file.txt", "file.txt/nested"} {
		if err := os.Chown(filepath.Join(target, filepath.FromSlash(path)), -1, os.Getgid()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(target, filepath.FromSlash(path)), mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(filepath.Join(target, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky); got != mode {
			t.Fatalf("initial directory mode = %v, want %v", got, mode)
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
	if err = prepared.Rollback(); err != nil {
		t.Fatal(err)
	}
	prepared.Release()
	for _, path := range []string{"file.txt", "file.txt/nested"} {
		info, err := root.Lstat(filepath.FromSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky); got != mode {
			t.Fatalf("restored directory %q mode = %v, want %v", path, got, mode)
		}
	}
}

func TestCheckoutDirectoryMetadataRestoresSupplementaryGroup(t *testing.T) {
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	group := -1
	for _, candidate := range groups {
		if candidate != os.Getgid() {
			group = candidate
			break
		}
	}
	if group == -1 {
		t.Skip("requires a supplementary group different from the default group")
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.Chown(".", os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if err = root.Mkdir("shared", 0o755); err != nil {
		t.Fatal(err)
	}
	if err = root.Chown("shared", os.Getuid(), group); err != nil {
		t.Fatal(err)
	}
	if err = root.Chmod("shared", 0o755|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	originalInfo, err := root.Lstat("shared")
	if err != nil {
		t.Fatal(err)
	}
	original, err := checkCheckoutDirectoryPermissions(root, "shared", originalInfo)
	if err != nil {
		t.Fatal(err)
	}
	if original.info.Mode()&os.ModeSetgid == 0 {
		t.Fatal("fixture did not retain setgid")
	}
	if err = root.Remove("shared"); err != nil {
		t.Fatal(err)
	}
	if err = root.Mkdir("shared", 0o755); err != nil {
		t.Fatal(err)
	}
	recreated, err := root.Lstat("shared")
	if err != nil {
		t.Fatal(err)
	}
	if recreated.Sys().(*syscall.Stat_t).Gid == uint32(group) {
		t.Fatal("fixture did not change group")
	}
	restoredDirectory, err := restoreCheckoutDirectoryMetadata(root, "shared", recreated, original)
	if err != nil {
		t.Fatal(err)
	}
	restoredDirectory.Close()
	restored, err := root.Lstat("shared")
	if err != nil {
		t.Fatal(err)
	}
	got, want := restored.Sys().(*syscall.Stat_t), original.info.Sys().(*syscall.Stat_t)
	if got.Uid != want.Uid || got.Gid != want.Gid || restored.Mode() != original.info.Mode() {
		t.Fatalf("restored ownership/mode = %d:%d %v, want %d:%d %v", got.Uid, got.Gid, restored.Mode(), want.Uid, want.Gid, original.info.Mode())
	}
}

type checkoutOwnershipInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (info checkoutOwnershipInfo) Sys() any { return &info.stat }

func TestCheckoutOwnershipFailurePreservesBackupAndRetries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged process to reject chown")
	}
	for _, absent := range []bool{false, true} {
		t.Run(fmt.Sprintf("absent=%t", absent), func(t *testing.T) {
			target, before, incoming := checkoutPathTypeTransition(t, true)
			if absent {
				if err := os.Remove(filepath.Join(target, "file.txt/nested/child")); err != nil {
					t.Fatal(err)
				}
				var err error
				before, err = ObserveCheckout(t.Context(), target)
				if err != nil {
					t.Fatal(err)
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
			original := prepared.removedDirs["file.txt"]
			stat := *original.info.Sys().(*syscall.Stat_t)
			stat.Uid = uint32(os.Geteuid() + 1)
			modified := original
			modified.info = checkoutOwnershipInfo{FileInfo: original.info, stat: stat}
			prepared.removedDirs["file.txt"] = modified
			if err = prepared.Rollback(); ErrorCode(err) != CodeOutcomeUnknown {
				t.Fatalf("unrestorable ownership error = %v", err)
			}
			prepared.Release()
			for _, record := range prepared.entries {
				if record.previous != nil {
					if _, err = root.Lstat(record.backupTemp); err != nil {
						t.Fatalf("lost recovery material: %v", err)
					}
				}
			}
			if err = prepared.Rollback(); ErrorCode(err) != CodeOutcomeUnknown {
				t.Fatalf("retry accepted unrestored ownership: %v", err)
			}
			prepared.removedDirs["file.txt"] = original
			if err = prepared.Rollback(); err != nil {
				t.Fatal(err)
			}
			prepared.Release()
			if absent {
				for _, path := range []string{"file.txt", "file.txt/nested"} {
					info, err := root.Lstat(path)
					if err != nil || !info.IsDir() {
						t.Fatalf("empty directory not restored: %s: %v", path, err)
					}
				}
			}
			after, err := ObserveCheckout(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			if after.RevalidationDigest != before.RevalidationDigest {
				t.Fatal("retry did not fully restore the checkout")
			}
		})
	}
}

func TestCheckoutTransitionRejectsDirectoryACL(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-preparation=%t", late), func(t *testing.T) {
			target, before, incoming := checkoutPathTypeTransition(t, true)
			root, err := os.OpenRoot(target)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			var prepared *preparedCheckoutFiles
			if late {
				prepared, err = prepareCheckoutFiles(root, before, incoming)
				if err != nil {
					t.Fatal(err)
				}
				defer prepared.Release()
			}
			installCheckoutTestACL(t, filepath.Join(target, "file.txt", "nested"))
			if !late {
				assertCheckoutMetadataPreflightConflict(t, target, before, incoming)
			}
			if late {
				err = prepared.removeReplacedDirectories("file.txt")
			} else {
				prepared, err = prepareCheckoutFiles(root, before, incoming)
				if prepared != nil {
					defer prepared.Release()
				}
			}
			if ErrorCode(err) != CodeConflict || !strings.Contains(err.Error(), "extended permissions") {
				t.Fatalf("ACL rejection = %v", err)
			}
			if _, err = os.Stat(filepath.Join(target, "file.txt", "nested", "child")); err != nil {
				t.Fatalf("original leaf lost: %v", err)
			}
		})
	}
}

func TestCheckoutACLInspectionFailure(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory.Close()
	if err = rejectCheckoutDirectoryACL(directory); err == nil {
		t.Fatal("closed descriptor accepted")
	}
}

func TestCheckoutTransitionRejectsDirectoryXattr(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-preparation=%t", late), func(t *testing.T) {
			target, before, incoming := checkoutPathTypeTransition(t, true)
			root, err := os.OpenRoot(target)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			var prepared *preparedCheckoutFiles
			if late {
				prepared, err = prepareCheckoutFiles(root, before, incoming)
				if err != nil {
					t.Fatal(err)
				}
				defer prepared.Release()
			}
			directory, err := root.Open("file.txt/nested")
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			const name = "user.schooner-test"
			if err = unix.Fsetxattr(int(directory.Fd()), name, []byte("keep"), 0); err != nil {
				t.Fatal(err)
			}
			if !late {
				assertCheckoutMetadataPreflightConflict(t, target, before, incoming)
			}
			if late {
				err = prepared.Apply()
			} else {
				prepared, err = prepareCheckoutFiles(root, before, incoming)
				if prepared != nil {
					defer prepared.Release()
				}
			}
			if ErrorCode(err) != CodeConflict {
				t.Fatalf("xattr rejection = %v", err)
			}
			if late {
				if err = prepared.Rollback(); err != nil {
					t.Fatal(err)
				}
				prepared.Release()
			}
			after, err := ObserveCheckout(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			if after.RevalidationDigest != before.RevalidationDigest {
				t.Fatal("original checkout not preserved")
			}
			var value [4]byte
			n, err := unix.Fgetxattr(int(directory.Fd()), name, value[:])
			if err != nil || string(value[:n]) != "keep" {
				t.Fatalf("original xattr lost: %q, %v", value, err)
			}
		})
	}
}

func TestCheckoutDirectoryRemovalRejectsConcurrentChmod(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.Mkdir("directory", 0o755); err != nil {
		t.Fatal(err)
	}
	original, err := root.Lstat("directory")
	if err != nil {
		t.Fatal(err)
	}
	prepared := preparedCheckoutFiles{root: root, removedDirs: map[string]checkoutDirectoryMetadata{"directory": {info: original}}}
	if err = root.Chmod("directory", 0o700); err != nil {
		t.Fatal(err)
	}
	if err = prepared.removeReplacedDirectories("directory"); ErrorCode(err) != CodeConflict || errors.Unwrap(err) == nil || !strings.Contains(errors.Unwrap(err).Error(), "ownership or mode changed") {
		t.Fatalf("concurrent chmod rejection = %v", err)
	}
	current, err := root.Lstat("directory")
	if err != nil || current.Mode().Perm() != 0o700 {
		t.Fatalf("concurrent permissions lost: %v, %v", current, err)
	}
	if prepared.mutated {
		t.Fatal("concurrent chmod refusal mutated directory")
	}
}

func TestCheckoutTransitionRejectsDirectoryNodump(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-preparation=%t", late), func(t *testing.T) {
			target, before, incoming := checkoutPathTypeTransition(t, true)
			root, err := os.OpenRoot(target)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			var prepared *preparedCheckoutFiles
			if late {
				prepared, err = prepareCheckoutFiles(root, before, incoming)
				if err != nil {
					t.Fatal(err)
				}
				defer prepared.Release()
			}
			directory, err := root.Open("file.txt/nested")
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			installCheckoutTestNodump(t, directory)
			if !late {
				assertCheckoutMetadataPreflightConflict(t, target, before, incoming)
			}
			if late {
				err = prepared.Apply()
			} else {
				prepared, err = prepareCheckoutFiles(root, before, incoming)
			}
			if prepared != nil {
				defer prepared.Release()
			}
			if ErrorCode(err) != CodeConflict || errors.Unwrap(err) == nil || !strings.Contains(errors.Unwrap(err).Error(), "inode flags") {
				t.Fatalf("NODUMP refusal = %v", err)
			}
			if late {
				if err = prepared.Rollback(); err != nil {
					t.Fatal(err)
				}
				prepared.Release()
			}
			after, err := ObserveCheckout(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			if after.RevalidationDigest != before.RevalidationDigest {
				t.Fatal("original checkout not preserved")
			}
			if err = rejectCheckoutDirectoryFlags(directory); err == nil {
				t.Fatal("original NODUMP flag lost")
			}
		})
	}
}

func TestCheckoutRestoredDirectoryRejectsNodump(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.Mkdir("directory", 0o755); err != nil {
		t.Fatal(err)
	}
	originalInfo, err := root.Lstat("directory")
	if err != nil {
		t.Fatal(err)
	}
	original, err := checkCheckoutDirectoryPermissions(root, "directory", originalInfo)
	if err != nil {
		t.Fatal(err)
	}
	if err = root.Remove("directory"); err != nil {
		t.Fatal(err)
	}
	if err = root.Mkdir("directory", 0o755); err != nil {
		t.Fatal(err)
	}
	recreated, err := root.Lstat("directory")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := root.Open("directory")
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	installCheckoutTestNodump(t, directory)
	restoredDirectory, err := restoreCheckoutDirectoryMetadata(root, "directory", recreated, original)
	if restoredDirectory != nil {
		restoredDirectory.Close()
	}
	if ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("restored NODUMP refusal = %v", err)
	}
	if err = rejectCheckoutDirectoryFlags(directory); err == nil {
		t.Fatal("restoration silently removed NODUMP")
	}
	directory.Close()
	if err = rejectCheckoutDirectoryFlags(directory); err == nil {
		t.Fatal("failed flag inspection accepted")
	}
}

func TestCheckoutRollbackPinnedParentSwap(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, path := range []string{"private", "private/nested"} {
		if err = root.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originalInfo, err := root.Lstat("private/nested")
	if err != nil {
		t.Fatal(err)
	}
	original, err := checkCheckoutDirectoryPermissions(root, "private/nested", originalInfo)
	if err != nil {
		t.Fatal(err)
	}
	if err = root.Remove("private/nested"); err != nil {
		t.Fatal(err)
	}
	if err = root.Mkdir("private/nested", 0o755); err != nil {
		t.Fatal(err)
	}
	recreated, err := root.Lstat("private/nested")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := restoreCheckoutDirectoryMetadata(root, "private/nested", recreated, original)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	backup, err := root.Create("backup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backup.WriteString("private backup"); err != nil {
		t.Fatal(err)
	}
	backup.Close()
	// Swap an ancestor after verification, before installing the backup.
	if err = root.Rename("private", "moved"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"private", "private/nested"} {
		if err = root.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prepared := preparedCheckoutFiles{root: root, recreatedDirs: map[string]os.FileInfo{"private/nested": recreated}}
	previous, _, err := checkoutFileOnRoot(root, "backup")
	if err != nil {
		t.Fatal(err)
	}
	backupInfo, err := root.Lstat("backup")
	if err != nil {
		t.Fatal(err)
	}
	record := preparedCheckoutFile{path: "private/nested/secret", backupTemp: "backup", previous: &previous, backupInfo: backupInfo}
	if err = prepared.restoreBackupAt(&record, parent, []string{"private/nested"}); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("parent swap result = %v", err)
	}
	if _, err = root.Lstat("private/nested/secret"); !os.IsNotExist(err) {
		t.Fatalf("backup reached replacement directory: %v", err)
	}
	for _, path := range []string{"backup", "moved/nested/secret"} {
		file, err := root.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(file)
		file.Close()
		if err != nil || string(data) != "private backup" {
			t.Fatalf("recovery contents lost at %s", path)
		}
	}
}

func assertCheckoutMetadataPreflightConflict(t *testing.T, target string, before CheckoutState, incoming ExtractedCheckout) {
	t.Helper()
	if _, err := PreflightCheckoutFiles(t.Context(), target, incoming.State.Files); ErrorCode(err) != CodeConflict {
		t.Fatalf("paged metadata preflight = %v", err)
	}
	if err := PreflightCheckoutApplication(t.Context(), target, before, incoming.State.Files, incoming.State.AbsentPaths); ErrorCode(err) != CodeConflict {
		t.Fatalf("full metadata preflight = %v", err)
	}
	var absent []string
	var absentPage []CheckoutFile
	for _, entry := range incoming.State.Files {
		absent = append(absent, entry.Path)
		absentPage = append(absentPage, CheckoutFile{Path: entry.Path, Kind: "absent"})
	}
	if _, err := PreflightCheckoutFiles(t.Context(), target, absentPage); ErrorCode(err) != CodeConflict {
		t.Fatalf("paged absent metadata preflight = %v", err)
	}
	if err := PreflightCheckoutApplication(t.Context(), target, before, nil, absent); ErrorCode(err) != CodeConflict {
		t.Fatalf("absent metadata preflight = %v", err)
	}
}

func TestCheckoutRollbackRejectsSwappedBackupBeforeInstall(t *testing.T) {
	for _, variant := range []string{"file", "symlink", "same-content", "in-place"} {
		t.Run(variant, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			writeCheckoutFile(t, filepath.Join(root.Name(), "backup"), "private original", 0o600)
			previous, _, err := checkoutFileOnRoot(root, "backup")
			if err != nil {
				t.Fatal(err)
			}
			info, err := root.Lstat("backup")
			if err != nil {
				t.Fatal(err)
			}
			record := preparedCheckoutFile{path: "restored", backupTemp: "backup", previous: &previous, backupInfo: info}
			// Model the swap after Rollback's initial validation, before Linkat.
			if variant != "in-place" {
				if err = root.Rename("backup", "original"); err != nil {
					t.Fatal(err)
				}
			}
			if variant == "symlink" {
				err = root.Symlink("original", "backup")
			} else {
				content := "unrelated replacement"
				if variant == "same-content" {
					content = "private original"
				}
				err = os.WriteFile(filepath.Join(root.Name(), "backup"), []byte(content), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			parent, err := root.Open(".")
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			prepared := preparedCheckoutFiles{root: root}
			if err = prepared.restoreBackupAt(&record, parent, nil); ErrorCode(err) != CodeOutcomeUnknown {
				t.Fatalf("restore = %v", err)
			}
			if _, err = root.Lstat("restored"); !os.IsNotExist(err) {
				t.Fatalf("changed backup installed: %v", err)
			}
			if !record.preserveBackup {
				t.Fatal("backup not retained")
			}
			if _, err = root.Lstat("backup"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckoutRestoredBackupRejectsRetainedWriter(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	writer, err := root.Create("backup")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = writer.WriteString("original"); err != nil {
		t.Fatal(err)
	}
	previous, _, err := checkoutFileOnRoot(root, "backup")
	if err != nil {
		t.Fatal(err)
	}
	if err = root.Link("backup", "linked"); err != nil {
		t.Fatal(err)
	}
	linked, present, err := checkoutFileOnRoot(root, "linked")
	if err != nil || !present || !checkoutFileContentEqual(linked, previous) {
		t.Fatal("invalid initial rollback link")
	}
	// The original descriptor changes the validated inode before installation.
	if _, err = writer.WriteAt([]byte("modified"), 0); err != nil {
		t.Fatal(err)
	}
	if err = root.Rename("linked", "restored"); err != nil {
		t.Fatal(err)
	}
	record := preparedCheckoutFile{path: "restored", backupTemp: "backup", previous: &previous}
	prepared := preparedCheckoutFiles{root: root}
	if err = prepared.verifyRestoredBackup(&record); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("installed verification = %v", err)
	}
	if !record.preserveBackup {
		t.Fatal("recovery material was not retained")
	}
	prepared.entries = []preparedCheckoutFile{record}
	prepared.Release()
	if _, err = root.Lstat("backup"); err != nil {
		t.Fatal("backup released after failed installed verification")
	}
}

func TestCheckoutEmptyDirectoryRecoveryRejectsAncestorSwap(t *testing.T) {
	t.Parallel()
	for _, afterMetadata := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-metadata=%t", afterMetadata), func(t *testing.T) {
			target := t.TempDir()
			if err := os.MkdirAll(filepath.Join(target, "outer/inner"), 0o700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(target)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			originalInfo, err := root.Lstat("outer/inner")
			if err != nil {
				t.Fatal(err)
			}
			original, err := checkCheckoutDirectoryPermissions(root, "outer/inner", originalInfo)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Chmod(filepath.Join(target, "outer/inner"), 0o755); err != nil {
				t.Fatal(err)
			}
			recreated, err := root.Lstat("outer/inner")
			if err != nil {
				t.Fatal(err)
			}
			if afterMetadata {
				directory, err := restoreCheckoutDirectoryMetadata(root, "outer/inner", recreated, original)
				if err != nil {
					t.Fatal(err)
				}
				directory.Close()
			}
			if err = root.Rename("outer", "moved"); err != nil {
				t.Fatal(err)
			}
			if err = root.Symlink("moved", "outer"); err != nil {
				t.Fatal(err)
			}
			if !afterMetadata {
				if _, err = restoreCheckoutDirectoryMetadata(root, "outer/inner", recreated, original); ErrorCode(err) != CodeOutcomeUnknown {
					t.Fatalf("metadata through symlink = %v", err)
				}
				info, err := root.Lstat("moved/inner")
				if err != nil || info.Mode().Perm() != 0o755 {
					t.Fatal("metadata applied through replaced ancestry")
				}
			}
			prepared := preparedCheckoutFiles{root: root, removedDirs: map[string]checkoutDirectoryMetadata{"outer/inner": original}, recreatedDirs: map[string]os.FileInfo{"outer/inner": recreated}}
			if err = prepared.verifyRestoredDirectoryAncestry(); ErrorCode(err) != CodeOutcomeUnknown {
				t.Fatalf("empty directory final verification = %v", err)
			}
		})
	}
}

func TestCheckoutRootBackupInstallPreservesCollision(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			writeCheckoutFile(t, filepath.Join(root.Name(), "backup"), "original private data", 0o600)
			previous, _, err := checkoutFileOnRoot(root, "backup")
			if err != nil {
				t.Fatal(err)
			}
			info, err := root.Lstat("backup")
			if err != nil {
				t.Fatal(err)
			}
			if err = root.Mkdir("restored", 0o755); err != nil {
				t.Fatal(err)
			}
			if err = root.Remove("restored"); err != nil {
				t.Fatal(err)
			}
			// A concurrent leaf appears after the transition directory is removed.
			if kind == "file" {
				writeCheckoutFile(t, filepath.Join(root.Name(), "restored"), "concurrent", 0o600)
			} else {
				if err = root.Symlink("elsewhere", "restored"); err != nil {
					t.Fatal(err)
				}
			}
			concurrent, err := root.Lstat("restored")
			if err != nil {
				t.Fatal(err)
			}
			record := preparedCheckoutFile{path: "restored", backupTemp: "backup", previous: &previous, backupInfo: info}
			parent, err := openCheckoutDirectoryNoFollow(root, ".")
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			prepared := preparedCheckoutFiles{root: root}
			if err = prepared.restoreBackupAt(&record, parent, nil); ErrorCode(err) != CodeOutcomeUnknown {
				t.Fatalf("collision = %v", err)
			}
			prepared.entries = []preparedCheckoutFile{record}
			prepared.Release()
			after, err := root.Lstat("restored")
			if err != nil || !os.SameFile(after, concurrent) {
				t.Fatal("concurrent leaf overwritten")
			}
			data, err := os.ReadFile(filepath.Join(root.Name(), "backup"))
			if err != nil || string(data) != "original private data" {
				t.Fatal("backup not retained")
			}
		})
	}
}
