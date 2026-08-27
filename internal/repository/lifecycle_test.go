package repository

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/process"
	"github.com/thewelshrich/schooner/internal/source"
)

type lifecycleWorktreeUse struct {
	sessions []string
	err      error
}

func TestOperationJournalsPreserveXDGWhileWorktreeLocksUseStableHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/custom/state")
	journal, err := OperationStateDirectory("/home/alice")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/custom/state/schooner/operations/git"; journal != want {
		t.Fatalf("journal directory = %q, want %q", journal, want)
	}
	locks, err := WorktreeLockStateDirectory("/home/alice")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/home/alice/.local/state/schooner/operations/git"; locks != want {
		t.Fatalf("lock directory = %q, want %q", locks, want)
	}
}

type lifecycleRunnerFunc func(context.Context, string, ...string) (process.Result, error)

type lifecycleWorktreeUseFunc func(context.Context, string) ([]string, error)

type lifecycleCloneExecutor struct {
	localSource string
	fail        bool
	requests    []source.CloneExecution
}

func (executor *lifecycleCloneExecutor) Clone(ctx context.Context, request source.CloneExecution, prepare source.PrepareCloneAttempt) error {
	executor.requests = append(executor.requests, request)
	if err := prepare(); err != nil {
		return err
	}
	if executor.fail {
		return &source.Error{Code: "authentication_required", Message: "GitHub repository authentication is required", Context: map[string]string{"reason": "credentials_missing"}}
	}
	candidate := (&url.URL{Scheme: "file", Path: executor.localSource}).String()
	arguments := []string{"-c", "url." + candidate + ".insteadOf=" + request.SuppliedOrigin, "-C", request.WorktreeRoot, "clone", "-c", "remote.origin.url=" + request.SuppliedOrigin}
	if request.Branch != "" {
		arguments = append(arguments, "--branch", request.Branch)
	}
	arguments = append(arguments, "--", request.SuppliedOrigin, request.Destination)
	if _, err := (osMutationRunner{}).Run(ctx, "git", arguments...); err != nil {
		return source.NewError("conflict", "test clone failed", err)
	}
	return nil
}

func (f lifecycleRunnerFunc) Run(ctx context.Context, name string, args ...string) (process.Result, error) {
	return f(ctx, name, args...)
}

func (f lifecycleWorktreeUseFunc) ManagedSessions(ctx context.Context, path string) ([]string, error) {
	return f(ctx, path)
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

func TestLifecycleCloneV2RecoversByRepositoryIdentityAndPreservesFirstOrigin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worktrees")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	executor := &lifecycleCloneExecutor{localSource: createLifecycleSource(t), fail: true}
	lifecycle, err := NewLifecycleWithOptions(root, state, nil, LifecycleOptions{CloneExecutor: executor})
	if err != nil {
		t.Fatal(err)
	}
	firstOrigin := "https://github.com/Owner/Repo.git"
	if _, err = lifecycle.CloneV2(t.Context(), CloneRequest{Source: firstOrigin}); ErrorCode(err) != CodeAuthentication {
		t.Fatalf("first clone error = %v", err)
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint []byte
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			checkpoint, err = os.ReadFile(filepath.Join(state, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if !strings.Contains(string(checkpoint), firstOrigin) {
		t.Fatalf("checkpoint did not preserve first supplied origin: %s", checkpoint)
	}
	executor.fail = false
	secondOrigin := "git@github.com:owner/repo.git"
	cloned, err := lifecycle.CloneV2(t.Context(), CloneRequest{Source: secondOrigin})
	if err != nil || cloned.Inspection == nil || cloned.Path != filepath.Join(lifecycle.root, "repo") {
		t.Fatalf("recovered clone = %+v, %v", cloned, err)
	}
	if len(executor.requests) != 2 || executor.requests[1].SuppliedOrigin != firstOrigin {
		t.Fatalf("clone requests = %+v", executor.requests)
	}
	if storedOrigin := strings.TrimSpace(lifecycleGitOutput(t, cloned.Path, "config", "--get-all", "remote.origin.url")); storedOrigin != firstOrigin {
		t.Fatalf("stored origin = %q, want %q", storedOrigin, firstOrigin)
	}
	recovered, err := lifecycle.CloneV2(t.Context(), CloneRequest{Source: "ssh://git@github.com/OWNER/REPO.git"})
	if err != nil || !recovered.Recovered || len(executor.requests) != 2 {
		t.Fatalf("completed recovery = %+v, requests = %d, err = %v", recovered, len(executor.requests), err)
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

func TestLifecycleCloneRecoveryRejectsUnconfirmedStagedClone(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	source := createLifecycleSource(t)
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
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
		OwnershipToken: strings.Repeat("c", 64),
	}
	if err = lifecycle.createOwnedStage(&record); err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, root, "clone", "--", source, record.StagingPath)
	if err = lifecycle.save(&record); err != nil {
		t.Fatal(err)
	}

	if _, err = lifecycle.Clone(t.Context(), CloneRequest{Source: source}); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("unconfirmed staged clone recovery error = %v", err)
	}
	if _, err = os.Stat(record.StagingPath); err != nil {
		t.Fatalf("unconfirmed staged clone was changed: %v", err)
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

func TestLifecycleAddRecoversDetachedTag(t *testing.T) {
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
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	added, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "tagged", Branch: "v1.0.0"})
	if err != nil || added.Inspection == nil || !added.Inspection.Worktree.Detached {
		t.Fatalf("detached add = %+v, %v", added, err)
	}
	recovered, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "tagged", Branch: "v1.0.0"})
	if err != nil || !recovered.Recovered || recovered.Path != added.Path {
		t.Fatalf("detached add recovery = %+v, %v", recovered, err)
	}
}

