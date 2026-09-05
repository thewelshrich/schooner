//go:build linux || darwin

package repository

import (
	"errors"
	"fmt"
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
	if err = restoreCheckoutDirectoryMetadata(root, "shared", recreated, original); err != nil {
		t.Fatal(err)
	}
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
	prepared.removedDirs["file.txt"] = original
	if err = prepared.Rollback(); err != nil {
		t.Fatal(err)
	}
	prepared.Release()
	after, err := ObserveCheckout(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if after.RevalidationDigest != before.RevalidationDigest {
		t.Fatal("retry did not fully restore the checkout")
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
