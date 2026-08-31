package workspacetransfer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/thewelshrich/schooner/internal/repository"
)

type fakePullSource struct {
	worktree      string
	inspection    PullInspection
	inspectErr    error
	captureErr    error
	captured      bool
	manifestAsked bool
	beforeCapture func()
}

func (source *fakePullSource) InspectPullSource(_ context.Context, request PullInspectRequest) (PullInspection, error) {
	source.manifestAsked = request.IncludeManifest
	return source.inspection, source.inspectErr
}

func (source *fakePullSource) CapturePullSource(ctx context.Context, request PullCaptureRequest) (PullCapture, error) {
	source.captured = true
	if source.beforeCapture != nil {
		source.beforeCapture()
	}
	if source.captureErr != nil {
		return PullCapture{}, source.captureErr
	}
	capture, err := repository.CaptureCheckout(ctx, source.worktree, request.Staging)
	return PullCapture{Capture: capture, DestinationAncestor: source.inspection.DestinationAncestor}, err
}

func TestPullAppliesRemoteCommittedAndDirtyState(t *testing.T) {
	local, remote := pullRepositories(t)
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("remote commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, remote, "add", "file.txt")
	gitRun(t, remote, "commit", "-m", "remote")
	if err := os.WriteFile(filepath.Join(remote, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "mixed.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, remote, "add", "mixed.txt")
	if err := os.WriteFile(filepath.Join(remote, "mixed.txt"), []byte("unstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "binary.bin"), []byte{0, 1, 2, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.txt", filepath.Join(remote, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(remote, "file.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	state, err := repository.ObserveCheckout(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakePullSource{worktree: remote, inspection: PullInspection{State: state, DestinationAncestor: true}}
	result, err := Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: source})
	if err != nil || result.Action != ActionPulled || !source.captured || result.BytesTransferred == 0 {
		t.Fatalf("pull = %+v, captured=%t, err=%v", result, source.captured, err)
	}
	localState, err := repository.ObserveCheckout(t.Context(), local)
	if err != nil || localState.Digest != state.Digest {
		t.Fatalf("local = %+v, remote = %+v, err=%v", localState, state, err)
	}
}

func TestPullProtectsDirtyAheadAndDifferentRepositoryDestinations(t *testing.T) {
	local, remote := pullRepositories(t)
	remoteState, err := repository.ObserveCheckout(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	remoteState.Digest = "different"
	remoteState.RevalidationDigest = "different"

	if err = os.WriteFile(filepath.Join(local, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := &fakePullSource{inspection: PullInspection{State: remoteState, DestinationAncestor: true}}
	result, err := Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: source})
	if err == nil || result.Action != ActionConflict || source.captured {
		t.Fatalf("dirty pull = %+v, captured=%t, err=%v", result, source.captured, err)
	}
	if removeErr := os.Remove(filepath.Join(local, "local.txt")); removeErr != nil {
		t.Fatal(removeErr)
	}

	source.inspection.DestinationAncestor = false
	result, err = Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: source})
	if err == nil || result.Action != ActionConflict || source.captured {
		t.Fatalf("ahead pull = %+v, captured=%t, err=%v", result, source.captured, err)
	}

	source.inspection.DestinationAncestor = true
	source.inspection.State.RepositoryIdentity = "example.com/other/repo"
	result, err = Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: source})
	if err == nil || result.Action != ActionConflict || source.captured {
		t.Fatalf("identity pull = %+v, captured=%t, err=%v", result, source.captured, err)
	}
}

func TestPullDryRunUsesManifestWithoutCreatingStaging(t *testing.T) {
	local, remote := pullRepositories(t)
	if err := os.WriteFile(filepath.Join(remote, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := repository.ObserveCheckout(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(t.TempDir(), "must-not-exist")
	source := &fakePullSource{inspection: PullInspection{State: state, DestinationAncestor: true}}
	result, err := Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: staging, DryRun: true, Source: source})
	if err != nil || result.Action != ActionWouldPull || result.FilesChanged != 1 || !source.manifestAsked || source.captured {
		t.Fatalf("dry run = %+v, source=%+v, err=%v", result, source, err)
	}
	if _, statErr := os.Lstat(staging); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("dry run staging = %v", statErr)
	}
}

func TestPullDryRunRejectsBranchOwnedByAnotherLocalWorktree(t *testing.T) {
	local, remote := pullRepositories(t)
	gitRun(t, remote, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(remote, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, remote, "add", "feature.txt")
	gitRun(t, remote, "commit", "-m", "feature")
	gitRun(t, local, "fetch", remote, "feature:feature")
	other := filepath.Join(t.TempDir(), "other")
	gitRun(t, local, "worktree", "add", other, "feature")
	state, err := repository.ObserveCheckout(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakePullSource{inspection: PullInspection{State: state, DestinationAncestor: true}}
	result, err := Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: filepath.Join(t.TempDir(), "unused"), DryRun: true, Source: source})
	if err == nil || result.Action != ActionConflict || source.captured {
		t.Fatalf("branch-owned dry run = %+v, captured=%t, err=%v", result, source.captured, err)
	}
}

func TestPullIdenticalDirtyStateIsNoChange(t *testing.T) {
	local, _ := pullRepositories(t)
	if err := os.WriteFile(filepath.Join(local, "dirty.txt"), []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := repository.ObserveCheckout(t.Context(), local)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakePullSource{inspection: PullInspection{State: state, DestinationAncestor: true}}
	result, err := Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: "/remote/repo", Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: source})
	if err != nil || result.Action != ActionNoChange || source.captured {
		t.Fatalf("no-op pull = %+v, captured=%t, err=%v", result, source.captured, err)
	}
}

func TestPullRejectsDestinationChangeAfterPreflight(t *testing.T) {
	local, remote := pullRepositories(t)
	if err := os.WriteFile(filepath.Join(remote, "remote.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := repository.ObserveCheckout(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakePullSource{worktree: remote, inspection: PullInspection{State: state, DestinationAncestor: true}}
	source.beforeCapture = func() {
		if writeErr := os.WriteFile(filepath.Join(local, "file.txt"), []byte("late local edit\n"), 0o644); writeErr != nil {
			t.Error(writeErr)
		}
	}
	result, err := Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: source})
	if err == nil || result.Action == ActionPulled {
		t.Fatalf("late-change pull = %+v, err=%v", result, err)
	}
	contents, readErr := os.ReadFile(filepath.Join(local, "file.txt"))
	if readErr != nil || string(contents) != "late local edit\n" {
		t.Fatalf("late local edit = %q, %v", contents, readErr)
	}
}

func TestPullRejectsSourceChangeDuringCapture(t *testing.T) {
	local, remote := pullRepositories(t)
	if err := os.WriteFile(filepath.Join(remote, "remote.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := repository.ObserveCheckout(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakePullSource{worktree: remote, inspection: PullInspection{State: state, DestinationAncestor: true}}
	source.beforeCapture = func() {
		if writeErr := os.WriteFile(filepath.Join(remote, "remote.txt"), []byte("after\n"), 0o644); writeErr != nil {
			t.Error(writeErr)
		}
	}
	result, err := Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: source})
	if err == nil || result.Action != ActionConflict {
		t.Fatalf("source-change pull = %+v, err=%v", result, err)
	}
	contents, readErr := os.ReadFile(filepath.Join(local, "file.txt"))
	if readErr != nil || string(contents) != "base\n" {
		t.Fatalf("local file = %q, %v", contents, readErr)
	}
}

func TestPullPreservesDetachedHeadAndTrackedDeletion(t *testing.T) {
	local, remote := pullRepositories(t)
	gitRun(t, remote, "rm", "file.txt")
	gitRun(t, remote, "commit", "-m", "delete tracked file")
	gitRun(t, remote, "checkout", "--detach")
	state, err := repository.ObserveCheckout(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakePullSource{worktree: remote, inspection: PullInspection{State: state, DestinationAncestor: true}}
	result, err := Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: source})
	if err != nil || result.Action != ActionPulled {
		t.Fatalf("detached pull = %+v, err=%v", result, err)
	}
	if _, statErr := os.Lstat(filepath.Join(local, "file.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("tracked deletion = %v", statErr)
	}
	localState, observeErr := repository.ObserveCheckout(t.Context(), local)
	if observeErr != nil || !localState.Detached || localState.Branch != "" || localState.Digest != state.Digest {
		t.Fatalf("local state = %+v, remote = %+v, err=%v", localState, state, observeErr)
	}
}

func TestPullNormalizesInsufficientSpaceBeforeMutation(t *testing.T) {
	local, remote := pullRepositories(t)
	if err := os.WriteFile(filepath.Join(remote, "remote.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := repository.ObserveCheckout(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakePullSource{
		inspection: PullInspection{State: state, DestinationAncestor: true},
		captureErr: &os.PathError{Op: "write", Path: "/staging/payload", Err: syscall.ENOSPC},
	}
	result, err := Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: source})
	var transferError *Error
	if !errors.As(err, &transferError) || transferError.Code != CodeInsufficientSpace || result.Action == ActionPulled {
		t.Fatalf("insufficient-space pull = %+v, error=%v", result, err)
	}
}