func TestLifecycleAddRecoveryRejectsUnconfirmedStagedWorktree(t *testing.T) {
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
	runLifecycleGit(t, cloned.Path, "branch", "unconfirmed")
	target := filepath.Join(lifecycle.root, "unconfirmed")
	intent := fingerprint("worktree_add", target, cloned.Inspection.Repository.CommonDirectory, "unconfirmed")
	record := operationRecord{
		SchemaVersion:  operationSchemaVersion,
		ID:             intent[:24],
		Kind:           "worktree_add",
		IntentSHA256:   intent,
		TargetPath:     target,
		StagingPath:    filepath.Join(lifecycle.root, ".schooner-stage-"+intent[:24], "unconfirmed"),
		Checkpoint:     "add_pending",
		OwnershipToken: strings.Repeat("d", 64),
	}
	if err = lifecycle.createOwnedStage(&record); err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "worktree", "add", "--lock", record.StagingPath, "unconfirmed")
	if err = lifecycle.save(&record); err != nil {
		t.Fatal(err)
	}

	if _, err = lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "unconfirmed", Branch: "unconfirmed"}); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("unconfirmed staged add recovery error = %v", err)
	}
	if _, err = os.Stat(record.StagingPath); err != nil {
		t.Fatalf("unconfirmed staged Worktree was changed: %v", err)
	}
}

func TestLifecycleAddRejectsUndiscoverableDestinations(t *testing.T) {
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
	for name, path := range map[string]string{
		"nested": filepath.Join("source", "nested"),
		"deep":   filepath.Join("one", "two", "three", "four", "five", "six", "seven", "eight", "nine"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, addErr := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: path}); ErrorCode(addErr) != CodeInvalidInput {
				t.Fatalf("undiscoverable destination error = %v", addErr)
			}
		})
	}
}

func TestValidateDiscoveryCapacityRejectsCandidateAndVisitExhaustion(t *testing.T) {
	for name, metrics := range map[string]discoveryMetrics{
		"candidate": {Inspected: maxCandidates},
		"visited":   {Visited: maxVisited},
		"truncated": {Truncated: true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateDiscoveryCapacity(metrics, 1); ErrorCode(err) != CodeConflict {
				t.Fatalf("capacity error = %v", err)
			}
		})
	}
	if err := validateDiscoveryCapacity(discoveryMetrics{Visited: maxVisited - 1}, 2); ErrorCode(err) != CodeConflict {
		t.Fatalf("staging reservation error = %v", err)
	}
	if err := validatePromotionCapacity(discoveryMetrics{Visited: maxVisited - 1}, 2); ErrorCode(err) != CodeConflict {
		t.Fatalf("final-promotion capacity error = %v", err)
	}
}

func TestLifecycleCloneRejectsExhaustedDiscoveryCapacity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxCandidates; index++ {
		if err := os.MkdirAll(filepath.Join(root, fmtInt(index), ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)}); ErrorCode(err) != CodeConflict {
		t.Fatalf("capacity-limited clone error = %v", err)
	}
}

