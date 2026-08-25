package repository

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/thewelshrich/schooner/internal/process"
)

const operationSchemaVersion = 1

const stageOwnershipFile = ".schooner-operation"
const worktreeIncarnationFile = ".schooner-incarnation"

const (
	CodeConflict            Code = "conflict"
	CodeAuthentication      Code = "authentication_required"
	CodePermissionDenied    Code = "permission_denied"
	CodeOperationInProgress Code = "operation_in_progress"
	CodeOutcomeUnknown      Code = "outcome_unknown"
)

type WorktreeUse interface {
	ManagedSessions(context.Context, string) ([]string, error)
}

type Lifecycle struct {
	root     string
	state    string
	inUse    WorktreeUse
	commands mutationRunner
	now      func() time.Time
}

type LifecycleOptions struct {
	NonInteractive bool
}

// MutationLock serializes Worktree removal with future managed Session starts.
// Session lifecycle code must acquire this lock before its final live
// Worktree validation and hold it until tmux metadata has been committed.
type MutationLock struct{ file *os.File }

func AcquireWorktreeMutationLock(stateDirectory, canonicalWorktreePath string) (*MutationLock, error) {
	if stateDirectory == "" || !filepath.IsAbs(stateDirectory) || filepath.Clean(stateDirectory) != stateDirectory || canonicalWorktreePath == "" || !filepath.IsAbs(canonicalWorktreePath) || filepath.Clean(canonicalWorktreePath) != canonicalWorktreePath {
		return nil, &Error{Code: CodeInvalidInput, Message: "Worktree mutation lock paths must be canonical and absolute"}
	}
	return acquireMutationLock(stateDirectory, canonicalWorktreePath)
}

