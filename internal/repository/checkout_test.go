package repository

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/process"
)

type cancelAfterGitMutationRunner struct {
	cancel    context.CancelFunc
	delegate  runner
	match     func([]string) bool
	cancelled bool
}

type mutateBeforeGitRunner struct {
	delegate  runner
	match     func([]string) bool
	before    func()
	cancel    context.CancelFunc
	triggered bool
}

func (value *mutateBeforeGitRunner) Run(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	if !value.triggered && value.match(arguments) {
		value.triggered = true
		value.before()
	}
	output, err := value.delegate.Run(ctx, name, arguments...)
	if err == nil && value.triggered && value.cancel != nil {
		value.cancel()
		value.cancel = nil
		return output, context.Canceled
	}
	return output, err
}

func (value *cancelAfterGitMutationRunner) Run(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	output, err := value.delegate.Run(ctx, name, arguments...)
	if err == nil && !value.cancelled && value.match(arguments) {
		value.cancelled = true
		value.cancel()
		return output, context.Canceled
	}
	return output, err
}

func TestCheckoutCaptureAndApplyPreservesWorkspaceState(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	runCheckoutGit(t, "init", source)
	runCheckoutGit(t, "-C", source, "config", "user.name", "Test")
	runCheckoutGit(t, "-C", source, "config", "user.email", "test@example.com")
	writeCheckoutFile(t, filepath.Join(source, ".gitignore"), "ignored.env\n", 0o644)
	writeCheckoutFile(t, filepath.Join(source, "mixed.txt"), "base\n", 0o644)
	writeCheckoutFile(t, filepath.Join(source, "deleted.txt"), "delete\n", 0o644)
	writeCheckoutFile(t, filepath.Join(source, "tool"), "#!/bin/sh\necho base\n", 0o755)
	if err := os.Symlink("mixed.txt", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, "-C", source, "add", ".")
	runCheckoutGit(t, "-C", source, "commit", "-m", "base")
	writeCheckoutFile(t, filepath.Join(source, "mixed.txt"), "staged\n", 0o644)
	runCheckoutGit(t, "-C", source, "add", "mixed.txt")
	writeCheckoutFile(t, filepath.Join(source, "mixed.txt"), "unstaged\n", 0o644)
	if err := os.Remove(filepath.Join(source, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, filepath.Join(source, "new.bin"), string([]byte{0, 1, 2, 255}), 0o644)
	writeCheckoutFile(t, filepath.Join(source, "ignored.env"), "destination-local\n", 0o644)

	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Release()
	for _, entry := range capture.State.Files {
		if entry.Path == "ignored.env" {
			t.Fatal("ignored file was captured")
		}
	}
	extracted, err := ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer extracted.Release()
	destination := filepath.Join(t.TempDir(), "destination")
	applied, err := ApplyCheckout(t.Context(), destination, extracted)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Digest != capture.State.Digest {
		t.Fatalf("digest = %s, want %s", applied.Digest, capture.State.Digest)
	}
	sourceStatus := runCheckoutGit(t, "-C", source, "status", "--porcelain=v2")
	destinationStatus := runCheckoutGit(t, "-C", destination, "status", "--porcelain=v2")
	if !bytes.Equal(sourceStatus, destinationStatus) {
		t.Fatalf("status differs\nsource:\n%s\ndestination:\n%s", sourceStatus, destinationStatus)
	}
	if _, err = os.Lstat(filepath.Join(destination, "ignored.env")); !os.IsNotExist(err) {
		t.Fatalf("ignored file transferred: %v", err)
	}
}

func TestApplyCheckoutAllowsRewindOnlyForExactOperationCreatedSeed(t *testing.T) {
	source := checkoutTestRepository(t)
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

	destination := filepath.Join(t.TempDir(), "destination")
	runCheckoutGit(t, "clone", source, destination)
	runCheckoutGit(t, "-C", destination, "config", "user.name", "Test")
	runCheckoutGit(t, "-C", destination, "config", "user.email", "test@example.com")
	writeCheckoutFile(t, filepath.Join(destination, "remote-only.txt"), "published after local HEAD\n", 0o644)
	runCheckoutGit(t, "-C", destination, "add", "remote-only.txt")
	runCheckoutGit(t, "-C", destination, "commit", "-m", "newer clone seed")
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := ObserveCheckout(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = ApplyCheckoutIfUnchanged(t.Context(), destination, payload, seed.Digest); ErrorCode(err) != CodeConflict {
		t.Fatalf("ordinary destination rewind error = %v, want conflict", err)
	}
	applied, err := ApplyCheckoutIfOperationCreatedAndUnchanged(t.Context(), destination, payload, seed.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Digest != capture.State.Digest || applied.HEAD != capture.State.HEAD {
		t.Fatalf("applied state = %+v, want source digest %s at %s", applied, capture.State.Digest, capture.State.HEAD)
	}
}

func TestApplyCheckoutWithinRootCreatesNestedDestinationAndRejectsSymlinkAncestor(t *testing.T) {
	source := checkoutTestRepository(t)
	capture, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Release()
	extracted, err := ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer extracted.Release()
	root := t.TempDir()
	target := filepath.Join(root, "owner", "repository")
	if _, err = ApplyCheckoutWithinRoot(t.Context(), root, target, extracted); err != nil {
		t.Fatal(err)
	}

	otherRoot := t.TempDir()
	outside := t.TempDir()
	if err = os.Symlink(outside, filepath.Join(otherRoot, "owner")); err != nil {
		t.Fatal(err)
	}
	escaped := filepath.Join(otherRoot, "owner", "repository")
	if _, err = ApplyCheckoutWithinRoot(t.Context(), otherRoot, escaped, extracted); ErrorCode(err) != CodeConflict {
		t.Fatalf("symlink ancestor create error = %v", err)
	}
	if _, err = os.Lstat(filepath.Join(outside, "repository")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first-create escaped its Worktree Root: %v", err)
	}
}

func TestCheckoutApplyPreservesIgnoredDestinationFilesAndRejectsCollision(t *testing.T) {
	source := checkoutTestRepository(t)
	capture, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	applyCheckoutCapture(t, destination, capture)
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	capture.Release()
	writeCheckoutFile(t, filepath.Join(destination, "ignored.env"), "remote-only\n", 0o600)
	writeCheckoutFile(t, filepath.Join(source, "file.txt"), "changed\n", 0o644)
	capture, err = CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	applyCheckoutCapture(t, destination, capture)
	capture.Release()
	contents, err := os.ReadFile(filepath.Join(destination, "ignored.env"))
	if err != nil || string(contents) != "remote-only\n" {
		t.Fatalf("ignored destination file = %q, err=%v", contents, err)
	}
	writeCheckoutFile(t, filepath.Join(source, "ignored.env"), "incoming\n", 0o644)
	runCheckoutGit(t, "-C", source, "add", "--force", "ignored.env")
	capture, err = CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Release()
	extracted, err := ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer extracted.Release()
	if _, err = ApplyCheckout(t.Context(), destination, extracted); ErrorCode(err) != CodeConflict {
		t.Fatalf("collision error = %v", err)
	}
}

func TestCheckoutApplyVerifiesMultipleTrackedDeletionsInOneDirectory(t *testing.T) {
	source := checkoutTestRepository(t)
	if err := os.MkdirAll(filepath.Join(source, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, filepath.Join(source, "dir", "a.txt"), "a\n", 0o644)
	writeCheckoutFile(t, filepath.Join(source, "dir", "b.txt"), "b\n", 0o644)
	runCheckoutGit(t, "-C", source, "add", "dir")
	runCheckoutGit(t, "-C", source, "commit", "-m", "directory files")
	base, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	applyCheckoutCapture(t, destination, base)
	base.Release()
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, "-C", source, "rm", "dir/a.txt", "dir/b.txt")
	deleted, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer deleted.Release()
	applied := applyCheckoutCapture(t, destination, deleted)
	if applied.Digest != deleted.State.Digest {
		t.Fatalf("digest = %s, want %s", applied.Digest, deleted.State.Digest)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err = os.Lstat(filepath.Join(destination, "dir", name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted destination path %s exists: %v", name, err)
		}
	}
}

func TestCheckoutCaptureAndApplyDetachedHEAD(t *testing.T) {
	source := checkoutTestRepository(t)
	runCheckoutGit(t, "-C", source, "checkout", "--detach")
	capture, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Release()
	destination := filepath.Join(t.TempDir(), "destination")
	applied := applyCheckoutCapture(t, destination, capture)
	if !applied.Detached || applied.Branch != "" {
		t.Fatalf("applied HEAD state = %+v", applied)
	}
}

func TestCheckoutCreateForcesSHA1ObjectFormat(t *testing.T) {
	source := checkoutTestRepository(t)
	capture, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Release()
	t.Setenv("GIT_DEFAULT_HASH", "sha256")
	destination := filepath.Join(t.TempDir(), "destination")
	applyCheckoutCapture(t, destination, capture)
	format := strings.TrimSpace(string(runCheckoutGit(t, "-C", destination, "rev-parse", "--show-object-format")))
	if format != "sha1" {
		t.Fatalf("destination object format = %q, want sha1", format)
	}
}

func TestCheckoutCreateRecordsCredentialFreeNetworkOriginBeforeVerification(t *testing.T) {
	source := checkoutTestRepository(t)
	runCheckoutGit(t, "-C", source, "remote", "add", "origin", "https://example.com/owner/repo.git")
	capture, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Release()
	destination := filepath.Join(t.TempDir(), "destination")
	applied := applyCheckoutCapture(t, destination, capture)
	if applied.Digest != capture.State.Digest || applied.RepositoryIdentity != "example.com/owner/repo" {
		t.Fatalf("applied = %+v, source = %+v", applied, capture.State)
	}
	destination = applied.Worktree
	if origin := string(bytes.TrimSpace(runCheckoutGit(t, "-C", destination, "remote", "get-url", "origin"))); origin != "https://example.com/owner/repo.git" {
		t.Fatalf("origin = %q", origin)
	}
	runCheckoutGit(t, "-C", destination, "remote", "remove", "origin")
	writeCheckoutFile(t, filepath.Join(source, "file.txt"), "source update\n", 0o644)
	next, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer next.Release()
	extracted, err := ExtractCheckoutPayload(next.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer extracted.Release()
	expected, err := ObserveCheckout(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	applied, err = ApplyCheckoutIfUnchanged(t.Context(), destination, extracted, expected.Digest)
	if err != nil || applied.Digest != next.State.Digest || applied.RepositoryIdentity != "" {
		t.Fatalf("one-sided identity apply = %+v, err=%v", applied, err)
	}
}

func TestApplyCheckoutIfUnchangedProtectsLateDestinationEdit(t *testing.T) {
	source := checkoutTestRepository(t)
	initial, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	applyCheckoutCapture(t, destination, initial)
	initial.Release()
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := ObserveCheckout(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, filepath.Join(source, "file.txt"), "incoming\n", 0o644)
	incoming, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Release()
	extracted, err := ExtractCheckoutPayload(incoming.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer extracted.Release()
	writeCheckoutFile(t, filepath.Join(destination, "file.txt"), "late edit\n", 0o644)
	if _, err = ApplyCheckoutIfUnchanged(t.Context(), destination, extracted, expected.Digest); ErrorCode(err) != CodeConflict {
		t.Fatalf("late edit error = %v", err)
	} else if CheckoutMutationStarted(err) {
		t.Fatalf("late edit was incorrectly reported as a partially applied checkout: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "file.txt"))
	if err != nil || string(contents) != "late edit\n" {
		t.Fatalf("destination contents = %q, err=%v", contents, err)
	}
}

func TestApplyCheckoutRejectsIncomingUntrackedPathIgnoredOnlyByDestination(t *testing.T) {
	source := checkoutTestRepository(t)
	initial, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	applyCheckoutCapture(t, destination, initial)
	initial.Release()
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, filepath.Join(destination, ".git", "info", "exclude"), "remote-only.txt\n", 0o600)
	writeCheckoutFile(t, filepath.Join(source, "remote-only.txt"), "incoming\n", 0o644)
	incoming, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Release()
	extracted, err := ExtractCheckoutPayload(incoming.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer extracted.Release()
	expected, err := ObserveCheckout(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyCheckoutIfUnchanged(t.Context(), destination, extracted, expected.Digest); ErrorCode(err) != CodeConflict {
		t.Fatalf("destination ignore conflict = %v", err)
	} else if CheckoutMutationStarted(err) {
		t.Fatalf("destination ignore conflict was detected after mutation: %v", err)
	}
	if _, err = os.Lstat(filepath.Join(destination, "remote-only.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ignored incoming path was left behind: %v", err)
	}
}

func TestPreflightCheckoutFilesRejectsSymlinkParentForAbsentIncomingPath(t *testing.T) {
	destination := checkoutTestRepository(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(destination, "parent")); err != nil {
		t.Fatal(err)
	}
	_, err := PreflightCheckoutFiles(t.Context(), destination, []CheckoutFile{{Path: "parent/file.txt", Kind: "file", Tracked: true}})
	if ErrorCode(err) != CodeConflict {
		t.Fatalf("symlink parent preflight error = %v", err)
	}
}

func TestRestoreCheckoutAfterFailedApplyRemovesIncomingOnlyFiles(t *testing.T) {
	source := checkoutTestRepository(t)
	initial, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	applyCheckoutCapture(t, destination, initial)
	initial.Release()
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := CaptureCheckout(t.Context(), destination, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Release()
	backupPayload, err := ExtractCheckoutPayload(backup.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer backupPayload.Release()

	writeCheckoutFile(t, filepath.Join(source, "file.txt"), "incoming replacement\n", 0o644)
	writeCheckoutFile(t, filepath.Join(source, "incoming-only.txt"), "incoming addition\n", 0o644)
	incoming, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Release()
	incomingPayload, err := ExtractCheckoutPayload(incoming.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer incomingPayload.Release()
	writeCheckoutFile(t, filepath.Join(destination, "file.txt"), "incoming replacement\n", 0o644)
	writeCheckoutFile(t, filepath.Join(destination, "incoming-only.txt"), "incoming addition\n", 0o644)

	restored, err := RestoreCheckoutAfterFailedApply(t.Context(), destination, backupPayload, incomingPayload)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Digest != backup.State.Digest {
		t.Fatalf("restored digest = %s, want %s", restored.Digest, backup.State.Digest)
	}
	if _, err = os.Lstat(filepath.Join(destination, "incoming-only.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incoming-only path remained after rollback: %v", err)
	}
}

func TestRestoreCheckoutAfterFailedApplyProtectsConcurrentEdit(t *testing.T) {
	source := checkoutTestRepository(t)
	initial, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	applyCheckoutCapture(t, destination, initial)
	initial.Release()
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := CaptureCheckout(t.Context(), destination, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Release()
	backupPayload, err := ExtractCheckoutPayload(backup.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer backupPayload.Release()
	writeCheckoutFile(t, filepath.Join(source, "incoming-only.txt"), "incoming addition\n", 0o644)
	incoming, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Release()
	incomingPayload, err := ExtractCheckoutPayload(incoming.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer incomingPayload.Release()
	writeCheckoutFile(t, filepath.Join(destination, "incoming-only.txt"), "editor changed this\n", 0o644)

	if _, err = RestoreCheckoutAfterFailedApply(t.Context(), destination, backupPayload, incomingPayload); ErrorCode(err) != CodeConflict {
		t.Fatalf("concurrent rollback edit error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "incoming-only.txt"))
	if err != nil || string(contents) != "editor changed this\n" {
		t.Fatalf("concurrent edit = %q, err=%v", contents, err)
	}
}

func TestRestoreCheckoutAfterFailedApplyProtectsConcurrentIndexChange(t *testing.T) {
	source := checkoutTestRepository(t)
	initial, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	applyCheckoutCapture(t, destination, initial)
	initial.Release()
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := CaptureCheckout(t.Context(), destination, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Release()
	backupPayload, err := ExtractCheckoutPayload(backup.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer backupPayload.Release()
	writeCheckoutFile(t, filepath.Join(source, "incoming-only.txt"), "incoming addition\n", 0o644)
	incoming, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Release()
	incomingPayload, err := ExtractCheckoutPayload(incoming.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer incomingPayload.Release()
	writeCheckoutFile(t, filepath.Join(destination, "editor-staged.txt"), "editor work\n", 0o644)
	runCheckoutGit(t, "-C", destination, "add", "editor-staged.txt")

	if _, err = RestoreCheckoutAfterFailedApply(t.Context(), destination, backupPayload, incomingPayload); ErrorCode(err) != CodeConflict {
		t.Fatalf("concurrent index change error = %v", err)
	}
	if _, err = os.Lstat(filepath.Join(destination, "editor-staged.txt")); err != nil {
		t.Fatalf("concurrent staged file was removed: %v", err)
	}
}

func TestInstallCheckoutEntryPreservesExistingFileWhenIncomingCannotBeRead(t *testing.T) {
	target := t.TempDir()
	destination := filepath.Join(target, "file.txt")
	writeCheckoutFile(t, destination, "original\n", 0o644)
	digest := sha256.Sum256([]byte("original\n"))
	previous := CheckoutFile{Path: "file.txt", Kind: "file", Size: int64(len("original\n")), SHA256: hex.EncodeToString(digest[:]), Tracked: true}
	err := installCheckoutEntry(target, t.TempDir(), CheckoutFile{Path: "file.txt", Kind: "file", Tracked: true}, &previous)
	if err == nil {
		t.Fatal("missing incoming file unexpectedly installed")
	}
	contents, readErr := os.ReadFile(destination)
	if readErr != nil || string(contents) != "original\n" {
		t.Fatalf("existing destination = %q, err=%v", contents, readErr)
	}
}

func TestPreflightCheckoutBranchFindsWorktreeOutsideDiscoveryRoot(t *testing.T) {
	target := checkoutTestRepository(t)
	runCheckoutGit(t, "-C", target, "branch", "elsewhere")
	outside := filepath.Join(t.TempDir(), "outside", "linked")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, "-C", target, "worktree", "add", outside, "elsewhere")
	headBefore := string(bytes.TrimSpace(runCheckoutGit(t, "-C", outside, "rev-parse", "HEAD")))

	err := PreflightCheckoutBranch(t.Context(), target, CheckoutState{Branch: "elsewhere", HEAD: headBefore})
	if ErrorCode(err) != CodeConflict {
		t.Fatalf("branch collision error = %v", err)
	}
	if headAfter := string(bytes.TrimSpace(runCheckoutGit(t, "-C", outside, "rev-parse", "HEAD"))); headAfter != headBefore {
		t.Fatalf("outside Worktree HEAD changed from %s to %s", headBefore, headAfter)
	}
}

func TestPreflightCheckoutBranchRejectsSymbolicDestinationBranch(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
	}{
		{name: "resolving", target: "refs/heads/terminal"},
		{name: "dangling", target: "refs/heads/missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := checkoutTestRepository(t)
			oldBranch := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "symbolic-ref", "--short", "HEAD")))
			oldHEAD := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "HEAD")))
			if test.name == "resolving" {
				runCheckoutGit(t, "-C", target, "branch", "terminal", oldHEAD)
			}
			runCheckoutGit(t, "-C", target, "symbolic-ref", "refs/heads/incoming", test.target)

			update, err := updateCheckoutHEAD(t.Context(), target, CheckoutState{HEAD: oldHEAD, Branch: "incoming"})
			if ErrorCode(err) != CodeConflict || update != nil {
				t.Fatalf("symbolic branch update = %+v, err=%v; want conflict before mutation", update, err)
			}
			if branch := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "symbolic-ref", "--short", "HEAD"))); branch != oldBranch {
				t.Fatalf("HEAD branch = %q, want %q", branch, oldBranch)
			}
			if binding := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "symbolic-ref", "--no-recurse", "refs/heads/incoming"))); binding != test.target {
				t.Fatalf("incoming symbolic target = %q, want %q", binding, test.target)
			}
			if test.name == "resolving" {
				if terminal := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "refs/heads/terminal"))); terminal != oldHEAD {
					t.Fatalf("terminal ref = %s, want %s", terminal, oldHEAD)
				}
			}
		})
	}
}

func TestObserveCheckoutRejectsInProgressGitOperation(t *testing.T) {
	source := checkoutTestRepository(t)
	gitPath := string(bytes.TrimSpace(runCheckoutGit(t, "-C", source, "rev-parse", "--git-path", "MERGE_HEAD")))
	if !filepath.IsAbs(gitPath) {
		gitPath = filepath.Join(source, gitPath)
	}
	writeCheckoutFile(t, gitPath, "1111111111111111111111111111111111111111\n", 0o600)
	if _, err := ObserveCheckout(t.Context(), source); ErrorCode(err) != CodeUnsupported {
		t.Fatalf("in-progress operation error = %v", err)
	}
}

func TestObserveCheckoutRejectsSymlinkAncestorWithoutReadingOutsideWorktree(t *testing.T) {
	source := checkoutTestRepository(t)
	directory := filepath.Join(source, "nested")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, filepath.Join(directory, "tracked.txt"), "inside\n", 0o644)
	runCheckoutGit(t, "-C", source, "add", "nested/tracked.txt")
	runCheckoutGit(t, "-C", source, "commit", "-m", "nested file")
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeCheckoutFile(t, filepath.Join(outside, "tracked.txt"), "outside secret\n", 0o600)
	if err := os.Symlink(outside, directory); err != nil {
		t.Fatal(err)
	}

	if _, err := ObserveCheckout(t.Context(), source); ErrorCode(err) != CodeUnsupported {
		t.Fatalf("symlink ancestor error = %v", err)
	}
}

func TestObserveCheckoutRejectsAutocrlfInputAndConversionAttributes(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "autocrlf input", setup: func(t *testing.T, source string) {
			runCheckoutGit(t, "-C", source, "config", "core.autocrlf", "input")
		}},
		{name: "text attribute", setup: func(t *testing.T, source string) {
			writeCheckoutFile(t, filepath.Join(source, ".gitattributes"), "*.txt text\n", 0o644)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := checkoutTestRepository(t)
			test.setup(t, source)
			if _, err := ObserveCheckout(t.Context(), source); ErrorCode(err) != CodeUnsupported {
				t.Fatalf("unsupported conversion error = %v", err)
			}
		})
	}
}