func TestLifecycleCloneRevalidatesCapacityBeforePromotion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	populated := false
	lifecycle.commands = lifecycleRunnerFunc(func(ctx context.Context, name string, args ...string) (process.Result, error) {
		result, runErr := (osMutationRunner{}).Run(ctx, name, args...)
		if runErr == nil && !populated && slices.Contains(args, "clone") {
			populated = true
			for index := 0; index < maxCandidates; index++ {
				if mkdirErr := os.MkdirAll(filepath.Join(root, "capacity-"+fmtInt(index), ".git"), 0o755); mkdirErr != nil {
					t.Fatal(mkdirErr)
				}
			}
		}
		return result, runErr
	})
	if _, err = lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)}); ErrorCode(err) != CodeConflict {
		t.Fatalf("promotion capacity error = %v", err)
	}
	if _, err = os.Lstat(filepath.Join(root, "source")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capacity-limited clone was promoted: %v", err)
	}
}

func TestLifecycleAddRecoversWhenSourceWorktreeWasRemoved(t *testing.T) {
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
	runLifecycleGit(t, cloned.Path, "branch", "source-linked")
	runLifecycleGit(t, cloned.Path, "branch", "target-linked")
	sourceLinked, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "source-linked", Branch: "source-linked"})
	if err != nil {
		t.Fatal(err)
	}
	targetLinked, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: sourceLinked.Path, Path: "target-linked", Branch: "target-linked"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Remove(t.Context(), sourceLinked.Path); err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Add(t.Context(), AddRequest{RepositoryPath: "unrelated-missing", Path: "target-linked", Branch: "target-linked"}); ErrorCode(err) != CodeNotFound {
		t.Fatalf("unrelated missing source recovery error = %v", err)
	}

	recovered, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: sourceLinked.Path, Path: "target-linked", Branch: "target-linked"})
	if err != nil || !recovered.Recovered || recovered.Path != targetLinked.Path {
		t.Fatalf("missing source recovery = %+v, %v", recovered, err)
	}
}

func TestLifecycleCanRemoveReplacementWorktree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "original")
	original, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "replace", Branch: "original"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Remove(t.Context(), original.Path); err != nil {
		t.Fatal(err)
	}
	replacement, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "replace", Branch: "original"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Remove(t.Context(), original.Path); err != nil {
		t.Fatalf("replacement removal error = %v", err)
	}
	if _, inspectErr := Inspect(t.Context(), root, replacement.Path); ErrorCode(inspectErr) != CodeNotFound {
		t.Fatalf("replacement Worktree remains after intentional removal: %v", inspectErr)
	}
}

func TestLifecycleRemovalRejectsReplacementDuringSessionCheck(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	var primary, linked string
	replaced := false
	usage := lifecycleWorktreeUseFunc(func(context.Context, string) ([]string, error) {
		if !replaced {
			replaced = true
			runLifecycleGit(t, primary, "worktree", "remove", "--", linked)
			runLifecycleGit(t, primary, "worktree", "add", linked, "replace-race")
		}
		return nil, nil
	})
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), usage)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	primary = cloned.Path
	runLifecycleGit(t, primary, "branch", "replace-race")
	added, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: primary, Path: "replace-race", Branch: "replace-race"})
	if err != nil {
		t.Fatal(err)
	}
	linked = added.Path
	if _, err = lifecycle.Remove(t.Context(), linked); ErrorCode(err) != CodeConflict {
		t.Fatalf("replacement race removal error = %v", err)
	}
	if _, err = Inspect(t.Context(), root, linked); err != nil {
		t.Fatalf("replacement Worktree was removed: %v", err)
	}
}

func TestLifecycleRemovalReconcilesDisappearanceDuringSessionCheck(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	var primary, linked string
	removed := false
	usage := lifecycleWorktreeUseFunc(func(context.Context, string) ([]string, error) {
		if !removed {
			removed = true
			runLifecycleGit(t, primary, "worktree", "remove", "--", linked)
		}
		return nil, nil
	})
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), usage)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	primary = cloned.Path
	runLifecycleGit(t, primary, "branch", "disappear-race")
	added, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: primary, Path: "disappear-race", Branch: "disappear-race"})
	if err != nil {
		t.Fatal(err)
	}
	linked = added.Path
	result, err := lifecycle.Remove(t.Context(), linked)
	if err != nil || !result.Recovered {
		t.Fatalf("disappeared removal recovery = %+v, %v", result, err)
	}
	if conflict, conflictErr := lifecycle.incompleteConflict(linked, fingerprint("fresh-operation")); conflictErr != nil || conflict {
		t.Fatalf("retired removal record blocked another target operation: %t, %v", conflict, conflictErr)
	}
}