func TestPullPreservesIgnoredFilesAndRejectsIncomingCollision(t *testing.T) {
	local, remote := pullRepositories(t)
	if err := os.WriteFile(filepath.Join(local, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, local, "add", ".gitignore")
	gitRun(t, local, "commit", "-m", "ignore env")
	gitRun(t, remote, "fetch", local, "HEAD")
	gitRun(t, remote, "reset", "--hard", "FETCH_HEAD")
	if err := os.WriteFile(filepath.Join(local, ".env"), []byte("local secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "remote.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, remote, "add", "remote.txt")
	gitRun(t, remote, "commit", "-m", "remote")
	state, err := repository.ObserveCheckout(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakePullSource{worktree: remote, inspection: PullInspection{State: state, DestinationAncestor: true}}
	result, err := Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: source})
	if err != nil || result.Action != ActionPulled {
		t.Fatalf("ignored preservation pull = %+v, %v", result, err)
	}
	contents, err := os.ReadFile(filepath.Join(local, ".env"))
	if err != nil || string(contents) != "local secret\n" {
		t.Fatalf("ignored file = %q, %v", contents, err)
	}

	if err = os.WriteFile(filepath.Join(remote, ".env"), []byte("remote tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, remote, "add", "-f", ".env")
	gitRun(t, remote, "commit", "-m", "track env")
	state, err = repository.ObserveCheckout(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	source.inspection = PullInspection{State: state, DestinationAncestor: true}
	result, err = Pull(t.Context(), PullRequest{LocalWorktree: local, RemoteWorktree: remote, Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: source})
	if err == nil || result.Action != ActionConflict {
		t.Fatalf("ignored collision pull = %+v, %v", result, err)
	}
	contents, err = os.ReadFile(filepath.Join(local, ".env"))
	if err != nil || string(contents) != "local secret\n" {
		t.Fatalf("ignored collision changed file = %q, %v", contents, err)
	}
}

func pullRepositories(t *testing.T) (string, string) {
	t.Helper()
	local := transferRepository(t)
	remote := filepath.Join(t.TempDir(), "remote")
	if output, err := exec.Command("git", "clone", local, remote).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, output)
	}
	gitRun(t, remote, "config", "user.name", "Test")
	gitRun(t, remote, "config", "user.email", "test@example.com")
	gitRun(t, remote, "remote", "set-url", "origin", "https://example.com/owner/repo.git")
	canonical, err := filepath.EvalSymlinks(remote)
	if err != nil {
		t.Fatal(err)
	}
	return local, canonical
}

func gitRun(t *testing.T, worktree string, arguments ...string) {
	t.Helper()
	args := append([]string{"-C", worktree}, arguments...)
	if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}