func TestObserveCheckoutRejectsPromisorRepositoryAndTransferErrorsHideDiagnostics(t *testing.T) {
	source := checkoutTestRepository(t)
	runCheckoutGit(t, "-C", source, "config", "remote.origin.promisor", "true")
	if _, err := ObserveCheckout(t.Context(), source); ErrorCode(err) != CodeUnsupported {
		t.Fatalf("promisor repository error = %v", err)
	}

	t.Run("missing object is not fetched", func(t *testing.T) {
		source := checkoutTestRepository(t)
		remote := filepath.Join(t.TempDir(), "remote.git")
		runCheckoutGit(t, "clone", "--bare", source, remote)
		runCheckoutGit(t, "-C", source, "remote", "add", "origin", remote)
		runCheckoutGit(t, "-C", source, "config", "remote.origin.promisor", "true")
		runCheckoutGit(t, "-C", source, "config", "remote.origin.partialclonefilter", "blob:none")
		tree := strings.TrimSpace(string(runCheckoutGit(t, "-C", source, "rev-parse", "HEAD^{tree}")))
		missingObject := filepath.Join(source, ".git", "objects", tree[:2], tree[2:])
		if err := os.Remove(missingObject); err != nil {
			t.Fatal(err)
		}

		if _, err := ObserveCheckout(t.Context(), source); ErrorCode(err) != CodeUnsupported {
			t.Fatalf("incomplete promisor repository error = %v", err)
		}
		if _, err := os.Lstat(missingObject); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing object was fetched during observation: %v", err)
		}
	})

	secret := "https://token@example.com/owner/repo"
	err := gitTransferError("create Git pack", process.Result{Stderr: []byte(secret)}, errors.New("exit status 1"))
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "create Git pack") {
		t.Fatalf("transfer error leaked diagnostics: %v", err)
	}
}