func TestCompletedAddRetryPreservesUserWorktreeLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "locked")
	added, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "locked", Branch: "locked"})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "worktree", "lock", "--reason", "user-lock", added.Path)
	if _, err = lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "locked", Branch: "locked"}); err != nil {
		t.Fatal(err)
	}
	output := lifecycleGitOutput(t, cloned.Path, "worktree", "list", "--porcelain")
	if !strings.Contains(output, "locked user-lock") {
		t.Fatalf("completed retry removed user lock: %s", output)
	}
}

func TestFreshAddDoesNotPruneUnrelatedRegistration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "stale-unrelated")
	stale := filepath.Join(root, "stale-unrelated")
	runLifecycleGit(t, cloned.Path, "worktree", "add", stale, "stale-unrelated")
	if err = os.RemoveAll(stale); err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "config", "gc.worktreePruneExpire", "now")
	runLifecycleGit(t, cloned.Path, "branch", "fresh")
	if _, err = lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "fresh", Branch: "fresh"}); err != nil {
		t.Fatal(err)
	}
	if output := lifecycleGitOutput(t, cloned.Path, "worktree", "list", "--porcelain"); !strings.Contains(output, stale) {
		t.Fatalf("fresh add pruned unrelated registration: %s", output)
	}
}

func TestRenameThroughVerifiedParentDoesNotFollowReplacementSymlink(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	destination, err := openDestinationParent(root, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	stageParent := filepath.Join(root, "stage")
	if err = os.Mkdir(stageParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(stageParent, "target"), 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(stageParent)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	originalParent := filepath.Join(root, "original-parent")
	if err = os.Rename(parent, originalParent); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err = os.Symlink(external, parent); err != nil {
		t.Fatal(err)
	}
	if err = renameNoReplaceAt(source, "target", destination, "target"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(originalParent, "target")); err != nil {
		t.Fatalf("verified destination did not receive Worktree: %v", err)
	}
	if _, err = os.Lstat(filepath.Join(external, "target")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement symlink received Worktree: %v", err)
	}
}

func TestWorktreeIdentityAllowsUnbornHead(t *testing.T) {
	gitDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDirectory, worktreeIncarnationFile), []byte("incarnation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection := Inspection{Repository: Repository{CommonDirectory: "/repo/.git"}, Worktree: Worktree{Path: "/root/unborn", GitDirectory: gitDirectory, Branch: "unborn"}}
	record := operationRecord{CommonDirectory: "/repo/.git", GitDirectory: gitDirectory, Branch: "unborn", IncarnationSHA256: fingerprint("incarnation\n")}
	matched, err := worktreeIdentityMatchesRecord(inspection, &record, "/root/unborn")
	if err != nil || !matched {
		t.Fatalf("unborn Worktree identity = %t, %v", matched, err)
	}
}

func TestInitializedSubmodulesAreDetectedBeforeMove(t *testing.T) {
	root := t.TempDir()
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, output := range map[string]string{"none": "-abc module\n", "initialized": " abc module\n"} {
		t.Run(name, func(t *testing.T) {
			lifecycle.commands = lifecycleRunnerFunc(func(context.Context, string, ...string) (process.Result, error) {
				return process.Result{Stdout: []byte(output)}, nil
			})
			initialized, detectErr := lifecycle.hasInitializedSubmodules(t.Context(), root)
			if detectErr != nil || initialized != (name == "initialized") {
				t.Fatalf("initialized = %t, %v", initialized, detectErr)
			}
		})
	}
}

func TestLifecycleAddRecoversAfterRenameBeforeGitRepair(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "repairable")
	failedRepair := false
	lifecycle.commands = lifecycleRunnerFunc(func(ctx context.Context, name string, args ...string) (process.Result, error) {
		if slices.Contains(args, "repair") && !failedRepair {
			failedRepair = true
			return process.Result{}, errors.New("injected repair failure")
		}
		return (osMutationRunner{}).Run(ctx, name, args...)
	})
	if _, err = lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "repairable", Branch: "repairable"}); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("post-rename repair error = %v", err)
	}
	lifecycle.commands = osMutationRunner{}
	recovered, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "repairable", Branch: "repairable"})
	if err != nil || !recovered.Recovered || recovered.Inspection == nil {
		t.Fatalf("post-rename recovery = %+v, %v", recovered, err)
	}
}