func (lock *MutationLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

type CloneRequest struct {
	Source string
	Branch string
}

type AddRequest struct {
	RepositoryPath string
	Path           string
	Branch         string
}

type MutationResult struct {
	Action              string      `json:"action"`
	Recovered           bool        `json:"recovered"`
	WorktreeRoot        string      `json:"worktree_root"`
	Inspection          *Inspection `json:"inspection,omitempty"`
	Path                string      `json:"path,omitempty"`
	RepositoriesChecked int         `json:"repositories_checked,omitempty"`
}

type operationRecord struct {
	SchemaVersion     int       `json:"schema_version"`
	ID                string    `json:"id"`
	Kind              string    `json:"kind"`
	IntentSHA256      string    `json:"intent_sha256"`
	TargetPath        string    `json:"target_path"`
	StagingPath       string    `json:"staging_path,omitempty"`
	Checkpoint        string    `json:"checkpoint"`
	CommonDirectory   string    `json:"common_directory,omitempty"`
	Branch            string    `json:"branch,omitempty"`
	HEAD              string    `json:"head,omitempty"`
	Origin            string    `json:"origin,omitempty"`
	Detached          bool      `json:"detached,omitempty"`
	OwnershipToken    string    `json:"ownership_token,omitempty"`
	RefSHA256         string    `json:"ref_sha256,omitempty"`
	RepositorySHA256  string    `json:"repository_sha256,omitempty"`
	GitDirectory      string    `json:"git_directory,omitempty"`
	IncarnationSHA256 string    `json:"incarnation_sha256,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type mutationRunner interface {
	Run(context.Context, string, ...string) (process.Result, error)
}

type osMutationRunner struct{ nonInteractive bool }

func gitMutationEnvironment(nonInteractive bool) []string {
	if !nonInteractive {
		return nil
	}
	return []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_SSH_COMMAND=ssh -o BatchMode=yes", "GIT_SSH_VARIANT=ssh"}
}

func (runner osMutationRunner) Run(ctx context.Context, name string, args ...string) (process.Result, error) {
	return process.RunCapturedWithoutEnvironment(ctx, maxOutputBytes, gitRepositoryEnvironment,
		gitMutationEnvironment(runner.nonInteractive), name, args...)
}

func NewLifecycle(worktreeRoot, stateDirectory string, inUse WorktreeUse) (*Lifecycle, error) {
	return NewLifecycleWithOptions(worktreeRoot, stateDirectory, inUse, LifecycleOptions{})
}

func NewLifecycleWithOptions(worktreeRoot, stateDirectory string, inUse WorktreeUse, options LifecycleOptions) (*Lifecycle, error) {
	root, err := canonicalDirectory(worktreeRoot)
	if err != nil {
		return nil, err
	}
	if stateDirectory == "" || !filepath.IsAbs(stateDirectory) || filepath.Clean(stateDirectory) != stateDirectory {
		return nil, &Error{Code: CodeInvalidInput, Message: "operation state directory must be a canonical absolute path"}
	}
	return &Lifecycle{root: root, state: stateDirectory, inUse: inUse, commands: osMutationRunner{nonInteractive: options.NonInteractive}, now: time.Now}, nil
}

func DefaultOperationStateDirectory() (string, error) {
	current, err := user.Current()
	if err != nil || current.HomeDir == "" || !filepath.IsAbs(current.HomeDir) {
		return "", fmt.Errorf("resolve operation state directory: current user home is invalid")
	}
	return OperationStateDirectory(current.HomeDir)
}

func OperationStateDirectory(home string) (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		if home == "" || !filepath.IsAbs(home) {
			return "", fmt.Errorf("resolve operation state directory: current user home is invalid")
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_STATE_HOME must be an absolute path")
	}
	return filepath.Join(filepath.Clean(base), "schooner", "operations", "git"), nil
}

func (l *Lifecycle) Clone(ctx context.Context, request CloneRequest) (MutationResult, error) {
	source, name, err := validateCloneSource(request.Source)
	if err != nil {
		return MutationResult{}, err
	}
	if err = validateRef(request.Branch); err != nil {
		return MutationResult{}, err
	}
	target, err := l.newPath(name)
	if err != nil {
		return MutationResult{}, err
	}
	intent := fingerprint("clone", target, source, request.Branch)
	return l.withOperation(ctx, "clone", target, intent, func(record *operationRecord, recovered bool) (MutationResult, error) {
		if record.Checkpoint == "complete" {
			if inspected, inspectErr := Inspect(ctx, l.root, target); inspectErr == nil && cloneMatchesRecord(inspected, record, target) {
				return MutationResult{Action: "clone", Recovered: true, WorktreeRoot: l.root, Inspection: &inspected, Path: inspected.Worktree.Path}, nil
			}
		}
		if _, statErr := os.Lstat(target); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			if statErr == nil && record.Checkpoint == "promote_pending" {
				if _, stageErr := os.Lstat(record.StagingPath); !errors.Is(stageErr, os.ErrNotExist) {
					return MutationResult{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("clone destination %q changed before staged promotion", target), Cause: stageErr}
				}
				if inspected, inspectErr := Inspect(ctx, l.root, target); inspectErr == nil && cloneMatchesRecord(inspected, record, target) {
					recordSnapshot(record, inspected)
					record.Checkpoint = "complete"
					if saveErr := l.saveAfterEffect(record); saveErr != nil {
						return MutationResult{}, saveErr
					}
					_ = removeOwnedStage(record)
					return MutationResult{Action: "clone", Recovered: true, WorktreeRoot: l.root, Inspection: &inspected, Path: inspected.Worktree.Path}, nil
				}
			}
			return MutationResult{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("clone destination %q already exists", target), Cause: statErr}
		}
		if record.StagingPath == "" {
			record.StagingPath = filepath.Join(l.root, ".schooner-stage-"+record.ID, filepath.Base(target))
		}
		if err = l.prepareStage(record); err != nil {
			return MutationResult{}, err
		}
		if _, inspectErr := Inspect(ctx, l.root, record.StagingPath); inspectErr != nil {
			if err = l.validateDiscoverableTarget(ctx, target, false); err != nil {
				return MutationResult{}, err
			}
			if removeErr := removeOwnedStage(record); removeErr != nil {
				return MutationResult{}, removeErr
			}
			if err = l.createOwnedStage(record); err != nil {
				return MutationResult{}, err
			}
			record.Checkpoint = "clone_pending"
			if err = l.save(record); err != nil {
				return MutationResult{}, err
			}
			args := []string{"-C", l.root, "clone"}
			if request.Branch != "" {
				args = append(args, "--branch", request.Branch)
			}
			args = append(args, "--", source, record.StagingPath)
			if _, err = l.runGit(ctx, args...); err != nil {
				return MutationResult{}, err
			}
			record.Checkpoint = "clone_finished"
			if err = l.saveAfterEffect(record); err != nil {
				return MutationResult{}, err
			}
		}
		staged, err := Inspect(ctx, l.root, record.StagingPath)
		if err != nil {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "Git clone finished but its staged Worktree could not be verified", Cause: err}
		}
		if record.Checkpoint == "clone_pending" {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "staged clone exists but successful Git completion was not checkpointed"}
		}
		recordSnapshot(record, staged)
		record.Checkpoint = "promote_pending"
		if err = l.saveAfterEffect(record); err != nil {
			return MutationResult{}, err
		}
		if _, err = os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			return MutationResult{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("clone destination %q changed during the operation", target), Cause: err}
		}
		if err = l.validateDiscoverableTarget(ctx, target, true); err != nil {
			return MutationResult{}, err
		}
		if err = renameNoReplace(record.StagingPath, target); err != nil {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "cloned Worktree could not be promoted safely", Cause: err}
		}
		_ = removeOwnedStage(record)
		inspected, err := Inspect(ctx, l.root, target)
		if err != nil {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "cloned Worktree could not be verified", Cause: err}
		}
		recordSnapshot(record, inspected)
		record.Checkpoint = "complete"
		if err = l.saveAfterEffect(record); err != nil {
			return MutationResult{}, err
		}
		return MutationResult{Action: "clone", Recovered: recovered, WorktreeRoot: l.root, Inspection: &inspected, Path: inspected.Worktree.Path}, nil
	})
}

func (l *Lifecycle) Add(ctx context.Context, request AddRequest) (MutationResult, error) {
	if err := validateRef(request.Branch); err != nil {
		return MutationResult{}, err
	}
	target, err := l.newPath(request.Path)
	if err != nil {
		return MutationResult{}, err
	}
	repositoryPath, err := l.newPath(request.RepositoryPath)
	if err != nil {
		return MutationResult{}, err
	}
	repositoryHash := fingerprint(repositoryPath)
	repositoryInspection, err := Inspect(ctx, l.root, repositoryPath)
	intent := ""
	if err != nil {
		if ErrorCode(err) != CodeNotFound {
			return MutationResult{}, err
		}
		recoveredRecord, found, recoverErr := l.findAddRecovery(target, fingerprint(request.Branch), repositoryHash)
		if recoverErr != nil {
			return MutationResult{}, recoverErr
		}
		if !found {
			return MutationResult{}, err
		}
		repositoryInspection.Repository.CommonDirectory = recoveredRecord.CommonDirectory
		intent = recoveredRecord.IntentSHA256
	} else {
		intent = fingerprint("worktree_add", target, repositoryInspection.Repository.CommonDirectory, request.Branch)
	}
	commonDirectory := repositoryInspection.Repository.CommonDirectory
	selectorHash := fingerprint(request.Branch)
	return l.withOperation(ctx, "worktree_add", target, intent, func(record *operationRecord, recovered bool) (MutationResult, error) {
		if record.RefSHA256 != "" && record.RefSHA256 != selectorHash || record.CommonDirectory != "" && record.CommonDirectory != commonDirectory || record.RepositorySHA256 != "" && record.RepositorySHA256 != repositoryHash {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "Worktree add checkpoint does not match its repository or ref selector"}
		}
		if record.RefSHA256 == "" || record.CommonDirectory == "" || record.RepositorySHA256 == "" {
			record.RefSHA256, record.CommonDirectory, record.RepositorySHA256 = selectorHash, commonDirectory, repositoryHash
			if err = l.save(record); err != nil {
				return MutationResult{}, err
			}
		}
		if existing, inspectErr := Inspect(ctx, l.root, target); inspectErr == nil {
			if (record.Checkpoint == "complete" || record.Checkpoint == "move_pending") && worktreeMatchesRecord(existing, record, target, commonDirectory) {
				recordSnapshot(record, existing)
				record.Checkpoint = "complete"
				if err = l.saveAfterEffect(record); err != nil {
					return MutationResult{}, err
				}
				return MutationResult{Action: "worktree_add", Recovered: true, WorktreeRoot: l.root, Inspection: &existing, Path: existing.Worktree.Path}, nil
			}
			return MutationResult{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("Worktree destination %q already exists", target)}
		}
		if record.Checkpoint == "move_pending" {
			if _, statErr := os.Lstat(target); statErr == nil {
				if _, repairErr := l.runGit(ctx, "--git-dir", commonDirectory, "worktree", "repair", target); repairErr != nil {
					return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "moved Worktree registration could not be repaired", Cause: repairErr}
				}
				if existing, inspectErr := Inspect(ctx, l.root, target); inspectErr == nil && worktreeMatchesRecord(existing, record, target, commonDirectory) {
					recordSnapshot(record, existing)
					record.Checkpoint = "complete"
					if err = l.saveAfterEffect(record); err != nil {
						return MutationResult{}, err
					}
					_ = removeOwnedStage(record)
					return MutationResult{Action: "worktree_add", Recovered: true, WorktreeRoot: l.root, Inspection: &existing, Path: existing.Worktree.Path}, nil
				}
				return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "moved Worktree could not be matched to its checkpoint"}
			}
		}
		if _, statErr := os.Lstat(target); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			return MutationResult{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("Worktree destination %q already exists", target), Cause: statErr}
		}
		if record.StagingPath == "" {
			record.StagingPath = filepath.Join(l.root, ".schooner-stage-"+record.ID, filepath.Base(target))
		}
		if err = l.prepareStage(record); err != nil {
			return MutationResult{}, err
		}
		if _, inspectErr := Inspect(ctx, l.root, record.StagingPath); inspectErr != nil {
			if err = l.validateDiscoverableTarget(ctx, target, false); err != nil {
				return MutationResult{}, err
			}
			_, removeErr := l.runGit(ctx, "--git-dir", commonDirectory, "worktree", "remove", "--", record.StagingPath)
			registered, registrationErr := l.registeredWorktree(ctx, commonDirectory, record.StagingPath)
			if registrationErr != nil {
				return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "operation-owned staging registration could not be reconciled", Cause: registrationErr}
			}
			if registered {
				return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "operation-owned staging Worktree remains registered", Cause: removeErr}
			}
			if err = removeOwnedStage(record); err != nil {
				return MutationResult{}, err
			}
			if err = l.createOwnedStage(record); err != nil {
				return MutationResult{}, err
			}
			if checked, pathErr := l.newPath(target); pathErr != nil || checked != target {
				return MutationResult{}, &Error{Code: CodeInvalidInput, Message: "Worktree destination changed during validation", Cause: pathErr}
			}
			record.Checkpoint = "add_pending"
			if err = l.save(record); err != nil {
				return MutationResult{}, err
			}
			args := []string{"--git-dir", commonDirectory, "worktree", "add", "--lock", "--reason", "schooner-operation:" + record.ID}
			args = append(args, record.StagingPath)
			if request.Branch != "" {
				args = append(args, request.Branch)
			}
			if _, err = l.runGit(ctx, args...); err != nil {
				return MutationResult{}, err
			}
			record.Checkpoint = "add_finished"
			if err = l.saveAfterEffect(record); err != nil {
				return MutationResult{}, err
			}
		}
		staged, err := Inspect(ctx, l.root, record.StagingPath)
		if err != nil || staged.Repository.CommonDirectory != commonDirectory {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "staged linked Worktree could not be verified", Cause: err}
		}
		if record.Checkpoint == "add_pending" {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "staged linked Worktree exists but successful Git completion was not checkpointed"}
		}
		recordSnapshot(record, staged)
		record.Checkpoint = "move_pending"
		if err = l.saveAfterEffect(record); err != nil {
			return MutationResult{}, err
		}
		if err = l.unlockStagedWorktree(ctx, commonDirectory, record.StagingPath); err != nil {
			return MutationResult{}, err
		}
		if err = l.validateDiscoverableTarget(ctx, target, true); err != nil {
			return MutationResult{}, err
		}
		initialized, submoduleErr := l.hasInitializedSubmodules(ctx, record.StagingPath)
		if submoduleErr != nil {
			return MutationResult{}, submoduleErr
		}
		if initialized {
			return MutationResult{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("staged Worktree %q contains initialized submodules; submodule lifecycle is outside this operation", record.StagingPath)}
		}
		parent, parentErr := openDestinationParent(l.root, filepath.Dir(target))
		if parentErr != nil {
			return MutationResult{}, &Error{Code: CodeConflict, Message: "Worktree destination ancestors changed during final validation", Cause: parentErr}
		}
		if err = l.validateDiscoverableTarget(ctx, target, true); err != nil {
			_ = parent.Close()
			return MutationResult{}, err
		}
		sourceParent, sourceErr := openExistingDirectory(l.root, filepath.Dir(record.StagingPath))
		if sourceErr == nil {
			sourceErr = renameNoReplaceAt(sourceParent, filepath.Base(record.StagingPath), parent, filepath.Base(target))
		}
		if sourceErr == nil {
			if syncErr := sourceParent.Sync(); syncErr != nil {
				sourceErr = syncErr
			}
		}
		if sourceErr == nil {
			if syncErr := parent.Sync(); syncErr != nil {
				sourceErr = syncErr
			}
		}
		if sourceParent != nil {
			if closeErr := sourceParent.Close(); sourceErr == nil {
				sourceErr = closeErr
			}
		}
		closeParentErr := parent.Close()
		if sourceErr == nil && closeParentErr != nil {
			sourceErr = closeParentErr
		}
		if sourceErr != nil {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "staged Worktree could not be moved through verified destination handles", Cause: sourceErr}
		}
		if _, err = l.runGit(ctx, "--git-dir", commonDirectory, "worktree", "repair", target); err != nil {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "moved Worktree registration could not be repaired", Cause: err}
		}
		_ = removeOwnedStage(record)
		inspected, err := Inspect(ctx, l.root, target)
		if err != nil {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "linked Worktree could not be verified", Cause: err}
		}
		recordSnapshot(record, inspected)
		record.Checkpoint = "complete"
		if err = l.saveAfterEffect(record); err != nil {
			return MutationResult{}, err
		}
		return MutationResult{Action: "worktree_add", Recovered: recovered, WorktreeRoot: l.root, Inspection: &inspected, Path: inspected.Worktree.Path}, nil
	})
}

func (l *Lifecycle) Remove(ctx context.Context, selector string) (MutationResult, error) {
	target, err := l.newPath(selector)
	if err != nil {
		return MutationResult{}, err
	}
	lock, err := acquireMutationLock(l.state, target)
	if err != nil {
		return MutationResult{}, err
	}
	defer func() { _ = lock.Close() }()
	initial, err := Inspect(ctx, l.root, target)
	if ErrorCode(err) == CodeNotFound {
		record, found, findErr := l.findRemoveRecovery(target)
		if findErr != nil {
			return MutationResult{}, findErr
		}
		if found {
			return l.reconcileRemoved(ctx, target, &record, true)
		}
		return MutationResult{}, err
	}
	if err != nil {
		return MutationResult{}, err
	}
	if initial.Worktree.Kind != Linked {
		return MutationResult{}, &Error{Code: CodeConflict, Message: "primary Worktrees require a separate repository-removal workflow"}
	}
	ownershipToken, tokenErr := newOwnershipToken()
	if tokenErr != nil {
		return MutationResult{}, fmt.Errorf("create Worktree removal identity: %w", tokenErr)
	}
	seed := operationRecord{OwnershipToken: ownershipToken}
	if err = ensureWorktreeIncarnation(&seed, initial); err != nil {
		return MutationResult{}, err
	}
	recordSnapshot(&seed, initial)
	intent := fingerprint("worktree_remove", target, initial.Repository.CommonDirectory, seed.IncarnationSHA256)
	return l.withOperationLocked(ctx, "worktree_remove", target, intent, func(record *operationRecord, recovered bool) (MutationResult, error) {
		current, inspectErr := Inspect(ctx, l.root, target)
		if ErrorCode(inspectErr) == CodeNotFound {
			if record.Checkpoint != "remove_pending" && record.Checkpoint != "complete" {
				return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "Worktree disappeared before Git removal could be established"}
			}
			return l.reconcileRemoved(ctx, target, record, true)
		}
		if inspectErr != nil {
			return MutationResult{}, inspectErr
		}
		if record.IncarnationSHA256 == "" {
			record.IncarnationSHA256 = seed.IncarnationSHA256
		}
		if record.Checkpoint == "remove_pending" || record.Checkpoint == "complete" {
			matches, matchErr := worktreeIdentityMatchesRecord(current, record, target)
			if matchErr != nil {
				return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "Worktree removal identity could not be verified", Cause: matchErr}
			}
			if !matches {
				return MutationResult{}, &Error{Code: CodeConflict, Message: "Worktree removal target has been replaced since the recorded operation"}
			}
		}
		if dirty(current.Worktree.Status) {
			return MutationResult{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("Worktree %q contains local changes or ignored files", current.Worktree.RelativePath)}
		}
		if l.inUse != nil {
			sessions, sessionErr := l.inUse.ManagedSessions(ctx, current.Worktree.Path)
			if sessionErr != nil {
				return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "managed Sessions could not be checked safely", Cause: sessionErr}
			}
			if len(sessions) != 0 {
				return MutationResult{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("Worktree %q has an active managed Session", current.Worktree.RelativePath)}
			}
		}
		current, inspectErr = Inspect(ctx, l.root, target)
		if inspectErr != nil || dirty(current.Worktree.Status) {
			return MutationResult{}, &Error{Code: CodeConflict, Message: "Worktree changed during removal validation", Cause: inspectErr}
		}
		matchesInitial, matchInitialErr := worktreeIdentityMatchesRecord(current, &seed, target)
		if matchInitialErr != nil {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "initial Worktree identity could not be rechecked", Cause: matchInitialErr}
		}
		if !matchesInitial {
			return MutationResult{}, &Error{Code: CodeConflict, Message: "Worktree was replaced during removal validation"}
		}
		if err = ensureWorktreeIncarnation(record, current); err != nil {
			return MutationResult{}, err
		}
		recordSnapshot(record, current)
		record.Checkpoint = "remove_pending"
		if err = l.save(record); err != nil {
			return MutationResult{}, err
		}
		if _, err = l.runGit(ctx, "-C", current.Worktree.Path, "worktree", "remove", "--", current.Worktree.Path); err != nil {
			return MutationResult{}, err
		}
		if _, inspectErr = Inspect(ctx, l.root, target); ErrorCode(inspectErr) != CodeNotFound {
			return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "Worktree removal could not be verified", Cause: inspectErr}
		}
		return l.reconcileRemoved(ctx, target, record, recovered)
	})
}

func (l *Lifecycle) Prune(ctx context.Context) (MutationResult, error) {
	intent := fingerprint("worktree_prune", l.root)
	return l.withOperation(ctx, "worktree_prune", l.root+"#prune", intent, func(record *operationRecord, recovered bool) (MutationResult, error) {
		catalog, err := Discover(ctx, l.root)
		if err != nil {
			return MutationResult{}, err
		}
		if catalogDiscoveryTruncated(catalog) {
			return MutationResult{}, &Error{Code: CodeConflict, Message: "Worktree registrations cannot be pruned while repository discovery is truncated"}
		}
		record.Checkpoint = "prune_pending"
		if err = l.save(record); err != nil {
			return MutationResult{}, err
		}
		for _, value := range catalog.Repositories {
			path := firstLivePath(value)
			if path == "" {
				continue
			}
			if _, err = l.runGit(ctx, "-C", path, "worktree", "prune"); err != nil {
				return MutationResult{}, err
			}
		}
		record.Checkpoint = "complete"
		if err = l.saveAfterEffect(record); err != nil {
			return MutationResult{}, err
		}
		return MutationResult{Action: "worktree_prune", Recovered: recovered, WorktreeRoot: l.root, Path: l.root, RepositoriesChecked: len(catalog.Repositories)}, nil
	})
}

func (l *Lifecycle) withOperation(ctx context.Context, kind, target, intent string, fn func(*operationRecord, bool) (MutationResult, error)) (MutationResult, error) {
	lock, err := acquireMutationLock(l.state, target)
	if err != nil {
		return MutationResult{}, err
	}
	defer func() { _ = lock.Close() }()
	return l.withOperationLocked(ctx, kind, target, intent, fn)
}

func (l *Lifecycle) withOperationLocked(ctx context.Context, kind, target, intent string, fn func(*operationRecord, bool) (MutationResult, error)) (MutationResult, error) {
	record, found, err := l.load(intent)
	if err != nil {
		return MutationResult{}, err
	}
	if found && (record.Kind != kind || record.TargetPath != target) {
		return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "Git operation checkpoint does not match the requested mutation"}
	}
	if conflict, conflictErr := l.incompleteConflict(target, intent); conflictErr != nil {
		return MutationResult{}, conflictErr
	} else if conflict {
		return MutationResult{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("a different interrupted Git operation owns %q", target)}
	}
	if !found {
		ownershipToken, tokenErr := newOwnershipToken()
		if tokenErr != nil {
			return MutationResult{}, fmt.Errorf("create Git operation ownership token: %w", tokenErr)
		}
		record = operationRecord{SchemaVersion: operationSchemaVersion, ID: intent[:24], Kind: kind, IntentSHA256: intent, TargetPath: target, Checkpoint: "requested", OwnershipToken: ownershipToken, UpdatedAt: l.now().UTC()}
		if err = l.save(&record); err != nil {
			return MutationResult{}, err
		}
	}
	return fn(&record, found)
}

func acquireMutationLock(stateDirectory, target string) (*MutationLock, error) {
	if err := ensurePrivateDirectory(stateDirectory); err != nil {
		return nil, fmt.Errorf("create Git operation state: %w", err)
	}
	if err := ensurePrivateDirectory(filepath.Join(stateDirectory, "locks")); err != nil {
		return nil, fmt.Errorf("create Git operation state: %w", err)
	}
	lockPath := filepath.Join(stateDirectory, "locks", fingerprint(target)+".lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Git operation lock: %w", err)
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, &Error{Code: CodeOperationInProgress, Message: fmt.Sprintf("another operation is already using %q", target), Cause: err}
	}
	return &MutationLock{file: file}, nil
}

func (l *Lifecycle) runGit(ctx context.Context, args ...string) (process.Result, error) {
	result, err := l.commands.Run(ctx, "git", append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false"}, args...)...)
	if err == nil {
		return result, nil
	}
	message := strings.ToLower(string(result.Stderr))
	code := CodeConflict
	safe := "Git operation failed"
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return result, &Error{Code: CodeOutcomeUnknown, Message: "Git operation was interrupted; retry to reconcile it", Cause: err}
	case strings.Contains(message, "authentication failed"), strings.Contains(message, "permission denied (publickey"), strings.Contains(message, "could not read username"), strings.Contains(message, "terminal prompts disabled"):
		code, safe = CodeAuthentication, "Git source authentication failed using the Box user's credentials"
	case strings.Contains(message, "permission denied"):
		code, safe = CodePermissionDenied, "Git operation was denied by the filesystem or remote source"
	case result.Truncated:
		code, safe = CodeOutcomeUnknown, "Git operation failed after producing bounded diagnostics; retry to reconcile it"
	}
	return result, &Error{Code: code, Message: safe, Cause: err}
}

func (l *Lifecycle) hasInitializedSubmodules(ctx context.Context, path string) (bool, error) {
	result, err := l.runGit(ctx, "-C", path, "submodule", "status", "--recursive")
	if err != nil {
		return false, err
	}
	if result.Truncated {
		return false, &Error{Code: CodeOutcomeUnknown, Message: "submodule status exceeded the verification limit"}
	}
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		if line != "" && line[0] != '-' {
			return true, nil
		}
	}
	return false, nil
}

func (l *Lifecycle) prepareStage(record *operationRecord) error {
	if record.OwnershipToken == "" || !within(l.root, record.StagingPath) || filepath.Base(filepath.Dir(record.StagingPath)) != ".schooner-stage-"+record.ID {
		return &Error{Code: CodeOutcomeUnknown, Message: "Git operation staging path is invalid"}
	}
	return nil
}

func (l *Lifecycle) createOwnedStage(record *operationRecord) error {
	if err := l.prepareStage(record); err != nil {
		return err
	}
	parent := filepath.Dir(record.StagingPath)
	if checked, err := l.newPath(record.TargetPath); err != nil || checked != record.TargetPath {
		return &Error{Code: CodeInvalidInput, Message: "Worktree destination changed during staging", Cause: err}
	}
	if err := mkdirOwnedStageParent(l.root, parent); err != nil {
		return &Error{Code: CodeOutcomeUnknown, Message: "operation staging directory already exists without established ownership", Cause: err}
	}
	marker := filepath.Join(parent, stageOwnershipFile)
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		_, err = io.WriteString(file, record.OwnershipToken+"\n")
	}
	if err == nil {
		err = file.Sync()
	}
	if file != nil {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		_ = os.Remove(marker)
		_ = os.Remove(parent)
		return &Error{Code: CodeOutcomeUnknown, Message: "operation staging ownership could not be recorded", Cause: err}
	}
	return nil
}

func verifyOwnedStage(record *operationRecord) error {
	parent := filepath.Dir(record.StagingPath)
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return &Error{Code: CodeOutcomeUnknown, Message: "operation staging path is not an owned directory"}
	}
	marker := filepath.Join(parent, stageOwnershipFile)
	file, err := os.OpenFile(marker, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return &Error{Code: CodeOutcomeUnknown, Message: "operation staging ownership could not be verified", Cause: err}
	}
	defer func() { _ = file.Close() }()
	markerInfo, err := file.Stat()
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Size() > 256 {
		return &Error{Code: CodeOutcomeUnknown, Message: "operation staging ownership marker is invalid", Cause: err}
	}
	contents, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil || string(contents) != record.OwnershipToken+"\n" {
		return &Error{Code: CodeOutcomeUnknown, Message: "operation staging ownership does not match its checkpoint", Cause: err}
	}
	return nil
}

func removeOwnedStage(record *operationRecord) error {
	parent := filepath.Dir(record.StagingPath)
	if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return &Error{Code: CodeOutcomeUnknown, Message: "operation staging path could not be inspected", Cause: err}
	}
	if err := verifyOwnedStage(record); err != nil {
		return err
	}
	if err := os.RemoveAll(parent); err != nil {
		return &Error{Code: CodeOutcomeUnknown, Message: "operation-owned staging path could not be cleaned", Cause: err}
	}
	return nil
}

func newOwnershipToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func recordSnapshot(record *operationRecord, inspected Inspection) {
	record.CommonDirectory = inspected.Repository.CommonDirectory
	record.Branch = inspected.Worktree.Branch
	record.HEAD = inspected.Worktree.HEAD
	record.Origin = inspected.Repository.Origin
	record.Detached = inspected.Worktree.Detached
	record.GitDirectory = inspected.Worktree.GitDirectory
}

func worktreeIdentityMatchesRecord(inspected Inspection, record *operationRecord, target string) (bool, error) {
	if record.IncarnationSHA256 == "" {
		return false, nil
	}
	contents, err := readWorktreeIncarnation(inspected.Worktree.GitDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return record.CommonDirectory != "" && record.GitDirectory != "" &&
		inspected.Worktree.Path == target &&
		inspected.Repository.CommonDirectory == record.CommonDirectory &&
		inspected.Worktree.GitDirectory == record.GitDirectory &&
		inspected.Worktree.Branch == record.Branch &&
		inspected.Worktree.HEAD == record.HEAD &&
		inspected.Worktree.Detached == record.Detached &&
		fingerprint(string(contents)) == record.IncarnationSHA256, nil
}

func ensureWorktreeIncarnation(record *operationRecord, inspected Inspection) error {
	path := filepath.Join(inspected.Worktree.GitDirectory, worktreeIncarnationFile)
	contents, err := readWorktreeIncarnation(inspected.Worktree.GitDirectory)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if errors.Is(createErr, os.ErrExist) {
			contents, createErr = readWorktreeIncarnation(inspected.Worktree.GitDirectory)
		}
		if createErr != nil {
			return &Error{Code: CodeOutcomeUnknown, Message: "Worktree incarnation marker could not be created", Cause: createErr}
		}
		if file != nil {
			contents = []byte(record.OwnershipToken + "\n")
			_, writeErr := file.Write(contents)
			if writeErr == nil {
				writeErr = file.Sync()
			}
			if closeErr := file.Close(); writeErr == nil {
				writeErr = closeErr
			}
			if writeErr != nil {
				return &Error{Code: CodeOutcomeUnknown, Message: "Worktree incarnation marker could not be recorded", Cause: writeErr}
			}
		}
	} else if err != nil {
		return &Error{Code: CodeOutcomeUnknown, Message: "Worktree incarnation marker could not be read", Cause: err}
	}
	record.IncarnationSHA256 = fingerprint(string(contents))
	return nil
}

func readWorktreeIncarnation(gitDirectory string) ([]byte, error) {
	file, err := os.OpenFile(filepath.Join(gitDirectory, worktreeIncarnationFile), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256 {
		return nil, &Error{Code: CodeOutcomeUnknown, Message: "Worktree incarnation marker is invalid", Cause: err}
	}
	return io.ReadAll(io.LimitReader(file, 257))
}

func cloneMatchesRecord(inspected Inspection, record *operationRecord, target string) bool {
	expectedGitDirectory := filepath.Join(target, ".git")
	return inspected.Worktree.Kind == Primary &&
		inspected.Worktree.Path == target &&
		inspected.Worktree.GitDirectory == expectedGitDirectory &&
		inspected.Repository.CommonDirectory == expectedGitDirectory &&
		inspected.Worktree.Branch == record.Branch &&
		inspected.Worktree.HEAD == record.HEAD &&
		inspected.Worktree.Detached == record.Detached &&
		inspected.Repository.Origin == record.Origin
}

func worktreeMatchesRecord(inspected Inspection, record *operationRecord, target, commonDirectory string) bool {
	return inspected.Worktree.Path == target &&
		inspected.Repository.CommonDirectory == commonDirectory &&
		inspected.Worktree.Branch == record.Branch &&
		inspected.Worktree.HEAD == record.HEAD &&
		inspected.Worktree.Detached == record.Detached
}

func (l *Lifecycle) validateDiscoverableTarget(ctx context.Context, target string, replacingStage bool) error {
	relative, err := filepath.Rel(l.root, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return &Error{Code: CodeInvalidInput, Message: "Worktree destination is outside the discovery root", Cause: err}
	}
	if len(strings.Split(filepath.ToSlash(relative), "/")) > maxDepth {
		return &Error{Code: CodeInvalidInput, Message: fmt.Sprintf("Worktree destination exceeds the discovery depth limit of %d", maxDepth)}
	}
	_, _, metrics, err := walkCandidatesMeasured(ctx, l.root, maxVisited, commandRunner{})
	if err != nil {
		return err
	}
	if metrics.Truncated {
		return &Error{Code: CodeConflict, Message: "Worktree destination cannot be added while discovery bounds are exhausted"}
	}
	if !replacingStage {
		missingDirectories := 1
		for ancestor := filepath.Dir(target); ancestor != l.root; ancestor = filepath.Dir(ancestor) {
			if _, statErr := os.Lstat(ancestor); errors.Is(statErr, os.ErrNotExist) {
				missingDirectories++
			} else {
				break
			}
		}
		// The operation-owned staging layout adds one more directory than the
		// final target layout while Git is populating it.
		if err = validateDiscoveryCapacity(metrics, missingDirectories+2); err != nil {
			return err
		}
	} else {
		missingAncestors := 0
		for ancestor := filepath.Dir(target); ancestor != l.root; ancestor = filepath.Dir(ancestor) {
			if _, statErr := os.Lstat(ancestor); errors.Is(statErr, os.ErrNotExist) {
				missingAncestors++
			} else {
				break
			}
		}
		if err = validatePromotionCapacity(metrics, missingAncestors); err != nil {
			return err
		}
	}
	for ancestor := filepath.Dir(target); ancestor != l.root; ancestor = filepath.Dir(ancestor) {
		if _, statErr := os.Lstat(ancestor); errors.Is(statErr, os.ErrNotExist) {
			continue
		} else if statErr != nil {
			return statErr
		}
		if inspected, inspectErr := Inspect(ctx, l.root, ancestor); inspectErr == nil && within(inspected.Worktree.Path, target) {
			return &Error{Code: CodeInvalidInput, Message: fmt.Sprintf("Worktree destination must not be nested inside Worktree %q", inspected.Worktree.RelativePath)}
		} else if inspectErr != nil && ErrorCode(inspectErr) != CodeNotFound {
			return inspectErr
		}
	}
	return nil
}

func validateDiscoveryCapacity(metrics discoveryMetrics, additionalDirectories int) error {
	if metrics.Truncated || metrics.Inspected+1 > maxCandidates || metrics.Visited+additionalDirectories > maxVisited {
		return &Error{Code: CodeConflict, Message: "Worktree destination would exceed discovery bounds"}
	}
	return nil
}

func validatePromotionCapacity(metrics discoveryMetrics, missingAncestors int) error {
	if metrics.Truncated || metrics.Visited+missingAncestors > maxVisited {
		return &Error{Code: CodeConflict, Message: "Worktree destination ancestors would exceed discovery bounds"}
	}
	return nil
}

func (l *Lifecycle) findAddRecovery(target, selectorHash, repositoryHash string) (operationRecord, bool, error) {
	entries, err := os.ReadDir(l.state)
	if errors.Is(err, os.ErrNotExist) {
		return operationRecord{}, false, nil
	}
	if err != nil {
		return operationRecord{}, false, err
	}
	var matched operationRecord
	found := false
	for _, entry := range entries {
		if entry.IsDir() || !isIntentRecordName(entry.Name()) {
			continue
		}
		candidate := strings.TrimSuffix(entry.Name(), ".json")
		record, present, loadErr := l.load(candidate)
		if loadErr != nil {
			return operationRecord{}, false, loadErr
		}
		if !present || record.Kind != "worktree_add" || record.TargetPath != target || record.RefSHA256 != selectorHash || record.RepositorySHA256 != repositoryHash || record.CommonDirectory == "" {
			continue
		}
		if found {
			return operationRecord{}, false, &Error{Code: CodeConflict, Message: "multiple Worktree add checkpoints match the missing repository path"}
		}
		matched, found = record, true
	}
	return matched, found, nil
}

func (l *Lifecycle) findRemoveRecovery(target string) (operationRecord, bool, error) {
	entries, err := os.ReadDir(l.state)
	if errors.Is(err, os.ErrNotExist) {
		return operationRecord{}, false, nil
	}
	if err != nil {
		return operationRecord{}, false, err
	}
	var latest operationRecord
	found := false
	for _, entry := range entries {
		if entry.IsDir() || !isIntentRecordName(entry.Name()) {
			continue
		}
		record, present, loadErr := l.load(strings.TrimSuffix(entry.Name(), ".json"))
		if loadErr != nil {
			return operationRecord{}, false, loadErr
		}
		if !present || record.Kind != "worktree_remove" || record.TargetPath != target || record.Checkpoint != "complete" && record.Checkpoint != "remove_pending" {
			continue
		}
		if !found || record.UpdatedAt.After(latest.UpdatedAt) {
			latest, found = record, true
		}
	}
	return latest, found, nil
}

func catalogDiscoveryTruncated(catalog Catalog) bool {
	for _, warning := range catalog.Warnings {
		if strings.Contains(warning.Message, "filesystem entry limit") || strings.Contains(warning.Message, "checkout candidate limit") || strings.Contains(warning.Message, "catalog output limit") {
			return true
		}
	}
	return false
}

func (l *Lifecycle) reconcileRemoved(ctx context.Context, target string, record *operationRecord, recovered bool) (MutationResult, error) {
	if record.CommonDirectory == "" {
		return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "Worktree removal checkpoint has no repository relationship"}
	}
	registered, err := l.registeredWorktree(ctx, record.CommonDirectory, target)
	if err != nil {
		return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "Worktree removal could not be reconciled against Git registration", Cause: err}
	}
	if registered {
		return MutationResult{}, &Error{Code: CodeOutcomeUnknown, Message: "Worktree path is absent but remains registered with Git; prune or repair it before retrying"}
	}
	record.Checkpoint = "complete"
	if err = l.saveAfterEffect(record); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{Action: "worktree_remove", Recovered: recovered, WorktreeRoot: l.root, Path: target}, nil
}

func (l *Lifecycle) registeredWorktree(ctx context.Context, commonDirectory, target string) (bool, error) {
	result, err := l.runGit(ctx, "--git-dir", commonDirectory, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, err
	}
	if result.Truncated {
		return false, &Error{Code: CodeOutcomeUnknown, Message: "Git worktree registration output exceeded the verification limit"}
	}
	members, err := parseWorktreeList(result.Stdout)
	if err != nil {
		return false, err
	}
	for _, member := range members {
		if !member.bare && member.path == target {
			return true, nil
		}
	}
	return false, nil
}

func (l *Lifecycle) worktreeLockState(ctx context.Context, commonDirectory, target string) (bool, bool, error) {
	result, err := l.runGit(ctx, "--git-dir", commonDirectory, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, false, err
	}
	if result.Truncated {
		return false, false, &Error{Code: CodeOutcomeUnknown, Message: "Git worktree lock output exceeded the verification limit"}
	}
	members, err := parseWorktreeList(result.Stdout)
	if err != nil {
		return false, false, err
	}
	for _, member := range members {
		if !member.bare && member.path == target {
			return true, member.locked, nil
		}
	}
	return false, false, nil
}

func (l *Lifecycle) unlockStagedWorktree(ctx context.Context, commonDirectory, target string) error {
	if _, err := l.runGit(ctx, "--git-dir", commonDirectory, "worktree", "unlock", target); err != nil {
		registered, locked, stateErr := l.worktreeLockState(ctx, commonDirectory, target)
		if stateErr != nil {
			return &Error{Code: CodeOutcomeUnknown, Message: "staged Worktree lock state could not be reconciled", Cause: stateErr}
		}
		if !registered || locked {
			return err
		}
	}
	return nil
}

func (l *Lifecycle) newPath(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || hasControl(value) {
		return "", &Error{Code: CodeInvalidInput, Message: "Worktree path is required"}
	}
	var target string
	if filepath.IsAbs(value) {
		if filepath.Clean(value) != value {
			return "", &Error{Code: CodeInvalidInput, Message: "absolute Worktree path must be canonical"}
		}
		target = value
	} else {
		if filepath.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
			return "", &Error{Code: CodeInvalidInput, Message: "Worktree path must be an exact root-relative path"}
		}
		target = filepath.Join(l.root, value)
	}
	if !within(l.root, target) || target == l.root {
		return "", &Error{Code: CodeInvalidInput, Message: "Worktree path escapes the configured root"}
	}
	ancestor := target
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(ancestor)
			if resolveErr != nil || resolved != ancestor || !within(l.root, resolved) {
				return "", &Error{Code: CodeInvalidInput, Message: "Worktree path must not resolve through symlinks", Cause: resolveErr}
			}
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", &Error{Code: CodeInvalidInput, Message: "Worktree path has no valid ancestor"}
		}
		ancestor = parent
	}
	return target, nil
}

func validateCloneSource(raw string) (string, string, error) {
	if raw == "" || !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw || strings.HasPrefix(raw, "-") || hasControl(raw) {
		return "", "", &Error{Code: CodeInvalidInput, Message: "Git repository source is invalid"}
	}
	nameSource := raw
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.User != nil && parsed.Scheme != "ssh" {
			return "", "", &Error{Code: CodeInvalidInput, Message: "Git repository source must not contain embedded credentials, query parameters, or fragments"}
		}
		if parsed.User != nil {
			if _, present := parsed.User.Password(); present {
				return "", "", &Error{Code: CodeInvalidInput, Message: "Git repository source must not contain embedded credentials"}
			}
		}
		nameSource = parsed.Path
	} else if strings.Contains(raw, "://") {
		return "", "", &Error{Code: CodeInvalidInput, Message: "Git repository source URL is malformed"}
	} else if colon := strings.IndexByte(raw, ':'); colon > 0 && !strings.ContainsRune(raw[:colon], filepath.Separator) {
		nameSource = raw[colon+1:]
	}
	nameSource = strings.TrimRight(nameSource, "/"+string(filepath.Separator))
	name := filepath.Base(nameSource)
	name = strings.TrimSuffix(name, ".git")
	if name == "" || name == "." || name == ".." || hasControl(name) {
		return "", "", &Error{Code: CodeInvalidInput, Message: "Git repository source has no safe destination name"}
	}
	return raw, name, nil
}

func validateRef(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 1024 || !utf8.ValidString(value) || hasControl(value) || strings.HasPrefix(value, "-") {
		return &Error{Code: CodeInvalidInput, Message: "Git branch or tag selector is invalid"}
	}
	return nil
}

func dirty(status Status) bool {
	return status.Staged != 0 || status.Unstaged != 0 || status.Untracked != 0 || status.Conflicted != 0 || status.Ignored != 0
}

func firstLivePath(value Repository) string {
	if value.Primary != nil {
		return value.Primary.Path
	}
	if len(value.Linked) != 0 {
		return value.Linked[0].Path
	}
	return ""
}

func fingerprint(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (l *Lifecycle) recordPath(intent string) string { return filepath.Join(l.state, intent+".json") }

func (l *Lifecycle) load(intent string) (operationRecord, bool, error) {
	path := l.recordPath(intent)
	info, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return operationRecord{}, false, nil
	}
	if statErr != nil {
		return operationRecord{}, false, fmt.Errorf("inspect Git operation checkpoint: %w", statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return operationRecord{}, false, &Error{Code: CodeOutcomeUnknown, Message: "Git operation checkpoint is not a regular file"}
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return operationRecord{}, false, nil
	}
	if err != nil {
		return operationRecord{}, false, fmt.Errorf("open Git operation checkpoint: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(bufio.NewReader(file))
	decoder.DisallowUnknownFields()
	var record operationRecord
	if err = decoder.Decode(&record); err != nil {
		return operationRecord{}, false, &Error{Code: CodeOutcomeUnknown, Message: "Git operation checkpoint is malformed", Cause: err}
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return operationRecord{}, false, &Error{Code: CodeOutcomeUnknown, Message: "Git operation checkpoint contains trailing data", Cause: err}
	}
	if record.SchemaVersion != operationSchemaVersion || record.IntentSHA256 != intent || record.ID == "" || record.TargetPath == "" {
		return operationRecord{}, false, &Error{Code: CodeOutcomeUnknown, Message: "Git operation checkpoint is incompatible"}
	}
	return record, true, nil
}

func (l *Lifecycle) save(record *operationRecord) error {
	if err := ensurePrivateDirectory(l.state); err != nil {
		return fmt.Errorf("create Git operation checkpoint directory: %w", err)
	}
	record.UpdatedAt = l.now().UTC()
	temporary, err := os.CreateTemp(l.state, ".operation-*.json")
	if err != nil {
		return fmt.Errorf("create Git operation checkpoint: %w", err)
	}
	path := temporary.Name()
	defer func() { _ = os.Remove(path) }()
	if err = temporary.Chmod(0o600); err == nil {
		err = json.NewEncoder(temporary).Encode(record)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return fmt.Errorf("write Git operation checkpoint: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close Git operation checkpoint: %w", closeErr)
	}
	if err = os.Rename(path, l.recordPath(record.IntentSHA256)); err != nil {
		return fmt.Errorf("replace Git operation checkpoint: %w", err)
	}
	directory, err := os.Open(l.state)
	if err != nil {
		return fmt.Errorf("open Git operation checkpoint directory: %w", err)
	}
	syncErr := directory.Sync()
	closeDirectoryErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync Git operation checkpoint directory: %w", syncErr)
	}
	if closeDirectoryErr != nil {
		return fmt.Errorf("close Git operation checkpoint directory: %w", closeDirectoryErr)
	}
	return nil
}

func (l *Lifecycle) saveAfterEffect(record *operationRecord) error {
	if err := l.save(record); err != nil {
		return &Error{Code: CodeOutcomeUnknown, Message: "Git effect was verified but its recovery checkpoint could not be saved", Cause: err}
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a private directory", path)
	}
	return os.Chmod(path, 0o700)
}

func (l *Lifecycle) incompleteConflict(target, intent string) (bool, error) {
	entries, err := os.ReadDir(l.state)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !isIntentRecordName(entry.Name()) || entry.Name() == intent+".json" {
			continue
		}
		candidate := strings.TrimSuffix(entry.Name(), ".json")
		record, found, loadErr := l.load(candidate)
		if loadErr != nil {
			return false, loadErr
		}
		if found && record.TargetPath == target && record.Checkpoint != "complete" {
			return true, nil
		}
	}
	return false, nil
}

func isIntentRecordName(name string) bool {
	if !strings.HasSuffix(name, ".json") {
		return false
	}
	value := strings.TrimSuffix(name, ".json")
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
