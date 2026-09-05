//go:build linux || darwin

package repository

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// Restore ownership before permissions: chown can clear setuid/setgid bits.
// Use the opened directory identity so a replacement cannot receive metadata.
// The caller owns the returned handle and keeps it open through backup installation.
func restoreCheckoutDirectoryMetadata(root *os.Root, path string, recreated os.FileInfo, original checkoutDirectoryMetadata) (directory *os.File, result error) {
	defer func() {
		if result != nil {
			if directory != nil {
				directory.Close()
				directory = nil
			}
			result = &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("directory %q ownership, permissions, or provenance could not be restored", path), Cause: result}
		}
	}()
	want, ok := original.info.Sys().(*syscall.Stat_t)
	if !ok {
		return directory, fmt.Errorf("captured directory ownership is unavailable")
	}
	var err error
	directory, err = openCheckoutDirectoryNoFollow(root, path)
	if err != nil {
		return directory, err
	}
	info, err := directory.Stat()
	if err != nil {
		return directory, err
	}
	if !info.IsDir() || !os.SameFile(info, recreated) {
		return directory, fmt.Errorf("recreated directory changed independently")
	}
	got, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return directory, fmt.Errorf("current directory ownership is unavailable")
	}
	if got.Uid != want.Uid || got.Gid != want.Gid {
		if err = directory.Chown(int(want.Uid), int(want.Gid)); err != nil {
			return directory, err
		}
	}
	mode := original.info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	if err = directory.Chmod(mode); err != nil {
		return directory, err
	}
	info, err = directory.Stat()
	if err != nil {
		return directory, err
	}
	got, ok = info.Sys().(*syscall.Stat_t)
	if !ok || got.Uid != want.Uid || got.Gid != want.Gid || info.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != mode {
		return directory, fmt.Errorf("directory ownership or permission bits did not match the backup")
	}
	metadata, err := checkoutDirectoryExtendedMetadata(directory)
	if err != nil {
		return directory, err
	}
	if !checkoutDirectoryProvenanceEqual(metadata, original) {
		return directory, fmt.Errorf("recreated directory provenance differs from the backup")
	}
	if err = verifyCheckoutDirectoryIdentity(root, path, recreated); err != nil {
		return directory, err
	}
	return directory, nil
}

// Captures do not preserve ACLs or extended attributes. Inspect the pinned
// directory descriptor and refuse unsupported metadata before removal.
func checkCheckoutDirectoryPermissions(root *os.Root, path string, expected os.FileInfo) (metadata checkoutDirectoryMetadata, result error) {
	defer func() {
		if result != nil {
			result = &Error{Code: CodeConflict, Message: fmt.Sprintf("directory %q extended permissions or attributes cannot be safely preserved", path), Cause: result}
		}
	}()
	directory, err := openCheckoutDirectoryNoFollow(root, path)
	if err != nil {
		return metadata, err
	}
	defer directory.Close()
	return inspectCheckoutDirectoryPermissions(directory, expected)
}

func inspectCheckoutDirectoryPermissions(directory *os.File, expected os.FileInfo) (metadata checkoutDirectoryMetadata, err error) {
	info, err := directory.Stat()
	if err != nil {
		return metadata, err
	}
	if !os.SameFile(info, expected) {
		return metadata, fmt.Errorf("directory changed independently")
	}
	want, wantOK := expected.Sys().(*syscall.Stat_t)
	got, gotOK := info.Sys().(*syscall.Stat_t)
	if !wantOK || !gotOK || got.Uid != want.Uid || got.Gid != want.Gid || info.Mode() != expected.Mode() {
		return metadata, fmt.Errorf("directory ownership or mode changed independently")
	}
	metadata, err = checkoutDirectoryExtendedMetadata(directory)
	metadata.info = info
	return metadata, err
}

// Only Darwin creation provenance may survive automatically. Retain and compare
// presence and bytes; never write it or accept any other extended attribute.
func checkoutDirectoryExtendedMetadata(directory *os.File) (metadata checkoutDirectoryMetadata, err error) {
	if err = rejectCheckoutDirectoryFlags(directory); err != nil {
		return metadata, err
	}
	size, err := unix.Flistxattr(int(directory.Fd()), nil)
	if err != nil {
		return metadata, err
	}
	if size != 0 {
		const name = "com.apple.provenance"
		if runtime.GOOS != "darwin" || size != len(name)+1 {
			return metadata, fmt.Errorf("directory has unsupported extended attributes")
		}
		names := make([]byte, size)
		n, err := unix.Flistxattr(int(directory.Fd()), names)
		if err != nil {
			return metadata, err
		}
		if n != len(names) || string(names) != name+"\x00" {
			return metadata, fmt.Errorf("directory has unsupported extended attributes")
		}
		size, err = unix.Fgetxattr(int(directory.Fd()), name, nil)
		if err != nil {
			return metadata, err
		}
		if size < 0 || size > 65536 {
			return metadata, fmt.Errorf("directory provenance exceeds supported size")
		}
		metadata.provenance = make([]byte, size)
		n, err = unix.Fgetxattr(int(directory.Fd()), name, metadata.provenance)
		if err != nil {
			return metadata, err
		}
		if n != size {
			return metadata, fmt.Errorf("directory provenance changed during inspection")
		}
		metadata.hasProvenance = true
	}
	return metadata, rejectCheckoutDirectoryACL(directory)
}

func checkoutDirectoryProvenanceEqual(left, right checkoutDirectoryMetadata) bool {
	return left.hasProvenance == right.hasProvenance && bytes.Equal(left.provenance, right.provenance)
}

// Install through the still-open verified parent. Keep the original backup until
// the root-visible ancestry is revalidated, so a moved parent cannot consume it.
func (prepared *preparedCheckoutFiles) restoreBackupAt(record *preparedCheckoutFile, parent *os.File, ancestors []string) error {
	sourceRoot, err := prepared.root.OpenRoot(filepath.Dir(record.backupTemp))
	if err != nil {
		return err
	}
	defer sourceRoot.Close()
	source, err := sourceRoot.Open(".")
	if err != nil {
		return err
	}
	defer source.Close()
	temporary, err := checkoutTemporaryPath(record.backupTemp)
	if err != nil {
		return err
	}
	name := filepath.Base(temporary)
	if err = unix.Linkat(int(source.Fd()), filepath.Base(record.backupTemp), int(source.Fd()), name, 0); err != nil {
		return err
	}
	defer unix.Unlinkat(int(source.Fd()), name, 0)
	linkedInfo, statErr := sourceRoot.Lstat(name)
	linked, present, inspectErr := checkoutFileOnRoot(sourceRoot, name)
	if statErr != nil || inspectErr != nil || !present || record.previous == nil || record.backupInfo == nil || !os.SameFile(linkedInfo, record.backupInfo) || !checkoutFileContentEqual(linked, *record.previous) {
		record.preserveBackup = true
		return &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("rollback material for destination path %q changed before installation; recovery backup preserved at %q", record.path, record.backupTemp)}
	}
	if err = renameNoReplaceAt(source, name, parent, filepath.Base(record.path)); err != nil {
		return err
	}
	for _, path := range ancestors {
		err := verifyCheckoutDirectoryIdentity(prepared.root, path, prepared.recreatedDirs[path])
		if err != nil {
			return &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("restored directory %q moved; recovery backup preserved at %q", path, record.backupTemp), Cause: err}
		}
	}
	if err := prepared.verifyRestoredBackup(record); err != nil {
		return err
	}
	return unix.Unlinkat(int(source.Fd()), filepath.Base(record.backupTemp), 0)
}