func TestRepositoryMutationLockSerializesAddAndPrune(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	commonDirectory := cloned.Inspection.Repository.CommonDirectory
	runLifecycleGit(t, cloned.Path, "branch", "serialized")
	repositoryLock, err := acquireMutationLock(state, commonDirectory+"#repository")
	if err != nil {
		t.Fatal(err)
	}
	request := AddRequest{RepositoryPath: cloned.Path, Path: "serialized", Branch: "serialized"}
	if _, err = lifecycle.Add(t.Context(), request); ErrorCode(err) != CodeOperationInProgress {
		t.Fatalf("concurrent add error = %v", err)
	}
	if _, err = lifecycle.Prune(t.Context()); ErrorCode(err) != CodeOperationInProgress {
		t.Fatalf("concurrent prune error = %v", err)
	}
	if err = repositoryLock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Add(t.Context(), request); err != nil {
		t.Fatalf("add after repository lock release = %v", err)
	}
	if _, err = lifecycle.Prune(t.Context()); err != nil {
		t.Fatalf("prune after repository lock release = %v", err)
	}
}

func TestLifecycleRemovalRecoversInterruptedQuarantineRestore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "restore-race")
	linked, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "restore-race", Branch: "restore-race"})
	if err != nil {
		t.Fatal(err)
	}
	failedRemove, failedRestore := false, false
	lifecycle.commands = lifecycleRunnerFunc(func(ctx context.Context, name string, args ...string) (process.Result, error) {
		last := args[len(args)-1]
		if slices.Contains(args, "remove") && strings.Contains(last, ".schooner-stage-") && !failedRemove {
			failedRemove = true
			return process.Result{}, errors.New("injected remove failure")
		}
		if slices.Contains(args, "repair") && last == linked.Path && !failedRestore {
			failedRestore = true
			return process.Result{}, errors.New("injected restore repair failure")
		}
		return (osMutationRunner{}).Run(ctx, name, args...)
	})
	if _, err = lifecycle.Remove(t.Context(), linked.Path); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("interrupted restore error = %v", err)
	}
	if _, statErr := os.Stat(linked.Path); statErr != nil {
		t.Fatalf("restored Worktree path is missing: %v", statErr)
	}
	lifecycle.commands = osMutationRunner{}
	if _, err = lifecycle.Remove(t.Context(), linked.Path); ErrorCode(err) != CodeConflict {
		t.Fatalf("restore reconciliation error = %v", err)
	}
	if _, err = lifecycle.Remove(t.Context(), linked.Path); err != nil {
		t.Fatalf("removal after restore reconciliation = %v", err)
	}
}

func TestLifecycleAddRejectsChangedMovePendingStage(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "snapshot-original")
	runLifecycleGit(t, cloned.Path, "branch", "snapshot-changed")
	failed := false
	lifecycle.commands = lifecycleRunnerFunc(func(ctx context.Context, name string, args ...string) (process.Result, error) {
		if slices.Contains(args, "submodule") && !failed {
			failed = true
			return process.Result{}, errors.New("injected pre-move failure")
		}
		return (osMutationRunner{}).Run(ctx, name, args...)
	})
	request := AddRequest{RepositoryPath: cloned.Path, Path: "snapshot-target", Branch: "snapshot-original"}
	if _, err = lifecycle.Add(t.Context(), request); err == nil {
		t.Fatal("pre-move interruption unexpectedly succeeded")
	}
	target, err := lifecycle.newPath(request.Path)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := lifecycle.findAddRecovery(target, fingerprint(request.Branch), fingerprint(cloned.Path))
	if err != nil || !found || record.Checkpoint != "move_pending" {
		t.Fatalf("move-pending record = %+v, %t, %v", record, found, err)
	}
	runLifecycleGit(t, record.StagingPath, "checkout", "snapshot-changed")
	lifecycle.commands = osMutationRunner{}
	if _, err = lifecycle.Add(t.Context(), request); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("changed staged snapshot error = %v", err)
	}
	if _, statErr := os.Stat(record.StagingPath); statErr != nil {
		t.Fatalf("changed staging Worktree was not preserved: %v", statErr)
	}
}

