package repository

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/process"
)

type lifecycleWorktreeUse struct {
	sessions []string
	err      error
}

type lifecycleRunnerFunc func(context.Context, string, ...string) (process.Result, error)

func (f lifecycleRunnerFunc) Run(ctx context.Context, name string, args ...string) (process.Result, error) {
	return f(ctx, name, args...)
}

func (f *lifecycleWorktreeUse) ManagedSessions(context.Context, string) ([]string, error) {
	return f.sessions, f.err
}

func TestLifecycleCloneAddRemoveAndRecover(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worktrees")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	source := createLifecycleSource(t)
	usage := &lifecycleWorktreeUse{}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), usage)
	if err != nil {
		t.Fatal(err)
	}

	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if cloned.Action != "clone" || cloned.Recovered || cloned.Inspection == nil || cloned.Inspection.Worktree.Kind != Primary {
		t.Fatalf("clone = %+v", cloned)
	}
	recovered, err := lifecycle.Clone(t.Context(), CloneRequest{Source: source})
	if err != nil || !recovered.Recovered || recovered.Path != cloned.Path {
		t.Fatalf("recovered clone = %+v, %v", recovered, err)
	}

	runLifecycleGit(t, cloned.Path, "branch", "feature")
	linked, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "feature", Branch: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	if linked.Inspection == nil || linked.Inspection.Worktree.Kind != Linked || linked.Inspection.Worktree.Branch != "feature" {
		t.Fatalf("linked = %+v", linked)
	}

	usage.sessions = []string{"session-1"}
	if _, err = lifecycle.Remove(t.Context(), linked.Path); ErrorCode(err) != CodeConflict {
		t.Fatalf("active Session removal error = %v", err)
	}
	usage.sessions = nil
	if err = os.WriteFile(filepath.Join(linked.Path, "dirty"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Remove(t.Context(), linked.Path); ErrorCode(err) != CodeConflict {
		t.Fatalf("dirty removal error = %v", err)
	}
	if err = os.Remove(filepath.Join(linked.Path, "dirty")); err != nil {
		t.Fatal(err)
	}
	removed, err := lifecycle.Remove(t.Context(), linked.Path)
	if err != nil || removed.Action != "worktree_remove" {
		t.Fatalf("removed = %+v, %v", removed, err)
	}
	removedAgain, err := lifecycle.Remove(t.Context(), linked.Path)
	if err != nil || !removedAgain.Recovered {
		t.Fatalf("recovered removal = %+v, %v", removedAgain, err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "pending")
	pending, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "pending", Branch: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	removeIntent := fingerprint("worktree_remove", pending.Path)
	if err = lifecycle.save(&operationRecord{SchemaVersion: operationSchemaVersion, ID: removeIntent[:24], Kind: "worktree_remove", IntentSHA256: removeIntent, TargetPath: pending.Path, CommonDirectory: pending.Inspection.Repository.CommonDirectory, Checkpoint: "remove_pending"}); err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "worktree", "remove", "--", pending.Path)
	if recoveredRemove, recoverErr := lifecycle.Remove(t.Context(), pending.Path); recoverErr != nil || !recoveredRemove.Recovered {
		t.Fatalf("pending removal recovery = %+v, %v", recoveredRemove, recoverErr)
	}
	automatic, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "automatic"})
	if err != nil || automatic.Inspection == nil || automatic.Inspection.Worktree.Branch != "automatic" {
		t.Fatalf("automatic branch Worktree = %+v, %v", automatic, err)
	}
	if _, err = lifecycle.Remove(t.Context(), automatic.Path); err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Remove(t.Context(), cloned.Path); ErrorCode(err) != CodeConflict {
		t.Fatalf("primary removal error = %v", err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "stale")
	stalePath := filepath.Join(lifecycle.root, "stale")
	runLifecycleGit(t, cloned.Path, "worktree", "add", stalePath, "stale")
	if err = os.RemoveAll(stalePath); err != nil {
		t.Fatal(err)
	}
	pruned, err := lifecycle.Prune(t.Context())
	if err != nil || pruned.RepositoriesChecked != 1 {
		t.Fatalf("prune = %+v, %v", pruned, err)
	}
	output := lifecycleGitOutput(t, cloned.Path, "worktree", "list", "--porcelain")
	if strings.Contains(output, stalePath) {
		t.Fatalf("stale Worktree registration remained: %s", output)
	}
}

func TestLifecycleRejectsConcurrentTargetMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	source := createLifecycleSource(t)
	lifecycle, err := NewLifecycle(root, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(lifecycle.root, "source")
	if err = os.MkdirAll(filepath.Join(state, "locks"), 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(state, "locks", fingerprint(target)+".lock")
	readyPath := lockPath + ".ready"
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=TestLifecycleLockHelper")
	command.Env = append(os.Environ(), "SCHOONER_TEST_LOCK="+lockPath, "SCHOONER_TEST_LOCK_READY="+readyPath)
	command.Stdin = reader
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	defer func() {
		_ = writer.Close()
		_ = command.Wait()
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, statErr := os.Stat(readyPath); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = lifecycle.Clone(t.Context(), CloneRequest{Source: source}); ErrorCode(err) != CodeOperationInProgress {
		t.Fatalf("concurrent mutation error = %v", err)
	}
}

func TestLifecycleCloneRecoveryRejectsDestinationCreatedBeforePromotion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	source := createLifecycleSource(t)
	lifecycle, err := NewLifecycle(root, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	root = lifecycle.root
	target := filepath.Join(root, "source")
	intent := fingerprint("clone", target, source, "")
	record := operationRecord{
		SchemaVersion:  operationSchemaVersion,
		ID:             intent[:24],
		Kind:           "clone",
		IntentSHA256:   intent,
		TargetPath:     target,
		StagingPath:    filepath.Join(root, ".schooner-stage-"+intent[:24], "source"),
		Checkpoint:     "clone_pending",
		OwnershipToken: strings.Repeat("a", 64),
	}
	if err = lifecycle.createOwnedStage(&record); err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, root, "clone", "--", source, record.StagingPath)
	staged, err := Inspect(t.Context(), root, record.StagingPath)
	if err != nil {
		t.Fatal(err)
	}
	recordSnapshot(&record, staged)
	record.Checkpoint = "promote_pending"
	if err = lifecycle.save(&record); err != nil {
		t.Fatal(err)
	}
	otherSource := createLifecycleSource(t)
	runLifecycleGit(t, root, "clone", "--", otherSource, target)

	if _, err = lifecycle.Clone(t.Context(), CloneRequest{Source: source}); ErrorCode(err) != CodeConflict {
		t.Fatalf("collision recovery error = %v", err)
	}
	if _, err = os.Stat(record.StagingPath); err != nil {
		t.Fatalf("owned staging clone was changed: %v", err)
	}
}

func TestLifecycleRemovalRecoveryRejectsStaleGitRegistration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	source := createLifecycleSource(t)
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "stale-removal")
	linked, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "stale-removal", Branch: "stale-removal"})
	if err != nil {
		t.Fatal(err)
	}
	intent := fingerprint("worktree_remove", linked.Path)
	if err = lifecycle.save(&operationRecord{SchemaVersion: operationSchemaVersion, ID: intent[:24], Kind: "worktree_remove", IntentSHA256: intent, TargetPath: linked.Path, CommonDirectory: linked.Inspection.Repository.CommonDirectory, Checkpoint: "remove_pending"}); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(linked.Path); err != nil {
		t.Fatal(err)
	}

	if _, err = lifecycle.Remove(t.Context(), linked.Path); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("stale registration recovery error = %v", err)
	}
	if output := lifecycleGitOutput(t, cloned.Path, "worktree", "list", "--porcelain"); !strings.Contains(output, linked.Path) {
		t.Fatalf("test setup did not retain stale registration: %s", output)
	}
}

func TestRemoveOwnedStageRequiresMatchingOwnershipMarker(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, ".schooner-stage-operation")
	staging := filepath.Join(parent, "repo")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(parent, "keep")
	if err := os.WriteFile(sentinel, []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := operationRecord{ID: "operation", StagingPath: staging, OwnershipToken: strings.Repeat("b", 64)}

	if err := removeOwnedStage(&record); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("unowned cleanup error = %v", err)
	}
	if contents, err := os.ReadFile(sentinel); err != nil || string(contents) != "user data" {
		t.Fatalf("unowned staging contents changed: %q, %v", contents, err)
	}
}

func TestLifecycleCloneAcceptsTagSelector(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	source := createLifecycleSource(t)
	runLifecycleGit(t, source, "tag", "v1.0.0", "main")
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.Clone(t.Context(), CloneRequest{Source: source, Branch: "v1.0.0"})
	if err != nil || result.Inspection == nil || !result.Inspection.Worktree.Detached {
		t.Fatalf("tag clone = %+v, %v", result, err)
	}
}

func TestLifecycleClassifiesBoundedGitFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		result process.Result
		want   Code
	}{
		{name: "authentication", result: process.Result{Stderr: []byte("fatal: Authentication failed")}, want: CodeAuthentication},
		{name: "permission", result: process.Result{Stderr: []byte("fatal: Permission denied")}, want: CodePermissionDenied},
		{name: "truncated", result: process.Result{Stderr: []byte("unknown"), Truncated: true}, want: CodeOutcomeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle.commands = lifecycleRunnerFunc(func(context.Context, string, ...string) (process.Result, error) {
				return test.result, errors.New("exit")
			})
			if _, runErr := lifecycle.runGit(t.Context(), "status"); ErrorCode(runErr) != test.want {
				t.Fatalf("error = %v, code = %s", runErr, ErrorCode(runErr))
			}
		})
	}
}

func TestLifecycleLockHelper(t *testing.T) {
	lockPath := os.Getenv("SCHOONER_TEST_LOCK")
	if lockPath == "" {
		return
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		os.Exit(2)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		os.Exit(3)
	}
	if err = os.WriteFile(os.Getenv("SCHOONER_TEST_LOCK_READY"), []byte("ready"), 0o600); err != nil {
		os.Exit(4)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestLifecycleRejectsUnsafeCloneSourcesAndDestinations(t *testing.T) {
	for _, source := range []string{"-upload-pack=evil", "https://token@example.com/repo.git", "https://user:secret@example.com/repo.git", "https://example.com/repo.git?token=secret", "ssh://user:secret@example.com/repo.git"} {
		if _, _, err := validateCloneSource(source); ErrorCode(err) != CodeInvalidInput {
			t.Errorf("source %q error = %v", source, err)
		}
	}
	for _, source := range []string{"git@example.com:owner/repo.git", "ssh://git@example.com/owner/repo.git", "../local/repo.git"} {
		if _, name, err := validateCloneSource(source); err != nil || name != "repo" {
			t.Errorf("source %q name=%q error=%v", source, name, err)
		}
	}
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.newPath("../escape"); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("traversal error = %v", err)
	}
	outside := t.TempDir()
	if err = os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.newPath("link/repo"); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("symlink error = %v", err)
	}
	source := createLifecycleSource(t)
	if err = os.Mkdir(filepath.Join(lifecycle.root, "source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Clone(t.Context(), CloneRequest{Source: source}); ErrorCode(err) != CodeConflict {
		t.Fatalf("destination collision error = %v", err)
	}
	if err = os.Remove(filepath.Join(lifecycle.root, "source")); err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Clone(t.Context(), CloneRequest{Source: source, Branch: "main"}); ErrorCode(err) != CodeConflict {
		t.Fatalf("different interrupted intent error = %v", err)
	}
}

func TestLifecycleOperationCheckpointDoesNotPersistSource(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	source := createLifecycleSource(t)
	lifecycle, err := NewLifecycle(root, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Clone(t.Context(), CloneRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	stateInfo, err := os.Stat(state)
	if err != nil {
		t.Fatal(err)
	}
	if stateInfo.Mode().Perm() != 0o700 {
		t.Fatalf("state mode = %v", stateInfo.Mode().Perm())
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(state, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(contents), source) {
			t.Fatalf("checkpoint persisted clone source: %s", contents)
		}
		info, statErr := entry.Info()
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("checkpoint mode = %v", info.Mode().Perm())
		}
	}
}

func createLifecycleSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source.git")
	seed := filepath.Join(root, "seed")
	runLifecycleGit(t, root, "init", "--bare", source)
	runLifecycleGit(t, root, "clone", source, seed)
	runLifecycleGit(t, seed, "config", "user.name", "Schooner Test")
	runLifecycleGit(t, seed, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, seed, "add", "README.md")
	runLifecycleGit(t, seed, "commit", "-m", "initial")
	runLifecycleGit(t, seed, "branch", "-M", "main")
	runLifecycleGit(t, seed, "push", "origin", "main")
	runLifecycleGit(t, source, "symbolic-ref", "HEAD", "refs/heads/main")
	return source
}

func runLifecycleGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	args := append([]string{"-C", directory}, arguments...)
	command := exec.Command("git", args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func lifecycleGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return string(output)
}
