package workspacetransfer

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thewelshrich/schooner/internal/repository"
)

type fakeRemote struct {
	destination    *repository.CheckoutState
	applied        bool
	preflight      error
	preflightFiles []repository.CheckoutFile
}

func (remote *fakeRemote) ObservePushDestination(context.Context, string) (*repository.CheckoutState, error) {
	return remote.destination, nil
}

func (remote *fakeRemote) PreflightPushDestination(_ context.Context, _ string, source repository.CheckoutState, _ bool) (PreflightResult, error) {
	remote.preflightFiles = append(remote.preflightFiles, source.Files...)
	if remote.preflight != nil || remote.destination == nil {
		return PreflightResult{}, remote.preflight
	}
	destination := make(map[string]repository.CheckoutFile, len(remote.destination.Files))
	for _, file := range remote.destination.Files {
		destination[file.Path] = file
	}
	result := PreflightResult{}
	for _, file := range source.Files {
		other, ok := destination[file.Path]
		if !ok {
			continue
		}
		result.ExistingFiles++
		if other.Kind == file.Kind && other.Executable == file.Executable && other.Size == file.Size && other.SHA256 == file.SHA256 {
			result.MatchingFiles++
		}
	}
	return result, nil
}

func TestPushPreflightsIndexedPathMissingFromWorkingTree(t *testing.T) {
	worktree := transferRepository(t)
	if err := os.WriteFile(filepath.Join(worktree, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", worktree, "add", "staged.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, output)
	}
	if err := os.Remove(filepath.Join(worktree, "staged.txt")); err != nil {
		t.Fatal(err)
	}
	source, err := repository.ObserveCheckout(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	destination := source
	destination.Digest = "different"
	destination.Status = repository.Status{}
	remote := &fakeRemote{destination: &destination}
	result, err := Push(t.Context(), PushRequest{LocalWorktree: worktree, RemoteWorktree: "/remote/repo", Staging: t.TempDir(), DryRun: true, Remote: remote})
	if err != nil || result.Action != ActionWouldPush {
		t.Fatalf("dry run = %+v, err=%v", result, err)
	}
	found := false
	for _, entry := range remote.preflightFiles {
		if entry.Path == "staged.txt" && entry.Kind == "absent" && entry.Tracked {
			found = true
		}
	}
	if !found {
		t.Fatalf("preflight files = %+v, want tracked absent path", remote.preflightFiles)
	}
}

func (remote *fakeRemote) ApplyPush(_ context.Context, request ApplyRequest, payload io.Reader) (ApplyResult, error) {
	remote.applied = true
	written, err := io.Copy(io.Discard, payload)
	return ApplyResult{State: request.SourceState, BytesTransferred: written}, err
}

func TestPushDryRunCreateAndApply(t *testing.T) {
	worktree := transferRepository(t)
	remote := &fakeRemote{}
	dryRunStaging := filepath.Join(t.TempDir(), "must-not-be-created")
	result, err := Push(t.Context(), PushRequest{LocalWorktree: worktree, RemoteWorktree: "/remote/repo", Staging: dryRunStaging, DryRun: true, Remote: remote})
	if err != nil || result.Action != ActionWouldPush || !result.Created || remote.applied {
		t.Fatalf("dry run = %+v, applied=%t, err=%v", result, remote.applied, err)
	}
	if _, statErr := os.Lstat(dryRunStaging); !os.IsNotExist(statErr) {
		t.Fatalf("dry run created staging state: %v", statErr)
	}
	result, err = Push(t.Context(), PushRequest{LocalWorktree: worktree, RemoteWorktree: "/remote/repo", Staging: t.TempDir(), Remote: remote})
	if err != nil || result.Action != ActionPushed || !remote.applied || result.BytesTransferred == 0 {
		t.Fatalf("push = %+v, applied=%t, err=%v", result, remote.applied, err)
	}
}

func TestPushProtectsDirtyAndDivergentDestination(t *testing.T) {
	worktree := transferRepository(t)
	source, err := repository.ObserveCheckout(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	dirty := source
	dirty.Digest = "different"
	dirty.Status.Unstaged = 1
	remote := &fakeRemote{destination: &dirty}
	result, err := Push(t.Context(), PushRequest{LocalWorktree: worktree, RemoteWorktree: "/remote/repo", Staging: t.TempDir(), Remote: remote})
	if err == nil || result.Action != ActionConflict || remote.applied {
		t.Fatalf("dirty push = %+v, applied=%t, err=%v", result, remote.applied, err)
	}
	divergent := source
	divergent.Digest = "different"
	divergent.HEAD = "1111111111111111111111111111111111111111"
	divergent.Status = repository.Status{}
	remote.destination = &divergent
	result, err = Push(t.Context(), PushRequest{LocalWorktree: worktree, RemoteWorktree: "/remote/repo", Staging: t.TempDir(), Remote: remote})
	if err == nil || result.Action != ActionConflict || remote.applied {
		t.Fatalf("divergent push = %+v, applied=%t, err=%v", result, remote.applied, err)
	}
}

func TestPushAllowsExactOperationCreatedCloneSeedAheadOfSource(t *testing.T) {
	worktree := transferRepository(t)
	source, err := repository.ObserveCheckout(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	seed := source
	seed.Worktree = "/remote/repo"
	seed.HEAD = "1111111111111111111111111111111111111111"
	seed.Digest = "operation-created-seed"
	seed.RevalidationDigest = "operation-created-seed"
	seed.Status = repository.Status{}
	remote := &fakeRemote{destination: &seed}

	result, err := Push(t.Context(), PushRequest{
		LocalWorktree: worktree, RemoteWorktree: seed.Worktree, Staging: t.TempDir(),
		Remote: remote, CreatedDestination: &seed,
	})
	if err != nil || result.Action != ActionPushed || !result.Created || !remote.applied {
		t.Fatalf("operation-created seed push = %+v, applied=%t, err=%v", result, remote.applied, err)
	}
}

func TestPushRejectsOperationCreatedCloneSeedThatChanged(t *testing.T) {
	worktree := transferRepository(t)
	seed, err := repository.ObserveCheckout(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	seed.Worktree = "/remote/repo"
	seed.Digest = "operation-created-seed"
	seed.RevalidationDigest = "operation-created-seed"
	destination := seed
	destination.Digest = "changed-after-clone"
	destination.RevalidationDigest = "changed-after-clone"
	remote := &fakeRemote{destination: &destination}

	result, err := Push(t.Context(), PushRequest{
		LocalWorktree: worktree, RemoteWorktree: seed.Worktree, Staging: t.TempDir(),
		Remote: remote, CreatedDestination: &seed,
	})
	if err == nil || result.Action != ActionConflict || remote.applied {
		t.Fatalf("changed operation-created seed push = %+v, applied=%t, err=%v", result, remote.applied, err)
	}
}

func TestPushIdenticalDirtyStateIsNoChange(t *testing.T) {
	worktree := transferRepository(t)
	if err := os.WriteFile(filepath.Join(worktree, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := repository.ObserveCheckout(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{destination: &state}
	result, err := Push(t.Context(), PushRequest{LocalWorktree: worktree, RemoteWorktree: "/remote/repo", Staging: t.TempDir(), Remote: remote})
	if err != nil || result.Action != ActionNoChange || remote.applied {
		t.Fatalf("identical push = %+v, applied=%t, err=%v", result, remote.applied, err)
	}
}

func TestPushRejectsDifferentRepositoryIdentityBeforeIdenticalNoOp(t *testing.T) {
	worktree := transferRepository(t)
	state, err := repository.ObserveCheckout(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	state.RepositoryIdentity = "example.com/other/repo"
	remote := &fakeRemote{destination: &state}
	result, err := Push(t.Context(), PushRequest{LocalWorktree: worktree, RemoteWorktree: "/remote/repo", Staging: t.TempDir(), Remote: remote})
	if err == nil || result.Action != ActionConflict || remote.applied {
		t.Fatalf("identity conflict = %+v, applied=%t, err=%v", result, remote.applied, err)
	}
}

func TestPushDryRunReportsRemoteFilesystemPreflightConflict(t *testing.T) {
	worktree := transferRepository(t)
	state, err := repository.ObserveCheckout(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	state.Digest = "different"
	remote := &fakeRemote{destination: &state, preflight: &repository.Error{Code: repository.CodeConflict, Message: "ignored destination path collides"}}
	result, err := Push(t.Context(), PushRequest{LocalWorktree: worktree, RemoteWorktree: "/remote/repo", Staging: t.TempDir(), DryRun: true, Remote: remote})
	if err == nil || result.Action != ActionConflict || remote.applied {
		t.Fatalf("dry-run preflight = %+v, applied=%t, err=%v", result, remote.applied, err)
	}
}

func TestPushCountsExecutableModeChange(t *testing.T) {
	worktree := transferRepository(t)
	state, err := repository.ObserveCheckout(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	destination := state
	destination.Digest = "different"
	destination.Files = append([]repository.CheckoutFile(nil), state.Files...)
	destination.Files[0].Executable = !destination.Files[0].Executable
	remote := &fakeRemote{destination: &destination}
	result, err := Push(t.Context(), PushRequest{LocalWorktree: worktree, RemoteWorktree: "/remote/repo", Staging: filepath.Join(t.TempDir(), "unused"), DryRun: true, Remote: remote})
	if err != nil || result.Action != ActionWouldPush || result.FilesChanged != 1 {
		t.Fatalf("mode-only dry run = %+v, err=%v", result, err)
	}
}

func transferRepository(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repo")
	for _, arguments := range [][]string{{"init", path}, {"-C", path, "config", "user.name", "Test"}, {"-C", path, "config", "user.email", "test@example.com"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"-C", path, "add", "file.txt"}, {"-C", path, "commit", "-m", "base"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	if output, err := exec.Command("git", "-C", path, "remote", "add", "origin", "https://example.com/owner/repo.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, output)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