func TestLifecycleAddRejectsChangedPromotedSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "promoted-original")
	runLifecycleGit(t, cloned.Path, "branch", "promoted-changed")
	target, err := lifecycle.newPath("promoted-target")
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	lifecycle.commands = lifecycleRunnerFunc(func(ctx context.Context, name string, args ...string) (process.Result, error) {
		result, runErr := (osMutationRunner{}).Run(ctx, name, args...)
		if runErr == nil && slices.Contains(args, "repair") && args[len(args)-1] == target && !changed {
			changed = true
			runLifecycleGit(t, target, "checkout", "promoted-changed")
		}
		return result, runErr
	})
	if _, err = lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: target, Branch: "promoted-original"}); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("changed promoted snapshot error = %v", err)
	}
	inspected, err := Inspect(t.Context(), lifecycle.root, target)
	if err != nil || inspected.Worktree.Branch != "promoted-changed" {
		t.Fatalf("changed promoted Worktree = %+v, %v", inspected.Worktree, err)
	}
}

func TestLifecycleAddDestinationConflictDoesNotBlockRemoval(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "occupied")
	runLifecycleGit(t, cloned.Path, "branch", "other-intent")
	occupied, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "occupied", Branch: "occupied"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: occupied.Path, Branch: "other-intent"}); ErrorCode(err) != CodeConflict {
		t.Fatalf("destination conflict error = %v", err)
	}
	if _, err = lifecycle.Remove(t.Context(), occupied.Path); err != nil {
		t.Fatalf("removal after pre-effect add conflict = %v", err)
	}
}

func TestLifecycleCompletedAddRecoveryCleansOwnedStage(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "cleanup-stage")
	request := AddRequest{RepositoryPath: cloned.Path, Path: "cleanup-stage", Branch: "cleanup-stage"}
	added, err := lifecycle.Add(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	intent := fingerprint("worktree_add", added.Path, cloned.Inspection.Repository.CommonDirectory, request.Branch)
	record, found, err := lifecycle.load(intent)
	if err != nil || !found {
		t.Fatalf("completed add record = %+v, %t, %v", record, found, err)
	}
	if err = lifecycle.createOwnedStage(&record); err != nil {
		t.Fatal(err)
	}
	record.Checkpoint = "move_pending"
	if err = lifecycle.save(&record); err != nil {
		t.Fatal(err)
	}
	recovered, err := lifecycle.Add(t.Context(), request)
	if err != nil || !recovered.Recovered {
		t.Fatalf("completed recovery = %+v, %v", recovered, err)
	}
	if _, statErr := os.Lstat(filepath.Dir(record.StagingPath)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("owned staging parent remains after recovery: %v", statErr)
	}
}

func TestCatalogDiscoveryTruncationBlocksPrune(t *testing.T) {
	for _, message := range []string{"filesystem entry limit of 10000 reached", "checkout candidate limit of 500 reached", "catalog output limit of 786432 bytes reached"} {
		if !catalogDiscoveryTruncated(Catalog{Warnings: []Warning{{Message: message}}}) {
			t.Fatalf("truncation warning was not detected: %s", message)
		}
	}
	if catalogDiscoveryTruncated(Catalog{Warnings: []Warning{{Message: "ordinary inspection warning"}}}) {
		t.Fatal("ordinary warning was treated as truncation")
	}
}

func TestValidateCatalogCapacityReservesCreationEntry(t *testing.T) {
	catalog := Catalog{WorktreeRoot: "/root", Repositories: []Repository{}, Warnings: []Warning{{Message: strings.Repeat("x", maxCatalogBytes-maxOriginBytes)}}}
	if err := validateCatalogCapacity(catalog, "/root/new-worktree"); ErrorCode(err) != CodeConflict {
		t.Fatalf("near-limit catalog capacity error = %v", err)
	}
	if err := validateCatalogCapacity(Catalog{WorktreeRoot: "/root", Repositories: []Repository{}, Warnings: []Warning{}}, "/root/new-worktree"); err != nil {
		t.Fatalf("ordinary catalog capacity error = %v", err)
	}
}