func TestObserveCheckoutRejectsReplacementRefsAndGrafts(t *testing.T) {
	t.Run("replacement ref", func(t *testing.T) {
		source := checkoutTestRepository(t)
		head := strings.TrimSpace(string(runCheckoutGit(t, "-C", source, "rev-parse", "HEAD")))
		runCheckoutGit(t, "-C", source, "replace", head, head)
		if _, err := ObserveCheckout(t.Context(), source); ErrorCode(err) != CodeUnsupported {
			t.Fatalf("replacement ref error = %v", err)
		}
	})

	t.Run("grafts", func(t *testing.T) {
		source := checkoutTestRepository(t)
		gitDirectory := strings.TrimSpace(string(runCheckoutGit(t, "-C", source, "rev-parse", "--git-common-dir")))
		if !filepath.IsAbs(gitDirectory) {
			gitDirectory = filepath.Join(source, gitDirectory)
		}
		if err := os.MkdirAll(filepath.Join(gitDirectory, "info"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeCheckoutFile(t, filepath.Join(gitDirectory, "info", "grafts"), "", 0o644)
		if _, err := ObserveCheckout(t.Context(), source); ErrorCode(err) != CodeUnsupported {
			t.Fatalf("grafts error = %v", err)
		}
	})
}

func TestCommitIsAncestorIgnoresGraftAddedAfterObservation(t *testing.T) {
	repositoryPath := checkoutTestRepository(t)
	base := strings.TrimSpace(string(runCheckoutGit(t, "-C", repositoryPath, "rev-parse", "HEAD")))
	writeCheckoutFile(t, filepath.Join(repositoryPath, "left.txt"), "left\n", 0o644)
	runCheckoutGit(t, "-C", repositoryPath, "add", "left.txt")
	runCheckoutGit(t, "-C", repositoryPath, "commit", "-m", "left")
	left := strings.TrimSpace(string(runCheckoutGit(t, "-C", repositoryPath, "rev-parse", "HEAD")))
	runCheckoutGit(t, "-C", repositoryPath, "checkout", "--detach", base)
	writeCheckoutFile(t, filepath.Join(repositoryPath, "right.txt"), "right\n", 0o644)
	runCheckoutGit(t, "-C", repositoryPath, "add", "right.txt")
	runCheckoutGit(t, "-C", repositoryPath, "commit", "-m", "right")
	right := strings.TrimSpace(string(runCheckoutGit(t, "-C", repositoryPath, "rev-parse", "HEAD")))
	gitDirectory := strings.TrimSpace(string(runCheckoutGit(t, "-C", repositoryPath, "rev-parse", "--git-common-dir")))
	if !filepath.IsAbs(gitDirectory) {
		gitDirectory = filepath.Join(repositoryPath, gitDirectory)
	}
	writeCheckoutFile(t, filepath.Join(gitDirectory, "info", "grafts"), right+" "+left+"\n", 0o644)
	safe, err := CommitIsAncestor(t.Context(), repositoryPath, left, right)
	if err != nil {
		t.Fatal(err)
	}
	if safe {
		t.Fatal("legacy graft falsified ancestry safety")
	}
}

func TestPreflightCheckoutFilesRejectsIgnoredCollisionForAbsentIndexedPath(t *testing.T) {
	target := checkoutTestRepository(t)
	gitDirectory := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "--git-dir")))
	if !filepath.IsAbs(gitDirectory) {
		gitDirectory = filepath.Join(target, gitDirectory)
	}
	writeCheckoutFile(t, filepath.Join(gitDirectory, "info", "exclude"), "staged.txt\n", 0o644)
	writeCheckoutFile(t, filepath.Join(target, "staged.txt"), "destination-local\n", 0o644)

	_, err := PreflightCheckoutFiles(t.Context(), target, []CheckoutFile{{Path: "staged.txt", Kind: "absent", Tracked: true}})
	if ErrorCode(err) != CodeConflict {
		t.Fatalf("absent ignored collision error = %v", err)
	}
}

