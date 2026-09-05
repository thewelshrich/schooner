//go:build linux || darwin

package repository

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
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
	original, err := root.Lstat("shared")
	if err != nil {
		t.Fatal(err)
	}
	if original.Mode()&os.ModeSetgid == 0 {
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
	got, want := restored.Sys().(*syscall.Stat_t), original.Sys().(*syscall.Stat_t)
	if got.Uid != want.Uid || got.Gid != want.Gid || restored.Mode() != original.Mode() {
		t.Fatalf("restored ownership/mode = %d:%d %v, want %d:%d %v", got.Uid, got.Gid, restored.Mode(), want.Uid, want.Gid, original.Mode())
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
	stat := *original.Sys().(*syscall.Stat_t)
	stat.Uid = uint32(os.Geteuid() + 1)
	prepared.removedDirs["file.txt"] = checkoutOwnershipInfo{FileInfo: original, stat: stat}
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