func TestCloneSnapshotRejectsPromotedChanges(t *testing.T) {
	target := "/root/repo"
	record := operationRecord{Branch: "main", HEAD: "abc", Origin: "https://example.com/repo.git"}
	inspection := Inspection{Repository: Repository{CommonDirectory: target + "/.git", Origin: record.Origin}, Worktree: Worktree{Kind: Primary, Path: target, GitDirectory: target + "/.git", Branch: record.Branch, HEAD: record.HEAD}}
	if !cloneMatchesRecord(inspection, &record, target) {
		t.Fatal("matching promoted clone snapshot was rejected")
	}
	inspection.Worktree.HEAD = "changed"
	if cloneMatchesRecord(inspection, &record, target) {
		t.Fatal("changed promoted clone snapshot was accepted")
	}
}

func TestLifecycleUsesRootOwnedStagingForNestedTargets(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	ancestor := filepath.Join(root, "parent")
	if err := os.Symlink(external, ancestor); err != nil {
		t.Fatal(err)
	}
	unsafeParent := filepath.Join(ancestor, ".schooner-stage-operation")
	if err := mkdirOwnedStageParent(root, unsafeParent); err == nil {
		t.Fatal("staging under a destination ancestor unexpectedly succeeded")
	}
	if _, err := os.Lstat(filepath.Join(external, ".schooner-stage-operation")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging escaped through a destination symlink: %v", err)
	}
}

func TestUnlockStagedWorktreeRecoversWithoutLocalizedDiagnostics(t *testing.T) {
	root := t.TempDir()
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "stage")
	lifecycle.commands = lifecycleRunnerFunc(func(_ context.Context, _ string, args ...string) (process.Result, error) {
		if slices.Contains(args, "unlock") {
			return process.Result{Stderr: []byte("lokalisierte Diagnose")}, errors.New("exit status 1")
		}
		return process.Result{Stdout: []byte("worktree " + target + "\x00\x00")}, nil
	})
	if err = lifecycle.unlockStagedWorktree(t.Context(), filepath.Join(root, ".git"), target); err != nil {
		t.Fatalf("idempotent unlock recovery = %v", err)
	}
}

func TestGitMutationsUseNonInteractiveSSH(t *testing.T) {
	for _, mode := range []bool{false, true} {
		for _, expected := range []string{"LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "SSH_ASKPASS_REQUIRE=never", "GIT_SSH_COMMAND=ssh -o BatchMode=yes", "GIT_SSH_VARIANT=ssh"} {
			if !slices.Contains(gitMutationEnvironment(mode), expected) {
				t.Fatalf("Git mutation environment for noninteractive=%t does not contain %q: %v", mode, expected, gitMutationEnvironment(mode))
			}
		}
	}
}

func TestRegisteredWorktreeRejectsTruncatedOutput(t *testing.T) {
	root := t.TempDir()
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.commands = lifecycleRunnerFunc(func(context.Context, string, ...string) (process.Result, error) {
		return process.Result{Stdout: []byte("worktree /partial\x00"), Truncated: true}, nil
	})
	if _, err = lifecycle.registeredWorktree(t.Context(), filepath.Join(root, ".git"), filepath.Join(root, "target")); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("truncated registration error = %v", err)
	}
}