func TestCheckoutIncarnationRejectsAtomicWorktreeReplacement(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	replacement := filepath.Join(parent, "replacement")
	for _, path := range []string{target, replacement} {
		runCheckoutGit(t, "init", path)
		runCheckoutGit(t, "-C", path, "config", "user.name", "Test")
		runCheckoutGit(t, "-C", path, "config", "user.email", "test@example.com")
		writeCheckoutFile(t, filepath.Join(path, "file.txt"), "base\n", 0o644)
		runCheckoutGit(t, "-C", path, "add", "file.txt")
		runCheckoutGit(t, "-C", path, "commit", "-m", "base")
	}
	incarnation, err := captureCheckoutIncarnation(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	retired := filepath.Join(parent, "retired")
	if err = os.Rename(target, retired); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, target); err != nil {
		t.Fatal(err)
	}
	if err = incarnation.Validate(t.Context(), target); ErrorCode(err) != CodeConflict {
		t.Fatalf("incarnation validation error = %v", err)
	}
}

func TestPreparedCheckoutIndexDoesNotRemoveAnotherGitWritersLockAfterPromotion(t *testing.T) {
	source := checkoutTestRepository(t)
	capture, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Release()
	extracted, err := ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer extracted.Release()
	target := filepath.Join(t.TempDir(), "target")
	applyCheckoutCapture(t, target, capture)
	prepared, err := prepareCheckoutIndex(t.Context(), target, extracted)
	if err != nil {
		t.Fatal(err)
	}
	if err = prepared.Promote(); err != nil {
		prepared.Release()
		t.Fatal(err)
	}
	indexPath := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "--git-path", "index")))
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(target, indexPath)
	}
	foreign := []byte("another Git writer")
	if err = os.WriteFile(indexPath+".lock", foreign, 0o600); err != nil {
		prepared.Release()
		t.Fatal(err)
	}
	prepared.Release()
	contents, err := os.ReadFile(indexPath + ".lock")
	if err != nil || !bytes.Equal(contents, foreign) {
		t.Fatalf("foreign index lock = %q, err=%v", contents, err)
	}
}

func TestPrepareCheckoutFilesFailureLeavesDestinationUnchanged(t *testing.T) {
	target := checkoutTestRepository(t)
	destination, err := ObserveCheckout(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	payloadDirectory := t.TempDir()
	if err = os.MkdirAll(filepath.Join(payloadDirectory, "files"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, filepath.Join(payloadDirectory, "files", "file.txt"), "incoming\n", 0o644)
	payload := ExtractedCheckout{Directory: payloadDirectory, State: CheckoutState{Files: []CheckoutFile{
		{Path: "file.txt", Kind: "file", Size: 9, SHA256: strings.Repeat("a", 64), Tracked: true},
		{Path: "missing.txt", Kind: "file", Size: 1, SHA256: strings.Repeat("b", 64), Tracked: false},
	}}}
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prepareCheckoutFiles(root, destination, payload); err == nil {
		_ = root.Close()
		t.Fatal("prepareCheckoutFiles unexpectedly succeeded")
	}
	if err = root.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(target, "file.txt"))
	if err != nil || string(contents) != "base\n" {
		t.Fatalf("destination file = %q, err=%v", contents, err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".schooner-file-") {
			t.Fatalf("staging file remained after failed preparation: %s", entry.Name())
		}
	}
}

func TestPreparedCheckoutFilesPreserveConcurrentWriteToRetiredInode(t *testing.T) {
	source := checkoutTestRepository(t)
	base, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	applyCheckoutCapture(t, destination, base)
	base.Release()
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, filepath.Join(source, "file.txt"), "incoming\n", 0o644)
	incoming, err := CaptureCheckout(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Release()
	payload, err := ExtractCheckoutPayload(incoming.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Release()
	destinationState, err := ObserveCheckout(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareCheckoutFiles(root, destinationState, payload)
	if err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	retired, err := os.OpenFile(filepath.Join(destination, "file.txt"), os.O_WRONLY, 0)
	if err != nil {
		prepared.Release()
		_ = root.Close()
		t.Fatal(err)
	}
	if err = prepared.Apply(); err != nil {
		_ = retired.Close()
		prepared.Release()
		_ = root.Close()
		t.Fatal(err)
	}
	unique := []byte("concurrent unique edit\n")
	if _, err = retired.WriteAt(unique, 0); err != nil {
		_ = retired.Close()
		prepared.Release()
		_ = root.Close()
		t.Fatal(err)
	}
	if err = retired.Truncate(int64(len(unique))); err != nil {
		_ = retired.Close()
		prepared.Release()
		_ = root.Close()
		t.Fatal(err)
	}
	if err = retired.Close(); err != nil {
		prepared.Release()
		_ = root.Close()
		t.Fatal(err)
	}
	observed, err := ObserveCheckout(t.Context(), destination)
	if err != nil {
		prepared.Release()
		_ = root.Close()
		t.Fatal(err)
	}
	if _, err = prepared.verificationState(observed); ErrorCode(err) != CodeOutcomeUnknown {
		prepared.Release()
		_ = root.Close()
		t.Fatalf("verification error = %v", err)
	}
	if err = prepared.Rollback(); ErrorCode(err) != CodeOutcomeUnknown {
		prepared.Release()
		_ = root.Close()
		t.Fatalf("rollback error = %v", err)
	}
	prepared.Release()
	if err = root.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".schooner-file-") {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(destination, entry.Name()))
		if readErr == nil && bytes.Equal(contents, unique) {
			found = true
		}
	}
	if !found {
		t.Fatal("concurrent unique edit was not preserved in recovery material")
	}
}

func TestExtractCheckoutPayloadRejectsTraversal(t *testing.T) {
	payload := filepath.Join(t.TempDir(), "payload.tar")
	file, err := os.Create(payload)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	if err = writer.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = ExtractCheckoutPayload(payload, t.TempDir()); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("traversal error = %v", err)
	}
}