func TestLifecycleRemoveRejectsIgnoredFiles(t *testing.T) {
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
	if err = os.WriteFile(filepath.Join(cloned.Path, ".gitignore"), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "add", ".gitignore")
	runLifecycleGit(t, cloned.Path, "-c", "user.name=Schooner Test", "-c", "user.email=test@example.com", "commit", "-m", "ignore env")
	runLifecycleGit(t, cloned.Path, "branch", "ignored")
	linked, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "ignored", Branch: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	ignored := filepath.Join(linked.Path, ".env")
	if err = os.WriteFile(ignored, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err = lifecycle.Remove(t.Context(), linked.Path); ErrorCode(err) != CodeConflict {
		t.Fatalf("ignored-file removal error = %v", err)
	}
	if contents, err := os.ReadFile(ignored); err != nil || string(contents) != "secret" {
		t.Fatalf("ignored file changed: %q, %v", contents, err)
	}
}

func TestLifecycleRemovalQuarantinesAndPreservesConcurrentIgnoredFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cloned.Path, ".gitignore"), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "add", ".gitignore")
	runLifecycleGit(t, cloned.Path, "-c", "user.name=Schooner Test", "-c", "user.email=test@example.com", "commit", "-m", "ignore env")
	runLifecycleGit(t, cloned.Path, "branch", "quarantine-race")
	linked, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "quarantine-race", Branch: "quarantine-race"})
	if err != nil {
		t.Fatal(err)
	}
	wrote := false
	lifecycle.commands = lifecycleRunnerFunc(func(ctx context.Context, name string, args ...string) (process.Result, error) {
		result, runErr := (osMutationRunner{}).Run(ctx, name, args...)
		if runErr == nil && slices.Contains(args, "repair") && strings.Contains(args[len(args)-1], ".schooner-stage-") && !wrote {
			wrote = true
			if writeErr := os.WriteFile(filepath.Join(args[len(args)-1], ".env"), []byte("secret"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		return result, runErr
	})
	if _, err = lifecycle.Remove(t.Context(), linked.Path); ErrorCode(err) != CodeConflict {
		t.Fatalf("concurrent ignored-file removal error = %v", err)
	}
	if contents, readErr := os.ReadFile(filepath.Join(linked.Path, ".env")); readErr != nil || string(contents) != "secret" {
		t.Fatalf("concurrent ignored file was not restored: %q, %v", contents, readErr)
	}
}

func TestLifecycleRemovalRestoresConcurrentIdentityChange(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := lifecycle.Clone(t.Context(), CloneRequest{Source: createLifecycleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	runLifecycleGit(t, cloned.Path, "branch", "identity-original")
	runLifecycleGit(t, cloned.Path, "branch", "identity-changed")
	linked, err := lifecycle.Add(t.Context(), AddRequest{RepositoryPath: cloned.Path, Path: "identity-race", Branch: "identity-original"})
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	lifecycle.commands = lifecycleRunnerFunc(func(ctx context.Context, name string, args ...string) (process.Result, error) {
		result, runErr := (osMutationRunner{}).Run(ctx, name, args...)
		stage := args[len(args)-1]
		if runErr == nil && slices.Contains(args, "repair") && strings.Contains(stage, ".schooner-stage-") && !changed {
			changed = true
			runLifecycleGit(t, stage, "checkout", "identity-changed")
		}
		return result, runErr
	})
	if _, err = lifecycle.Remove(t.Context(), linked.Path); ErrorCode(err) != CodeConflict {
		t.Fatalf("concurrent identity-change removal error = %v", err)
	}
	inspection, inspectErr := Inspect(t.Context(), lifecycle.root, linked.Path)
	if inspectErr != nil || inspection.Worktree.Branch != "identity-changed" {
		t.Fatalf("restored changed Worktree = %+v, %v", inspection.Worktree, inspectErr)
	}
}

func TestLifecycleClassifiesCheckpointFailureAfterGitEffect(t *testing.T) {
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
	lifecycle.commands = lifecycleRunnerFunc(func(ctx context.Context, name string, args ...string) (process.Result, error) {
		result, runErr := (osMutationRunner{}).Run(ctx, name, args...)
		if runErr == nil && slices.Contains(args, "clone") {
			if renameErr := os.Rename(state, state+"-moved"); renameErr != nil {
				t.Fatal(renameErr)
			}
			if writeErr := os.WriteFile(state, []byte("not a directory"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		return result, runErr
	})

	if _, err = lifecycle.Clone(t.Context(), CloneRequest{Source: source}); ErrorCode(err) != CodeOutcomeUnknown {
		t.Fatalf("post-effect checkpoint error = %v", err)
	}
}

func TestLifecycleConflictScanIgnoresTemporaryCheckpointFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(root, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(state, ".operation-partial.json"), []byte(`{"partial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	conflict, err := lifecycle.incompleteConflict(filepath.Join(lifecycle.root, "target"), fingerprint("intent"))
	if err != nil || conflict {
		t.Fatalf("temporary checkpoint conflict = %t, %v", conflict, err)
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
	invalidUTF8 := string([]byte{'r', 'e', 'p', 'o', 0xff})
	for _, source := range []string{"-upload-pack=evil", "https://token@example.com/repo.git", "https://user:secret@example.com/repo.git", "https://example.com/repo.git?token=secret", "ssh://user:secret@example.com/repo.git", invalidUTF8} {
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
	if _, err = lifecycle.newPath(invalidUTF8); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("invalid UTF-8 path error = %v", err)
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
	if cloned, cloneErr := lifecycle.Clone(t.Context(), CloneRequest{Source: source, Branch: "main"}); cloneErr != nil || cloned.Inspection == nil {
		t.Fatalf("clone after retired destination conflict = %+v, %v", cloned, cloneErr)
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