func TestExtractCheckoutPayloadRejectsMissingIndexedPathState(t *testing.T) {
	source := checkoutTestRepository(t)
	state, err := ObserveCheckout(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	for index, entry := range state.Files {
		if entry.Path == "file.txt" {
			state.Bytes -= entry.Size
			state.Files = append(state.Files[:index], state.Files[index+1:]...)
			break
		}
	}
	state.FileCount = len(state.Files)
	state.AbsentPaths = nil
	state.AbsentCount = 0
	state.Digest, err = checkoutDigest(state)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(checkoutPayloadMetadata{SchemaVersion: CheckoutSchemaVersion, State: state})
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(t.TempDir(), "payload.tar")
	file, err := os.Create(payload)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	if err = writeTarBytes(writer, "metadata.json", 0o600, metadata); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = ExtractCheckoutPayload(payload, t.TempDir()); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("missing indexed path state error = %v", err)
	}
}

func TestValidateImportedCheckoutObjectsRejectsMissingIndexBlobWithoutMutation(t *testing.T) {
	target := checkoutTestRepository(t)
	before, err := ObserveCheckout(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	incoming := before
	incoming.IndexEntries = append(incoming.IndexEntries, CheckoutIndexEntry{Path: "missing.txt", Mode: "100644", Object: strings.Repeat("1", 40)})
	if err = validateImportedCheckoutObjects(t.Context(), target, incoming); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("missing index blob error = %v", err)
	}
	after, err := ObserveCheckout(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if after.Digest != before.Digest {
		t.Fatalf("destination digest changed: %s, want %s", after.Digest, before.Digest)
	}
	incoming = before
	incoming.IndexEntries = append(incoming.IndexEntries, CheckoutIndexEntry{Path: "commit.txt", Mode: "100644", Object: before.HEAD})
	if err = validateImportedCheckoutObjects(t.Context(), target, incoming); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("commit used as index blob error = %v", err)
	}
}

func TestExtractCheckoutPayloadRejectsSymlinkParent(t *testing.T) {
	payload := filepath.Join(t.TempDir(), "payload.tar")
	outside := t.TempDir()
	linkDigest := sha256.Sum256([]byte(outside))
	fileDigest := sha256.Sum256([]byte("x"))
	state := CheckoutState{
		SchemaVersion: CheckoutSchemaVersion,
		FileCount:     2,
		Files: []CheckoutFile{
			{Path: "parent", Kind: "symlink", Size: int64(len(outside)), SHA256: hex.EncodeToString(linkDigest[:]), Tracked: true},
			{Path: "parent/escape", Kind: "file", Size: 1, SHA256: hex.EncodeToString(fileDigest[:]), Tracked: true},
		},
	}
	var err error
	state.Digest, err = checkoutDigest(state)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(checkoutPayloadMetadata{SchemaVersion: CheckoutSchemaVersion, State: state})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(payload)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	if err = writeTarBytes(writer, "metadata.json", 0o600, metadata); err == nil {
		err = writeTarBytes(writer, "objects.pack", 0o600, nil)
	}
	if err == nil {
		err = writer.WriteHeader(&tar.Header{Name: "files/parent", Linkname: outside, Mode: 0o777, Typeflag: tar.TypeSymlink})
	}
	if err == nil {
		err = writeTarBytes(writer, "files/parent/escape", 0o600, []byte("x"))
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ExtractCheckoutPayload(payload, t.TempDir()); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("symlink parent error = %v", err)
	}
	if _, err = os.Lstat(filepath.Join(outside, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("payload escaped staging directory: %v", err)
	}
}

func TestUpdateCheckoutHEADReconcilesCancellationAfterSuccessfulMutation(t *testing.T) {
	tests := []struct {
		name           string
		state          func(oldBranch, newHEAD string) CheckoutState
		prepare        func(t *testing.T, target, oldHEAD string)
		mutation       []string
		assertRestored func(t *testing.T, target, oldBranch, oldHEAD string)
	}{
		{
			name: "detached HEAD",
			state: func(_ string, newHEAD string) CheckoutState {
				return CheckoutState{HEAD: newHEAD, Detached: true}
			},
			mutation: []string{"update-ref", "--no-deref", "HEAD"},
		},
		{
			name: "attached HEAD binding",
			state: func(_ string, newHEAD string) CheckoutState {
				return CheckoutState{HEAD: newHEAD, Branch: "incoming"}
			},
			mutation: []string{"symbolic-ref", "HEAD", "refs/heads/incoming"},
			assertRestored: func(t *testing.T, target, _, _ string) {
				assertCheckoutRefAbsent(t, target, "refs/heads/incoming")
			},
		},
		{
			name: "new attached branch",
			state: func(_ string, newHEAD string) CheckoutState {
				return CheckoutState{HEAD: newHEAD, Branch: "incoming"}
			},
			mutation: []string{"update-ref", "--no-deref", "refs/heads/incoming"},
			assertRestored: func(t *testing.T, target, _, _ string) {
				assertCheckoutRefAbsent(t, target, "refs/heads/incoming")
			},
		},
		{
			name: "existing attached branch",
			state: func(_ string, newHEAD string) CheckoutState {
				return CheckoutState{HEAD: newHEAD, Branch: "incoming"}
			},
			prepare: func(t *testing.T, target, oldHEAD string) {
				runCheckoutGit(t, "-C", target, "branch", "incoming", oldHEAD)
			},
			mutation: []string{"update-ref", "--no-deref", "refs/heads/incoming"},
			assertRestored: func(t *testing.T, target, _, oldHEAD string) {
				if current := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "refs/heads/incoming"))); current != oldHEAD {
					t.Fatalf("restored incoming branch = %s, want %s", current, oldHEAD)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := checkoutTestRepository(t)
			oldBranch := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "symbolic-ref", "--short", "HEAD")))
			oldHEAD := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "HEAD")))
			writeCheckoutFile(t, filepath.Join(target, "file.txt"), "incoming commit\n", 0o644)
			runCheckoutGit(t, "-C", target, "add", "file.txt")
			runCheckoutGit(t, "-C", target, "commit", "-m", "incoming")
			newHEAD := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "HEAD")))
			runCheckoutGit(t, "-C", target, "reset", "--hard", oldHEAD)
			if test.prepare != nil {
				test.prepare(t, target, oldHEAD)
			}

			ctx, cancel := context.WithCancel(t.Context())
			commands := &cancelAfterGitMutationRunner{
				cancel:   cancel,
				delegate: commandRunner{},
				match: func(arguments []string) bool {
					return containsGitArgumentSequence(arguments, test.mutation)
				},
			}
			update, err := updateCheckoutHEADWithRunner(ctx, commands, target, test.state(oldBranch, newHEAD))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("update error = %v, want context cancellation", err)
			}
			if update == nil || !commands.cancelled {
				t.Fatalf("rollback token = %+v, command cancelled = %t", update, commands.cancelled)
			}
			if err = update.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			binding := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "symbolic-ref", "--short", "HEAD")))
			if binding != oldBranch {
				t.Fatalf("restored HEAD binding = %q, want %q", binding, oldBranch)
			}
			if current := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "HEAD"))); current != oldHEAD {
				t.Fatalf("restored HEAD = %s, want %s", current, oldHEAD)
			}
			if test.assertRestored != nil {
				test.assertRestored(t, target, oldBranch, oldHEAD)
			}
		})
	}
}

func TestCheckoutHEADRestoreDoesNotOverwriteIndependentSymbolicBindingAtSameCommit(t *testing.T) {
	target := checkoutTestRepository(t)
	oldHEAD := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "HEAD")))
	runCheckoutGit(t, "-C", target, "checkout", "--detach", oldHEAD)

	update, err := updateCheckoutHEAD(t.Context(), target, CheckoutState{HEAD: oldHEAD, Branch: "incoming"})
	if err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, "-C", target, "branch", "independent", oldHEAD)
	runCheckoutGit(t, "-C", target, "symbolic-ref", "HEAD", "refs/heads/independent")

	if err = update.Restore(t.Context()); ErrorCode(err) != CodeConflict {
		t.Fatalf("restore error = %v, want conflict", err)
	}
	binding := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "symbolic-ref", "--short", "HEAD")))
	if binding != "independent" {
		t.Fatalf("HEAD binding = %q, want independent", binding)
	}
	if current := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "HEAD"))); current != oldHEAD {
		t.Fatalf("HEAD = %s, want %s", current, oldHEAD)
	}
	assertCheckoutRefAbsent(t, target, "refs/heads/incoming")
}

func TestCheckoutHEADUpdateDoesNotDereferenceRacingSymbolicBranch(t *testing.T) {
	target := checkoutTestRepository(t)
	oldBranch := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "symbolic-ref", "--short", "HEAD")))
	oldHEAD := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "HEAD")))
	runCheckoutGit(t, "-C", target, "branch", "incoming", oldHEAD)
	runCheckoutGit(t, "-C", target, "branch", "terminal", oldHEAD)
	writeCheckoutFile(t, filepath.Join(target, "file.txt"), "incoming commit\n", 0o644)
	runCheckoutGit(t, "-C", target, "add", "file.txt")
	runCheckoutGit(t, "-C", target, "commit", "-m", "incoming")
	newHEAD := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "HEAD")))
	runCheckoutGit(t, "-C", target, "reset", "--hard", oldHEAD)

	ctx, cancel := context.WithCancel(t.Context())
	commands := &mutateBeforeGitRunner{
		delegate: commandRunner{},
		match: func(arguments []string) bool {
			return containsGitArgumentSequence(arguments, []string{"update-ref", "--no-deref", "refs/heads/incoming"})
		},
		before: func() {
			runCheckoutGit(t, "-C", target, "symbolic-ref", "refs/heads/incoming", "refs/heads/terminal")
		},
		cancel: cancel,
	}
	update, err := updateCheckoutHEADWithRunner(ctx, commands, target, CheckoutState{HEAD: newHEAD, Branch: "incoming"})
	if !errors.Is(err, context.Canceled) || update == nil || !commands.triggered {
		t.Fatalf("racing branch update = %+v, triggered=%t, err=%v", update, commands.triggered, err)
	}
	if terminal := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "refs/heads/terminal"))); terminal != oldHEAD {
		t.Fatalf("terminal ref was dereferenced and changed to %s, want %s", terminal, oldHEAD)
	}
	if err = update.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if branch := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "symbolic-ref", "--short", "HEAD"))); branch != oldBranch {
		t.Fatalf("restored HEAD branch = %q, want %q", branch, oldBranch)
	}
	if incoming := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "refs/heads/incoming"))); incoming != oldHEAD {
		t.Fatalf("restored incoming ref = %s, want %s", incoming, oldHEAD)
	}
}

func TestInspectCheckoutHEADReturnsDirectSymbolicBinding(t *testing.T) {
	target := checkoutTestRepository(t)
	head := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "rev-parse", "HEAD")))
	directBinding := strings.TrimSpace(string(runCheckoutGit(t, "-C", target, "symbolic-ref", "--no-recurse", "HEAD")))
	runCheckoutGit(t, "-C", target, "branch", "terminal", head)
	runCheckoutGit(t, "-C", target, "symbolic-ref", directBinding, "refs/heads/terminal")

	binding, inspectedHEAD, err := inspectCheckoutHEAD(t.Context(), commandRunner{}, target)
	if err != nil {
		t.Fatal(err)
	}
	if binding != directBinding || inspectedHEAD != head {
		t.Fatalf("HEAD inspection = (%q, %q), want direct binding %s at %s", binding, inspectedHEAD, directBinding, head)
	}
}

func containsGitArgumentSequence(arguments, expected []string) bool {
	for start := 0; start+len(expected) <= len(arguments); start++ {
		matches := true
		for index := range expected {
			if arguments[start+index] != expected[index] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func assertCheckoutRefAbsent(t *testing.T, target, ref string) {
	t.Helper()
	command := exec.Command("git", "-C", target, "show-ref", "--verify", "--quiet", ref)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if err := command.Run(); err == nil {
		t.Fatalf("ref %s still exists", ref)
	} else if exit := process.ExitCode(err); exit != 1 {
		t.Fatalf("inspect ref %s: %v", ref, err)
	}
}

func checkoutTestRepository(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source")
	runCheckoutGit(t, "init", path)
	runCheckoutGit(t, "-C", path, "config", "user.name", "Test")
	runCheckoutGit(t, "-C", path, "config", "user.email", "test@example.com")
	writeCheckoutFile(t, filepath.Join(path, ".gitignore"), "ignored.env\n", 0o644)
	writeCheckoutFile(t, filepath.Join(path, "file.txt"), "base\n", 0o644)
	runCheckoutGit(t, "-C", path, "add", ".")
	runCheckoutGit(t, "-C", path, "commit", "-m", "base")
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func applyCheckoutCapture(t *testing.T, destination string, capture CheckoutCapture) CheckoutState {
	t.Helper()
	extracted, err := ExtractCheckoutPayload(capture.PayloadPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer extracted.Release()
	applied, err := ApplyCheckout(t.Context(), destination, extracted)
	if err != nil {
		t.Fatal(err)
	}
	return applied
}

func runCheckoutGit(t *testing.T, arguments ...string) []byte {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return output
}

func writeCheckoutFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}
