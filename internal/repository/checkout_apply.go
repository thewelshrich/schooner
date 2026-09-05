package repository

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/thewelshrich/schooner/internal/process"
)

type ExtractedCheckout struct {
	State       CheckoutState
	Directory   string
	ObjectsPack string
}

// checkoutIncarnation pins the directory identities behind a Worktree path.
// An open Root protects file operations from path replacement; Git remains
// path-addressed, so each Git mutation is bracketed by this identity check.
type checkoutIncarnation struct {
	worktree     os.FileInfo
	gitPath      string
	gitDirectory os.FileInfo
}

func captureCheckoutIncarnation(ctx context.Context, target string) (*checkoutIncarnation, error) {
	worktree, err := os.Stat(target)
	if err != nil || !worktree.IsDir() {
		return nil, &Error{Code: CodeConflict, Message: "destination Worktree directory cannot be pinned", Cause: err}
	}
	gitOutput, err := git(ctx, commandRunner{}, target, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, &Error{Code: CodeConflict, Message: "destination Git directory cannot be resolved", Cause: err}
	}
	gitPath, err := filepath.EvalSymlinks(filepath.Clean(strings.TrimSpace(string(gitOutput))))
	if err != nil || !filepath.IsAbs(gitPath) {
		return nil, &Error{Code: CodeConflict, Message: "destination Git directory cannot be canonicalized", Cause: err}
	}
	gitDirectory, err := os.Stat(gitPath)
	if err != nil || !gitDirectory.IsDir() {
		return nil, &Error{Code: CodeConflict, Message: "destination Git directory cannot be pinned", Cause: err}
	}
	return &checkoutIncarnation{worktree: worktree, gitPath: gitPath, gitDirectory: gitDirectory}, nil
}

func (incarnation *checkoutIncarnation) Validate(ctx context.Context, target string) error {
	if incarnation == nil {
		return &Error{Code: CodeConflict, Message: "destination Worktree incarnation is unavailable"}
	}
	worktree, err := os.Stat(target)
	if err != nil || !os.SameFile(incarnation.worktree, worktree) {
		return &Error{Code: CodeConflict, Message: "destination Worktree directory changed during workspace application", Cause: err}
	}
	gitOutput, err := git(ctx, commandRunner{}, target, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return &Error{Code: CodeConflict, Message: "destination Git directory changed during workspace application", Cause: err}
	}
	gitPath, err := filepath.EvalSymlinks(filepath.Clean(strings.TrimSpace(string(gitOutput))))
	if err != nil || gitPath != incarnation.gitPath {
		return &Error{Code: CodeConflict, Message: "destination Git directory changed during workspace application", Cause: err}
	}
	gitDirectory, err := os.Stat(gitPath)
	if err != nil || !os.SameFile(incarnation.gitDirectory, gitDirectory) {
		return &Error{Code: CodeConflict, Message: "destination Git directory incarnation changed during workspace application", Cause: err}
	}
	return nil
}

type preparedCheckoutIndex struct {
	root       *os.Root
	lock       *os.File
	lockName   string
	indexName  string
	backupName string
	temporary  string
	promoted   bool
	ownsLock   bool
}

func prepareCheckoutIndex(ctx context.Context, target string, payload ExtractedCheckout) (*preparedCheckoutIndex, error) {
	gitIndex, err := git(ctx, commandRunner{}, target, "rev-parse", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	indexPath := filepath.Clean(strings.TrimSpace(string(gitIndex)))
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(target, indexPath)
	}
	indexParent, err := filepath.EvalSymlinks(filepath.Dir(indexPath))
	if err != nil {
		return nil, &Error{Code: CodeConflict, Message: "destination Git index parent cannot be canonicalized", Cause: err}
	}
	indexRoot, err := os.OpenRoot(indexParent)
	if err != nil {
		return nil, &Error{Code: CodeConflict, Message: "destination Git index parent cannot be opened safely", Cause: err}
	}
	prepared := &preparedCheckoutIndex{root: indexRoot, indexName: filepath.Base(indexPath), lockName: filepath.Base(indexPath) + ".lock"}
	failed := true
	defer func() {
		if failed {
			prepared.Release()
		}
	}()
	temporary, err := os.CreateTemp(payload.Directory, ".index-*")
	if err != nil {
		return nil, err
	}
	prepared.temporary = temporary.Name()
	if err = temporary.Close(); err != nil {
		return nil, err
	}
	if err = os.Remove(prepared.temporary); err != nil {
		return nil, err
	}
	indexEnvironment := []string{"GIT_INDEX_FILE=" + prepared.temporary}
	var result process.Result
	if len(payload.State.IndexEntries) == 0 {
		result, err = process.RunCapturedWithoutEnvironment(ctx, 64<<10, gitRepositoryEnvironment, indexEnvironment, "git", "--no-optional-locks", "-c", "core.fsmonitor=false", "-C", target, "read-tree", "--empty")
	} else {
		var index bytes.Buffer
		for _, entry := range payload.State.IndexEntries {
			_, _ = fmt.Fprintf(&index, "%s %s\t%s%c", entry.Mode, entry.Object, entry.Path, byte(0))
		}
		result, err = process.RunStreamingWithoutEnvironment(ctx, target, gitRepositoryEnvironment, indexEnvironment, &index, io.Discard, "git", "--no-optional-locks", "-c", "core.fsmonitor=false", "update-index", "-z", "--index-info")
	}
	if err != nil {
		return nil, gitTransferError("prepare workspace index", result, err)
	}
	prepared.lock, err = prepared.root.OpenFile(prepared.lockName, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, &Error{Code: CodeConflict, Message: "destination Git index is busy", Cause: err}
	}
	prepared.ownsLock = true
	input, err := os.Open(prepared.temporary)
	if err != nil {
		return nil, err
	}
	_, copyErr := io.Copy(prepared.lock, input)
	if closeErr := input.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil {
		copyErr = prepared.lock.Sync()
	}
	if closeErr := prepared.lock.Close(); copyErr == nil {
		copyErr = closeErr
	}
	prepared.lock = nil
	if copyErr != nil {
		return nil, copyErr
	}
	backupName, err := checkoutTemporaryPath(prepared.indexName)
	if err != nil {
		return nil, err
	}
	prepared.backupName = backupName
	if err = prepared.root.Link(prepared.indexName, prepared.backupName); errors.Is(err, os.ErrNotExist) {
		prepared.backupName = ""
	} else if err != nil {
		return nil, &Error{Code: CodeUnsupported, Message: "destination Git index cannot be protected for rollback", Cause: err}
	}
	failed = false
	return prepared, nil
}

func (prepared *preparedCheckoutIndex) Promote() error {
	if err := prepared.root.Rename(prepared.lockName, prepared.indexName); err != nil {
		return err
	}
	// The rename consumes Schooner's lock path. Another Git process may create
	// a new index.lock immediately, so Release must never unlink by name after
	// ownership has been relinquished.
	prepared.ownsLock = false
	prepared.promoted = true
	return nil
}

func (prepared *preparedCheckoutIndex) Restore(ctx context.Context, target string, expected []CheckoutIndexEntry) error {
	if !prepared.promoted {
		return nil
	}
	lock, err := prepared.root.OpenFile(prepared.lockName, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	prepared.ownsLock = true
	if closeErr := lock.Close(); closeErr != nil {
		_ = prepared.root.Remove(prepared.lockName)
		prepared.ownsLock = false
		return closeErr
	}
	current, currentErr := checkoutIndexEntries(ctx, target)
	if currentErr != nil || !slices.Equal(current, expected) {
		_ = prepared.root.Remove(prepared.lockName)
		prepared.ownsLock = false
		if currentErr != nil {
			return currentErr
		}
		return &Error{Code: CodeConflict, Message: "destination Git index changed independently before rollback"}
	}
	if prepared.backupName == "" {
		err = prepared.root.Remove(prepared.indexName)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		err = prepared.root.Rename(prepared.backupName, prepared.indexName)
		if err == nil {
			prepared.backupName = ""
		}
	}
	removeErr := prepared.root.Remove(prepared.lockName)
	prepared.ownsLock = false
	if err == nil && !errors.Is(removeErr, os.ErrNotExist) {
		err = removeErr
	}
	if err == nil {
		prepared.promoted = false
	}
	return err
}

func (prepared *preparedCheckoutIndex) Release() {
	if prepared == nil {
		return
	}
	if prepared.lock != nil {
		_ = prepared.lock.Close()
		prepared.lock = nil
	}
	if prepared.root != nil {
		if prepared.ownsLock {
			_ = prepared.root.Remove(prepared.lockName)
			prepared.ownsLock = false
		}
		if prepared.backupName != "" {
			_ = prepared.root.Remove(prepared.backupName)
		}
		_ = prepared.root.Close()
		prepared.root = nil
	}
	if prepared.temporary != "" {
		_ = os.Remove(prepared.temporary)
	}
}

// checkoutApplyError records that application reached a mutation of the
// destination checkout. Callers that keep rollback material must not restore
// it for pre-mutation conflicts: doing so could overwrite the concurrent
// change that caused the conflict.
type checkoutApplyError struct {
	cause error
}

func (e *checkoutApplyError) Error() string { return e.cause.Error() }
func (e *checkoutApplyError) Unwrap() error { return e.cause }

// CheckoutMutationStarted reports whether an apply error happened after the
// destination checkout began changing.
func CheckoutMutationStarted(err error) bool {
	var target *checkoutApplyError
	return errors.As(err, &target)
}

func afterCheckoutMutation(err error) error {
	if err == nil || CheckoutMutationStarted(err) {
		return err
	}
	return &checkoutApplyError{cause: err}
}

func ExtractCheckoutPayload(payloadPath, stagingDirectory string) (ExtractedCheckout, error) {
	if !filepath.IsAbs(payloadPath) || !filepath.IsAbs(stagingDirectory) {
		return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload paths must be absolute"}
	}
	if err := os.MkdirAll(stagingDirectory, 0o700); err != nil {
		return ExtractedCheckout{}, err
	}
	directory, err := os.MkdirTemp(stagingDirectory, "apply-*")
	if err != nil {
		return ExtractedCheckout{}, err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(directory)
		}
	}()
	file, err := os.Open(payloadPath)
	if err != nil {
		return ExtractedCheckout{}, err
	}
	defer file.Close()
	reader := tar.NewReader(file)
	var metadata checkoutPayloadMetadata
	seen := map[string]bool{}
	manifest := map[string]CheckoutFile{}
	objectsPack := filepath.Join(directory, "objects.pack")
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload is malformed", Cause: nextErr}
		}
		if seen[header.Name] {
			return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload contains duplicate entries"}
		}
		seen[header.Name] = true
		switch header.Name {
		case "metadata.json":
			if len(seen) != 1 || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 64<<20 {
				return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload metadata is invalid"}
			}
			decoder := json.NewDecoder(io.LimitReader(reader, header.Size))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&metadata); err != nil || metadata.SchemaVersion != CheckoutSchemaVersion || metadata.State.SchemaVersion != CheckoutSchemaVersion {
				return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload metadata is incompatible", Cause: err}
			}
			digest, digestErr := checkoutDigest(metadata.State)
			revalidation, revalidationErr := checkoutRevalidationDigest(metadata.State)
			if digestErr != nil || digest != metadata.State.Digest || revalidationErr != nil || revalidation != metadata.State.RevalidationDigest {
				return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload state digest is invalid", Cause: digestErr}
			}
			for _, entry := range metadata.State.Files {
				if validateErr := validateCheckoutPath(entry.Path); validateErr != nil || manifest[entry.Path].Path != "" || (entry.Kind != "file" && entry.Kind != "symlink") || entry.Size < 0 || len(entry.SHA256) != 64 {
					return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload manifest is invalid", Cause: validateErr}
				}
				manifest[entry.Path] = entry
			}
			if metadata.State.FileCount != len(metadata.State.Files) {
				return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload file count is invalid"}
			}
			if metadata.State.IndexCount != len(metadata.State.IndexEntries) {
				return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload index count is invalid"}
			}
			if metadata.State.AbsentCount != len(metadata.State.AbsentPaths) {
				return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload absent-path count is invalid"}
			}
			absentPaths := make(map[string]struct{}, len(metadata.State.AbsentPaths))
			for _, path := range metadata.State.AbsentPaths {
				if validateErr := validateCheckoutPath(path); validateErr != nil {
					return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload absent path is invalid", Cause: validateErr}
				}
				if _, duplicate := absentPaths[path]; duplicate {
					return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload contains duplicate absent paths"}
				}
				absentPaths[path] = struct{}{}
			}
			indexPaths := make(map[string]struct{}, len(metadata.State.IndexEntries))
			for _, entry := range metadata.State.IndexEntries {
				if validateErr := validateCheckoutPath(entry.Path); validateErr != nil || !validIndexMode(entry.Mode) || !validGitObjectID(entry.Object) {
					return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload index is invalid", Cause: validateErr}
				}
				if _, duplicate := indexPaths[entry.Path]; duplicate {
					return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload index contains duplicate paths"}
				}
				indexPaths[entry.Path] = struct{}{}
			}
			for path := range absentPaths {
				if _, indexed := indexPaths[path]; !indexed {
					return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload absent path is not present in its index"}
				}
				if _, present := manifest[path]; present {
					return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload path cannot be both present and absent"}
				}
			}
			for path := range indexPaths {
				_, present := manifest[path]
				_, absent := absentPaths[path]
				if !present && !absent {
					return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload is missing an indexed path state"}
				}
			}
		case "objects.pack":
			if metadata.SchemaVersion == "" || header.Typeflag != tar.TypeReg || header.Size < 0 {
				return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload Git pack is invalid"}
			}
			if err = copyPayloadRegular(reader, objectsPack, header.Size, 0o600, ""); err != nil {
				return ExtractedCheckout{}, err
			}
		default:
			if metadata.SchemaVersion == "" || !strings.HasPrefix(header.Name, "files/") {
				return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload contains an unexpected entry"}
			}
			relative := strings.TrimPrefix(header.Name, "files/")
			if validateErr := validateCheckoutPath(relative); validateErr != nil {
				return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload file path is invalid", Cause: validateErr}
			}
			entry, ok := manifest[relative]
			if !ok {
				return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload file is absent from its manifest"}
			}
			target := filepath.Join(directory, "files", filepath.FromSlash(relative))
			if err = ensurePayloadParents(filepath.Join(directory, "files"), filepath.Dir(target)); err != nil {
				return ExtractedCheckout{}, err
			}
			if entry.Kind == "file" {
				if header.Typeflag != tar.TypeReg || header.Size != entry.Size {
					return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload file metadata does not match its manifest"}
				}
				mode := os.FileMode(0o644)
				if entry.Executable {
					mode = 0o755
				}
				if err = copyPayloadRegular(reader, target, header.Size, mode, entry.SHA256); err != nil {
					return ExtractedCheckout{}, err
				}
			} else {
				if header.Typeflag != tar.TypeSymlink || int64(len(header.Linkname)) != entry.Size {
					return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload symlink metadata does not match its manifest"}
				}
				digest := sha256.Sum256([]byte(header.Linkname))
				if hex.EncodeToString(digest[:]) != entry.SHA256 {
					return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload symlink digest does not match its manifest"}
				}
				// Keep extracted payloads inert. The symlink target is stored in a
				// regular staging file and materialized only when the checkout entry
				// is promoted through a destination-scoped os.Root.
				if err = copyPayloadRegular(strings.NewReader(header.Linkname), target, entry.Size, 0o600, entry.SHA256); err != nil {
					return ExtractedCheckout{}, err
				}
			}
			delete(manifest, relative)
		}
	}
	if metadata.SchemaVersion == "" || !seen["objects.pack"] || len(manifest) != 0 {
		return ExtractedCheckout{}, &Error{Code: CodeInvalidInput, Message: "workspace payload is incomplete"}
	}
	failed = false
	return ExtractedCheckout{State: metadata.State, Directory: directory, ObjectsPack: objectsPack}, nil
}

func (value ExtractedCheckout) Release() { _ = os.RemoveAll(value.Directory) }

func ApplyCheckout(ctx context.Context, target string, payload ExtractedCheckout) (CheckoutState, error) {
	return applyCheckout(ctx, filepath.Dir(target), target, payload, "", "")
}

// ApplyCheckoutWithinRoot applies a checkout while constraining first-create
// promotion to a verified Worktree Root.
func ApplyCheckoutWithinRoot(ctx context.Context, root, target string, payload ExtractedCheckout) (CheckoutState, error) {
	return applyCheckout(ctx, root, target, payload, "", "")
}

// ApplyCheckoutIfUnchanged applies only when the existing destination still
// matches the state observed during planning.
func ApplyCheckoutIfUnchanged(ctx context.Context, target string, payload ExtractedCheckout, expectedDigest string) (CheckoutState, error) {
	if expectedDigest == "" {
		return CheckoutState{}, &Error{Code: CodeInvalidInput, Message: "expected destination checkout digest is required"}
	}
	return applyCheckout(ctx, filepath.Dir(target), target, payload, expectedDigest, "")
}

// ApplyCheckoutIfOperationCreatedAndUnchanged applies a workspace to a clean
// clone created and exactly revalidated by the current push. Its branch may be
// rewound to the authoritative source because the clone seed is operation
// output, not pre-existing destination work.
func ApplyCheckoutIfOperationCreatedAndUnchanged(ctx context.Context, target string, payload ExtractedCheckout, expectedDigest, createdBranch string) (CheckoutState, error) {
	if expectedDigest == "" {
		return CheckoutState{}, &Error{Code: CodeInvalidInput, Message: "expected operation-created checkout digest is required"}
	}
	return applyCheckout(ctx, filepath.Dir(target), target, payload, expectedDigest, createdBranch)
}

// CheckoutTransactionOptions configures the shared existing-Worktree
// transaction used by directional workspace transfers.
type CheckoutTransactionOptions struct {
	ExpectedStateDigest    string
	LockStateDirectory     string
	StagingDirectory       string
	OperationCreatedBranch string
}

// ApplyCheckoutTransaction applies an extracted checkout to an existing
// Worktree while holding Schooner's mutation lock. It reserves a complete
// rollback capture before application and restores it after any failure that
// reached destination mutation. Uncertain recovery retains the capture for
// manual recovery rather than deleting the remaining backup.
func ApplyCheckoutTransaction(ctx context.Context, target string, payload ExtractedCheckout, options CheckoutTransactionOptions) (CheckoutState, error) {
	if options.ExpectedStateDigest == "" || options.LockStateDirectory == "" || options.StagingDirectory == "" {
		return CheckoutState{}, &Error{Code: CodeInvalidInput, Message: "workspace transaction is not configured"}
	}
	lock, err := AcquireWorktreeMutationLock(options.LockStateDirectory, target)
	if err != nil {
		return CheckoutState{}, err
	}
	defer lock.Close()

	current, err := ObserveCheckout(ctx, target)
	if err != nil {
		return CheckoutState{}, err
	}
	if current.RevalidationDigest != options.ExpectedStateDigest {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "the destination Worktree changed after workspace preflight"}
	}
	backup, err := CaptureCheckout(ctx, target, options.StagingDirectory)
	if err != nil {
		return CheckoutState{}, err
	}
	releaseBackup := true
	defer func() {
		if releaseBackup {
			backup.Release()
		}
	}()
	current, err = ObserveCheckout(ctx, target)
	if err != nil {
		return CheckoutState{}, err
	}
	if current.RevalidationDigest != options.ExpectedStateDigest {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "the destination Worktree changed while rollback state was being prepared"}
	}

	var applied CheckoutState
	if options.OperationCreatedBranch != "" {
		applied, err = ApplyCheckoutIfOperationCreatedAndUnchanged(ctx, target, payload, options.ExpectedStateDigest, options.OperationCreatedBranch)
	} else {
		applied, err = ApplyCheckoutIfUnchanged(ctx, target, payload, options.ExpectedStateDigest)
	}
	if err == nil || !CheckoutMutationStarted(err) {
		return applied, err
	}
	releaseBackup = false // Recovery owns the capture and retains it on uncertainty.
	return restoreCheckoutTransaction(ctx, target, backup, payload, options.StagingDirectory, err)
}

func restoreCheckoutTransaction(ctx context.Context, target string, backup CheckoutCapture, incoming ExtractedCheckout, stagingDirectory string, applyErr error) (CheckoutState, error) {
	if ErrorCode(applyErr) == CodeOutcomeUnknown {
		return CheckoutState{}, &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("workspace recovery remains incomplete; captured backup preserved at %q for manual recovery", backup.PayloadPath), Cause: applyErr}
	}
	restore, restoreErr := ExtractCheckoutPayload(backup.PayloadPath, stagingDirectory)
	if restoreErr == nil {
		defer restore.Release()
		_, restoreErr = RestoreCheckoutAfterFailedApply(context.WithoutCancel(ctx), target, restore, incoming)
	}
	if restoreErr != nil {
		return CheckoutState{}, &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("workspace recovery remains incomplete; captured backup preserved at %q for manual recovery", backup.PayloadPath), Cause: errors.Join(applyErr, restoreErr)}
	}
	backup.Release()
	return CheckoutState{}, applyErr
}

// RestoreCheckoutAfterFailedApply restores a captured destination after an
// apply that reached checkout mutation. It first proves that every path the
// operation could have touched is either absent, still equal to the incoming
// payload, or still equal to the backup. This prevents rollback from erasing
// an editor change made while the operation was failing.
func RestoreCheckoutAfterFailedApply(ctx context.Context, target string, backup, incoming ExtractedCheckout) (CheckoutState, error) {
	currentState, err := ObserveCheckout(ctx, target)
	if err != nil {
		return CheckoutState{}, err
	}
	headKnown := checkoutHeadEqual(currentState, backup.State) || checkoutHeadEqual(currentState, incoming.State)
	indexKnown := checkoutIndexEqual(currentState, backup.State) || checkoutIndexEqual(currentState, incoming.State)
	if !headKnown || !indexKnown {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "destination Git HEAD or index changed independently while workspace application was being rolled back"}
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "destination Worktree could not be opened safely for rollback", Cause: err}
	}
	defer root.Close()
	backupFiles := checkoutFileMap(backup.State.Files)
	incomingFiles := checkoutFileMap(incoming.State.Files)
	paths := make(map[string]bool, len(backupFiles)+len(incomingFiles))
	for path := range backupFiles {
		paths[path] = true
	}
	for path := range incomingFiles {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	managedDirectories := checkoutManagedDirectories(paths)

	for _, path := range ordered {
		current, present, err := checkoutFileForTransition(root, path, paths, managedDirectories)
		if err != nil {
			return CheckoutState{}, err
		}
		if !present || checkoutFileContentEqual(current, backupFiles[path]) || checkoutFileContentEqual(current, incomingFiles[path]) {
			continue
		}
		return CheckoutState{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("destination path %q changed independently while workspace application was being rolled back", path)}
	}

	// The capture records leaves, not directory ownership or permissions.
	// Refuse before deleting anything if applying the backup would invent them.
	backupPaths := make(map[string]bool, len(backupFiles))
	for path := range backupFiles {
		backupPaths[path] = true
	}
	var backupDirectories []string
	for path := range checkoutManagedDirectories(backupPaths) {
		backupDirectories = append(backupDirectories, path)
	}
	sort.Strings(backupDirectories) // Parents first, so symlinks are never followed.
	for _, path := range backupDirectories {
		info, err := root.Lstat(filepath.FromSlash(path))
		if err != nil || !info.IsDir() {
			return CheckoutState{}, &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("cannot recreate directory %q: original directory metadata was not captured; backup preserved at %q for manual recovery", path, backup.Directory), Cause: err}
		}
	}

	// Expected incoming chains may remain as empty directories after their
	// leaves have already been removed by an interrupted inner rollback.
	recoveryFiles := checkoutFileMap(currentState.Files)
	for path, entry := range incomingFiles {
		recoveryFiles[path] = entry
	}
	replacementRoots := checkoutFileMap(backup.State.Files)
	for _, path := range backup.State.AbsentPaths {
		replacementRoots[path] = CheckoutFile{Path: path}
	}
	directories, err := pinReplacedCheckoutDirectories(root, recoveryFiles, replacementRoots)
	if err != nil {
		return CheckoutState{}, err
	}
	cleanup := preparedCheckoutFiles{root: root, removedDirs: directories}

	for _, path := range ordered {
		if _, existed := backupFiles[path]; existed {
			continue
		}
		current, present, err := checkoutFileForTransition(root, path, paths, managedDirectories)
		if err != nil {
			return CheckoutState{}, err
		}
		if !present {
			continue
		}
		if !checkoutFileContentEqual(current, incomingFiles[path]) {
			return CheckoutState{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("destination path %q changed independently while workspace application was being rolled back", path)}
		}
		if err = root.Remove(filepath.FromSlash(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return CheckoutState{}, err
		}
	}

	for path := range replacementRoots {
		if err = cleanup.removeReplacedDirectories(path); err != nil {
			return CheckoutState{}, err
		}
	}

	current, err := ObserveCheckout(ctx, target)
	if err != nil {
		return CheckoutState{}, err
	}
	if !checkoutHeadEqual(current, currentState) || !checkoutIndexEqual(current, currentState) {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "destination Git HEAD or index changed during workspace rollback"}
	}
	// Rewind only the currently checked-out, known operation branch. A different
	// backup branch still receives the ordinary independent-history check.
	rewindBranch := ""
	if !current.Detached && current.Branch == backup.State.Branch {
		rewindBranch = current.Branch
	}
	return applyCheckout(ctx, filepath.Dir(target), target, backup, current.RevalidationDigest, rewindBranch)
}

func checkoutHeadEqual(left, right CheckoutState) bool {
	return left.HEAD == right.HEAD && left.Branch == right.Branch && left.Detached == right.Detached
}

func checkoutIndexEqual(left, right CheckoutState) bool {
	return slices.Equal(left.IndexEntries, right.IndexEntries)
}

func checkoutFileMap(files []CheckoutFile) map[string]CheckoutFile {
	values := make(map[string]CheckoutFile, len(files))
	for _, entry := range files {
		values[entry.Path] = entry
	}
	return values
}

func checkoutFileOnRoot(root *os.Root, relative string) (CheckoutFile, bool, error) {
	if err := validateCheckoutDestinationParents(root, relative); err != nil {
		return CheckoutFile{}, false, err
	}
	info, err := root.Lstat(filepath.FromSlash(relative))
	if errors.Is(err, os.ErrNotExist) {
		return CheckoutFile{}, false, nil
	}
	if err != nil {
		return CheckoutFile{}, false, err
	}
	entry, err := checkoutFileAtRoot(root, relative, false, info)
	return entry, err == nil, err
}

func validateCheckoutDestinationParents(root *os.Root, relative string) error {
	parent := filepath.Dir(filepath.FromSlash(relative))
	if parent == "." {
		return nil
	}
	current := ""
	for _, component := range strings.Split(parent, string(filepath.Separator)) {
		if current == "" {
			current = component
		} else {
			current = filepath.Join(current, component)
		}
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination parent for %q cannot be inspected safely", relative), Cause: err}
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination parent for %q is not a real directory", relative)}
		}
	}
	return nil
}

func checkoutManagedDirectories(tracked map[string]bool) map[string]bool {
	directories := make(map[string]bool)
	for leaf, managed := range tracked {
		if !managed {
			continue
		}
		for parent := filepath.ToSlash(filepath.Dir(leaf)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if directories[parent] {
				break
			}
			directories[parent] = true
		}
	}
	return directories
}

// checkoutFileForTransition treats a tracked parent or a directory containing
// only tracked leaves as an outgoing path shape, never as an ignored collision.
// The ordinary reader remains strict for mutation and displacement verification.
func checkoutFileForTransition(root *os.Root, relative string, tracked, directories map[string]bool) (CheckoutFile, bool, error) {
	return checkoutFileForTransitionPreflight(root, relative, tracked, directories, false)
}

func checkoutFileForTransitionPreflight(root *os.Root, relative string, tracked, directories map[string]bool, metadata bool) (CheckoutFile, bool, error) {
	if err := validateCheckoutPath(relative); err != nil {
		return CheckoutFile{}, false, err
	}
	parts := strings.Split(relative, "/")
	for i := 1; i < len(parts); i++ {
		parent := strings.Join(parts[:i], "/")
		info, err := root.Lstat(filepath.FromSlash(parent))
		if errors.Is(err, os.ErrNotExist) {
			return CheckoutFile{}, false, nil
		}
		if err != nil {
			return CheckoutFile{}, false, err
		}
		if !info.IsDir() {
			if (info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) && tracked[parent] {
				return CheckoutFile{}, false, nil
			}
			return CheckoutFile{}, false, &Error{Code: CodeConflict, Message: fmt.Sprintf("destination parent for %q is not a real directory", relative)}
		}
	}
	info, err := root.Lstat(filepath.FromSlash(relative))
	if err != nil || !info.IsDir() {
		return checkoutFileOnRoot(root, relative)
	}
	err = fs.WalkDir(root.FS(), relative, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && tracked[path] {
			return nil
		}
		if entry.IsDir() && directories[path] {
			if metadata {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				_, err = checkCheckoutDirectoryPermissions(root, filepath.FromSlash(path), info)
				return err
			}
			return nil
		}
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("ignored destination path %q collides with the incoming workspace", path)}
	})
	return CheckoutFile{}, false, err
}

// Directory metadata is local to the prepared transaction, never the capture format.
type checkoutDirectoryMetadata struct {
	info          os.FileInfo
	provenance    []byte
	hasProvenance bool
}

// pinReplacedCheckoutDirectories records the directory identities that an
// incoming leaf replaces, before outgoing files are removed.
func pinReplacedCheckoutDirectories(root *os.Root, destinationFiles, incomingFiles map[string]CheckoutFile) (map[string]checkoutDirectoryMetadata, error) {
	directories := make(map[string]checkoutDirectoryMetadata)
	for oldPath := range destinationFiles {
		var parents []string
		for parent := filepath.ToSlash(filepath.Dir(oldPath)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			parents = append(parents, parent)
			if _, replaced := incomingFiles[parent]; !replaced {
				continue
			}
			// Inspect from the outside in, without following a replaced file
			// or symlink. Recovery may already have removed part of the chain.
			for i := len(parents) - 1; i >= 0; i-- {
				dir := parents[i]
				info, err := root.Lstat(filepath.FromSlash(dir))
				if errors.Is(err, os.ErrNotExist) {
					break
				}
				if err != nil {
					return nil, err
				}
				if !info.IsDir() {
					break
				}
				if _, pinned := directories[dir]; !pinned {
					metadata, err := checkCheckoutDirectoryPermissions(root, filepath.FromSlash(dir), info)
					if err != nil {
						return nil, err
					}
					directories[dir] = metadata
				}
			}
			break
		}
	}
	return directories, nil
}

// Group each contiguous directory chain under its replacement root once.
func checkoutDirectoryRemovals(paths []string) map[string][]string {
	known := make(map[string]bool, len(paths))
	for _, path := range paths {
		known[filepath.ToSlash(path)] = true
	}
	groups := make(map[string][]string)
	for _, path := range paths {
		path = filepath.ToSlash(path)
		root := path
		for parent := filepath.ToSlash(filepath.Dir(root)); known[parent]; parent = filepath.ToSlash(filepath.Dir(parent)) {
			root = parent
		}
		groups[root] = append(groups[root], path)
	}
	for _, paths := range groups {
		sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	}
	return groups
}

func (prepared *preparedCheckoutFiles) removeReplacedDirectories(path string) error {
	if prepared.directoryRemovals == nil {
		paths := make([]string, 0, len(prepared.removedDirs))
		for dir := range prepared.removedDirs {
			paths = append(paths, dir)
		}
		prepared.directoryRemovals = checkoutDirectoryRemovals(paths)
	}
	paths := prepared.directoryRemovals[path]
	for _, dir := range paths {
		info, err := prepared.root.Lstat(filepath.FromSlash(dir))
		if err != nil || !os.SameFile(info, prepared.removedDirs[dir].info) {
			return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination directory %q changed during workspace application", dir), Cause: err}
		}
		metadata, err := checkCheckoutDirectoryPermissions(prepared.root, filepath.FromSlash(dir), prepared.removedDirs[dir].info)
		if err != nil {
			return err
		}
		if !checkoutDirectoryProvenanceEqual(metadata, prepared.removedDirs[dir]) {
			return &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("directory %q provenance changed during workspace application", dir)}
		}
		mutated, err := removeCheckoutDirectory(prepared.root, filepath.FromSlash(dir), prepared.removedDirs[dir], true)
		prepared.mutated = prepared.mutated || mutated
		if err != nil {
			return err
		}
	}
	return nil
}

type preparedCheckoutFile struct {
	path                string
	stagingBase         string
	incoming            *CheckoutFile
	previous            *CheckoutFile
	incomingTemp        string
	backupTemp          string
	backupInfo          os.FileInfo
	absent              bool
	displacedTemp       string
	preserveBackup      bool
	preserveDisplaced   bool
	preserveDestination bool
}

type preparedCheckoutFiles struct {
	root              *os.Root
	entries           []preparedCheckoutFile
	createdDirs       []string
	createdDirInfo    map[string]os.FileInfo
	removedDirs       map[string]checkoutDirectoryMetadata
	directoryRemovals map[string][]string
	recreatedDirs     map[string]os.FileInfo
	mutated           bool
}

func prepareCheckoutFiles(root *os.Root, destination CheckoutState, payload ExtractedCheckout) (*preparedCheckoutFiles, error) {
	destinationFiles := checkoutFileMap(destination.Files)
	incomingFiles := checkoutFileMap(payload.State.Files)
	replacementRoots := checkoutFileMap(payload.State.Files)
	for _, path := range payload.State.AbsentPaths {
		replacementRoots[path] = CheckoutFile{Path: path}
	}
	paths := make(map[string]struct{}, len(payload.State.Files)+len(destination.Files))
	for path := range replacementRoots {
		paths[path] = struct{}{}
	}
	for _, entry := range destination.Files {
		if entry.Tracked {
			if _, keep := incomingFiles[entry.Path]; !keep {
				paths[entry.Path] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	// Delete outgoing leaves before creating their replacements.
	sort.Slice(ordered, func(i, j int) bool {
		_, left := replacementRoots[ordered[i]]
		_, right := replacementRoots[ordered[j]]
		if left != right {
			return !left
		}
		return ordered[i] < ordered[j]
	})
	prepared := &preparedCheckoutFiles{root: root, entries: make([]preparedCheckoutFile, 0, len(ordered))}
	failed := true
	defer func() {
		if failed {
			prepared.Release()
		}
	}()
	var err error
	prepared.removedDirs, err = pinReplacedCheckoutDirectories(root, destinationFiles, replacementRoots)
	if err != nil {
		return nil, err
	}
	for _, path := range ordered {
		prepared.entries = append(prepared.entries, preparedCheckoutFile{path: path, stagingBase: checkoutStagingBase(path, destinationFiles, replacementRoots)})
		record := &prepared.entries[len(prepared.entries)-1]
		_, replacement := replacementRoots[path]
		_, incoming := incomingFiles[path]
		record.absent = replacement && !incoming
		if value, ok := destinationFiles[path]; ok {
			copy := value
			record.previous = &copy
			current, present, err := checkoutFileOnRoot(root, path)
			if err != nil {
				return nil, err
			}
			if !present || !checkoutFileContentEqual(current, value) {
				return nil, &Error{Code: CodeConflict, Message: fmt.Sprintf("destination path %q changed before workspace files were staged", path)}
			}
			backup, err := checkoutTemporaryPath(record.stagingBase)
			if err != nil {
				return nil, err
			}
			if err = root.Link(filepath.FromSlash(path), backup); err != nil {
				return nil, &Error{Code: CodeUnsupported, Message: fmt.Sprintf("destination path %q cannot be protected for rollback", path), Cause: err}
			}
			record.backupTemp = backup
			record.backupInfo, err = root.Lstat(backup)
			if err != nil {
				return nil, err
			}
		}
		if value, ok := incomingFiles[path]; ok {
			copy := value
			record.incoming = &copy
			dirs, err := ensureCheckoutDestinationDirectories(root, record.stagingBase)
			prepared.createdDirs = append(prepared.createdDirs, dirs...)
			if pinErr := prepared.pinCreatedDirectories(dirs); pinErr != nil {
				return nil, pinErr
			}
			if err != nil {
				return nil, err
			}
			record.incomingTemp, err = stageCheckoutEntryBeside(root, filepath.Join(payload.Directory, "files"), value, record.stagingBase)
			if err != nil {
				return nil, err
			}
		}
	}
	failed = false
	return prepared, nil
}

func (prepared *preparedCheckoutFiles) pinCreatedDirectories(paths []string) error {
	if prepared.createdDirInfo == nil {
		prepared.createdDirInfo = make(map[string]os.FileInfo)
	}
	for _, path := range paths {
		info, err := prepared.root.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return &Error{Code: CodeConflict, Message: "created directory changed independently"}
		}
		prepared.createdDirInfo[path] = info
	}
	return nil
}

func ensureCheckoutDestinationDirectories(root *os.Root, relative string) ([]string, error) {
	parent := filepath.Dir(filepath.FromSlash(relative))
	if parent == "." {
		return nil, nil
	}
	created := make([]string, 0)
	current := ""
	for _, component := range strings.Split(parent, string(filepath.Separator)) {
		if current == "" {
			current = component
		} else {
			current = filepath.Join(current, component)
		}
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err = root.Mkdir(current, 0o755); err != nil {
				return created, err
			}
			created = append(created, current)
			continue
		}
		if err != nil {
			return created, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return created, &Error{Code: CodeConflict, Message: fmt.Sprintf("destination parent for %q is not a real directory", relative)}
		}
	}
	return created, nil
}

func (prepared *preparedCheckoutFiles) Apply() error {
	for index := range prepared.entries {
		record := &prepared.entries[index]
		if record.incoming != nil || record.absent {
			if err := prepared.removeReplacedDirectories(record.path); err != nil {
				return err
			}
		}
		if record.incoming != nil {
			dirs, err := ensureCheckoutDestinationDirectories(prepared.root, record.path)
			prepared.createdDirs = append(prepared.createdDirs, dirs...)
			if len(dirs) != 0 {
				prepared.mutated = true
			}
			if pinErr := prepared.pinCreatedDirectories(dirs); pinErr != nil {
				return pinErr
			}
			if err != nil {
				return err
			}
		}
		current, present, err := checkoutFileOnRoot(prepared.root, record.path)
		if err != nil {
			return err
		}
		if record.previous == nil {
			if present {
				return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination path %q appeared while workspace files were staged", record.path)}
			}
		} else if !present || !checkoutFileContentEqual(current, *record.previous) {
			return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination path %q changed while workspace files were staged", record.path)}
		}
		if record.previous != nil {
			if err = prepared.displaceAndVerify(record); err != nil {
				return err
			}
		}
		if record.incoming == nil {
			if err = prepared.removeDisplaced(record); err != nil {
				return err
			}
			continue
		}
		if record.previous == nil {
			prepared.mutated = true
			if err = prepared.root.Link(record.incomingTemp, filepath.FromSlash(record.path)); err == nil {
				err = prepared.root.Remove(record.incomingTemp)
			}
		} else {
			err = renameCheckoutNoReplace(prepared.root, record.incomingTemp, filepath.FromSlash(record.path))
		}
		if err != nil {
			if record.previous != nil {
				record.preserveDestination = true
				_ = prepared.removeDisplaced(record)
				return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination path %q appeared while workspace push was applying", record.path), Cause: err}
			}
			return err
		}
		record.incomingTemp = ""
		if err = prepared.removeDisplaced(record); err != nil {
			return err
		}
	}
	return nil
}

func (prepared *preparedCheckoutFiles) displaceAndVerify(record *preparedCheckoutFile) error {
	displaced, err := checkoutTemporaryPath(record.stagingBase)
	if err != nil {
		return err
	}
	if err = prepared.root.Rename(filepath.FromSlash(record.path), displaced); err != nil {
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination path %q changed immediately before replacement", record.path), Cause: err}
	}
	prepared.mutated = true
	record.displacedTemp = displaced
	current, present, inspectErr := checkoutFileOnRoot(prepared.root, displaced)
	if inspectErr == nil && present && checkoutFileContentEqual(current, *record.previous) {
		return nil
	}
	record.preserveDestination = true
	restoreErr := renameCheckoutNoReplace(prepared.root, displaced, filepath.FromSlash(record.path))
	if restoreErr == nil {
		record.displacedTemp = ""
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination path %q changed during workspace application", record.path), Cause: inspectErr}
	}
	record.preserveDisplaced = true
	return &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("destination path %q changed during workspace application; preserved displaced content at %q", record.path, filepath.ToSlash(displaced)), Cause: errors.Join(inspectErr, restoreErr)}
}

func (prepared *preparedCheckoutFiles) removeDisplaced(record *preparedCheckoutFile) error {
	if record.displacedTemp == "" {
		return nil
	}
	if err := prepared.root.Remove(record.displacedTemp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	record.displacedTemp = ""
	return nil
}

func renameCheckoutNoReplace(root *os.Root, source, destination string) error {
	sourceParent := filepath.Dir(source)
	destinationParent := filepath.Dir(destination)
	sourceDirectory, err := root.Open(sourceParent)
	if err != nil {
		return err
	}
	defer sourceDirectory.Close()
	destinationDirectory := sourceDirectory
	if destinationParent != sourceParent {
		destinationDirectory, err = root.Open(destinationParent)
		if err != nil {
			return err
		}
		defer destinationDirectory.Close()
	}
	return renameNoReplaceAt(sourceDirectory, filepath.Base(source), destinationDirectory, filepath.Base(destination))
}

func (prepared *preparedCheckoutFiles) Rollback() (rollbackErr error) {
	if prepared == nil || !prepared.mutated {
		return nil
	}
	defer func() {
		if rollbackErr != nil {
			for i := range prepared.entries {
				prepared.entries[i].preserveBackup = true
			}
		}
	}()
	known := make(map[string]bool, len(prepared.entries))
	for _, record := range prepared.entries {
		known[record.path] = true
	}
	managedDirectories := checkoutManagedDirectories(known)
	createdDirRemovals := checkoutDirectoryRemovals(prepared.createdDirs)
	for index := range prepared.entries {
		record := &prepared.entries[index]
		if record.preserveDestination {
			continue
		}
		if record.backupTemp != "" && record.previous != nil {
			backup, present, err := checkoutFileOnRoot(prepared.root, record.backupTemp)
			if err != nil {
				record.preserveBackup = true
				return err
			}
			if !present || !checkoutFileContentEqual(backup, *record.previous) {
				record.preserveBackup = present
				return &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("rollback material for destination path %q changed independently; preserved recovery content at %q", record.path, filepath.ToSlash(record.backupTemp))}
			}
		}
		current, present, err := checkoutFileForTransition(prepared.root, record.path, known, managedDirectories)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		if record.previous != nil && checkoutFileContentEqual(current, *record.previous) {
			continue
		}
		if record.incoming != nil && checkoutFileContentEqual(current, *record.incoming) {
			continue
		}
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination path %q changed independently before rollback", record.path)}
	}
	for index := len(prepared.entries) - 1; index >= 0; index-- {
		record := &prepared.entries[index]
		if record.preserveDestination {
			continue
		}
		current, present, err := checkoutFileForTransition(prepared.root, record.path, known, managedDirectories)
		if err != nil {
			return err
		}
		if record.previous == nil {
			if present {
				if err = prepared.root.Remove(filepath.FromSlash(record.path)); err != nil {
					return err
				}
			}
			continue
		}
		if present && checkoutFileContentEqual(current, *record.previous) {
			continue
		}
		if record.backupTemp == "" {
			return &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("rollback material for destination path %q is unavailable", record.path)}
		}
		// Incoming children have already been removed in reverse apply order.
		for _, dir := range createdDirRemovals[record.path] {
			changed, removeErr := removeCheckoutDirectory(prepared.root, dir, checkoutDirectoryMetadata{info: prepared.createdDirInfo[dir]}, false)
			prepared.mutated = prepared.mutated || changed
			if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return removeErr
			}
		}
		dirs, err := ensureCheckoutDestinationDirectories(prepared.root, record.path)
		if err != nil {
			return err
		}
		if prepared.recreatedDirs == nil {
			prepared.recreatedDirs = make(map[string]os.FileInfo)
		}
		for _, dir := range dirs {
			info, err := prepared.root.Lstat(dir)
			if err != nil {
				return err
			}
			prepared.recreatedDirs[filepath.ToSlash(dir)] = info
		}
		var parents []string
		for dir := filepath.ToSlash(filepath.Dir(record.path)); dir != "."; dir = filepath.ToSlash(filepath.Dir(dir)) {
			if prepared.recreatedDirs[dir] != nil && prepared.removedDirs[dir].info != nil {
				parents = append(parents, dir)
			}
		}
		var parent *os.File
		for i := len(parents) - 1; i >= 0; i-- {
			dir := parents[i]
			opened, restoreErr := restoreCheckoutDirectoryMetadata(prepared.root, dir, prepared.recreatedDirs[dir], prepared.removedDirs[dir])
			if parent != nil {
				parent.Close()
			}
			if restoreErr != nil {
				return restoreErr
			}
			parent = opened
		}
		if parent != nil {
			err = prepared.restoreBackupAt(record, parent, parents)
			parent.Close()
		} else {
			err = prepared.root.Rename(record.backupTemp, filepath.FromSlash(record.path))
		}
		if err != nil {
			return err
		}
		record.backupTemp = ""
	}
	for i := range prepared.entries {
		if !prepared.entries[i].preserveDestination {
			prepared.entries[i].preserveBackup = false
		}
	}
	prepared.mutated = false
	return nil
}

// verificationState removes only the exact random rollback paths reserved by
// this operation from a freshly observed state. The hard-linked backups must
// remain available until verification completes, but they are transaction
// internals rather than source workspace files.
func (prepared *preparedCheckoutFiles) verificationState(observed CheckoutState) (CheckoutState, error) {
	temporary := make(map[string]struct{})
	for index := range prepared.entries {
		record := &prepared.entries[index]
		if record.backupTemp == "" || record.previous == nil {
			continue
		}
		backup, present, err := checkoutFileOnRoot(prepared.root, record.backupTemp)
		if err != nil {
			return CheckoutState{}, err
		}
		if !present || !checkoutFileContentEqual(backup, *record.previous) {
			record.preserveBackup = present
			return CheckoutState{}, &Error{Code: CodeOutcomeUnknown, Message: fmt.Sprintf("rollback material for destination path %q changed before verification; preserved recovery content at %q", record.path, filepath.ToSlash(record.backupTemp))}
		}
		temporary[filepath.ToSlash(record.backupTemp)] = struct{}{}
	}
	if len(temporary) == 0 {
		return observed, nil
	}
	files := make([]CheckoutFile, 0, len(observed.Files))
	removed := 0
	for _, entry := range observed.Files {
		if _, internal := temporary[entry.Path]; internal {
			observed.Bytes -= entry.Size
			removed++
			continue
		}
		files = append(files, entry)
	}
	observed.Files = files
	observed.FileCount = len(files)
	if observed.Status.Untracked < removed {
		return CheckoutState{}, &Error{Code: CodeOutcomeUnknown, Message: "destination status could not be reconciled with reserved rollback files"}
	}
	observed.Status.Untracked -= removed
	var err error
	observed.Digest, err = checkoutDigest(observed)
	if err == nil {
		observed.RevalidationDigest, err = checkoutRevalidationDigest(observed)
	}
	return observed, err
}

func (prepared *preparedCheckoutFiles) Release() {
	if prepared == nil || prepared.root == nil {
		return
	}
	for _, record := range prepared.entries {
		if record.incomingTemp != "" {
			_ = prepared.root.Remove(record.incomingTemp)
		}
		if record.backupTemp != "" && !record.preserveBackup {
			if record.previous != nil {
				backup, present, err := checkoutFileOnRoot(prepared.root, record.backupTemp)
				if err != nil || (present && !checkoutFileContentEqual(backup, *record.previous)) {
					continue
				}
			}
			_ = prepared.root.Remove(record.backupTemp)
		}
		if record.displacedTemp != "" && !record.preserveDisplaced {
			_ = prepared.root.Remove(record.displacedTemp)
		}
	}
	sort.Slice(prepared.createdDirs, func(i, j int) bool { return len(prepared.createdDirs[i]) > len(prepared.createdDirs[j]) })
	for _, directory := range prepared.createdDirs {
		opened, err := prepared.root.OpenFile(directory, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			continue
		}
		info, statErr := opened.Stat()
		entries, readErr := opened.ReadDir(1)
		opened.Close()
		if statErr == nil && os.SameFile(info, prepared.createdDirInfo[directory]) && len(entries) == 0 && errors.Is(readErr, io.EOF) {
			_, _ = removeCheckoutDirectory(prepared.root, directory, checkoutDirectoryMetadata{info: prepared.createdDirInfo[directory]}, false)
		}
	}
}

func checkoutFileContentEqual(left, right CheckoutFile) bool {
	return left.Path != "" && right.Path != "" && left.Kind == right.Kind && left.Executable == right.Executable && left.Size == right.Size && left.SHA256 == right.SHA256
}

func applyCheckout(ctx context.Context, root, target string, payload ExtractedCheckout, expectedDigest, rewindBranch string) (CheckoutState, error) {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return CheckoutState{}, &Error{Code: CodeInvalidInput, Message: "destination Worktree path must be canonical and absolute"}
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !within(root, target) || root == target {
		return CheckoutState{}, &Error{Code: CodeInvalidInput, Message: "destination Worktree Root must be canonical, absolute, and contain the destination"}
	}
	relativeTarget, err := filepath.Rel(root, target)
	if err != nil {
		return CheckoutState{}, &Error{Code: CodeInvalidInput, Message: "destination Worktree is not relative to its Worktree Root", Cause: err}
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "destination Worktree Root cannot be canonicalized", Cause: err}
	}
	root = filepath.Clean(canonicalRoot)
	target = filepath.Join(root, relativeTarget)
	if !validGitObjectID(payload.State.HEAD) || payload.State.IndexCount != len(payload.State.IndexEntries) || payload.State.AbsentCount != len(payload.State.AbsentPaths) || payload.State.Digest == "" || payload.State.RevalidationDigest == "" {
		return CheckoutState{}, &Error{Code: CodeInvalidInput, Message: "workspace payload state is incomplete"}
	}
	if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		if expectedDigest != "" {
			return CheckoutState{}, &Error{Code: CodeConflict, Message: "destination Worktree disappeared after workspace preflight"}
		}
		return createCheckout(ctx, root, target, payload)
	} else if err != nil {
		return CheckoutState{}, err
	}
	return applyExistingCheckout(ctx, target, payload, expectedDigest, rewindBranch)
}

func createCheckout(ctx context.Context, root, target string, payload ExtractedCheckout) (CheckoutState, error) {
	parent := filepath.Dir(target)
	stageRoot := filepath.Dir(payload.Directory)
	stage, err := os.MkdirTemp(stageRoot, ".create-*")
	if err != nil {
		return CheckoutState{}, err
	}
	canonicalStageRoot, err := filepath.EvalSymlinks(stageRoot)
	if err != nil {
		return CheckoutState{}, err
	}
	stage, err = filepath.EvalSymlinks(stage)
	if err != nil || !within(canonicalStageRoot, stage) {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "workspace creation staging path is not contained by Schooner state", Cause: err}
	}
	stageRoot = canonicalStageRoot
	defer os.RemoveAll(stage)
	result, err := process.RunCapturedWithoutEnvironment(ctx, 64<<10, gitRepositoryEnvironment, nil, "git", "init", "--quiet", "--object-format=sha1", stage)
	if err != nil {
		return CheckoutState{}, gitTransferError("initialize destination Repository", result, err)
	}
	pack, err := os.Open(payload.ObjectsPack)
	if err != nil {
		return CheckoutState{}, err
	}
	result, importErr := process.RunStreamingWithoutEnvironment(ctx, stage, gitRepositoryEnvironment, nil, pack, io.Discard, "git", "--no-optional-locks", "-c", "core.fsmonitor=false", "index-pack", "--stdin")
	_ = pack.Close()
	if importErr != nil {
		return CheckoutState{}, gitTransferError("seed destination Git objects", result, importErr)
	}
	if err = validateImportedCheckoutObjects(ctx, stage, payload.State); err != nil {
		return CheckoutState{}, err
	}
	if _, err = initializeCheckoutHEAD(ctx, stage, payload.State); err != nil {
		return CheckoutState{}, err
	}
	if payload.State.CloneSource != "" {
		result, err = process.RunCapturedWithoutEnvironment(ctx, 64<<10, gitRepositoryEnvironment, nil, "git", "-C", stage, "remote", "add", "origin", payload.State.CloneSource)
		if err != nil {
			return CheckoutState{}, gitTransferError("record destination origin", result, err)
		}
	}
	if _, err = applyExistingCheckout(ctx, stage, payload, "", ""); err != nil {
		return CheckoutState{}, err
	}
	destinationParent, err := openDestinationParent(root, parent)
	if err != nil {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "destination Worktree ancestors changed during workspace creation", Cause: err}
	}
	sourceParent, moveErr := openExistingDirectory(stageRoot, stageRoot)
	if moveErr == nil {
		moveErr = renameNoReplaceAt(sourceParent, filepath.Base(stage), destinationParent, filepath.Base(target))
	}
	published := moveErr == nil
	if moveErr == nil {
		moveErr = sourceParent.Sync()
	}
	if moveErr == nil {
		moveErr = destinationParent.Sync()
	}
	if sourceParent != nil {
		if closeErr := sourceParent.Close(); moveErr == nil {
			moveErr = closeErr
		}
	}
	if closeErr := destinationParent.Close(); moveErr == nil {
		moveErr = closeErr
	}
	if errors.Is(moveErr, syscall.EXDEV) {
		return CheckoutState{}, &Error{Code: CodeUnsupported, Message: "workspace creation requires Schooner state and the Worktree Root to use the same filesystem", Cause: moveErr}
	}
	if moveErr != nil {
		if published {
			return reconcileCreatedCheckout(ctx, target, payload.State, moveErr)
		}
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "destination Worktree appeared or changed while the workspace was being created", Cause: moveErr}
	}
	return reconcileCreatedCheckout(ctx, target, payload.State, nil)
}

func reconcileCreatedCheckout(ctx context.Context, target string, expected CheckoutState, publicationErr error) (CheckoutState, error) {
	observed, observeErr := ObserveCheckout(context.WithoutCancel(ctx), target)
	if observeErr == nil && observed.RevalidationDigest == expected.RevalidationDigest {
		return observed, nil
	}
	if observeErr == nil {
		observeErr = &Error{Code: CodeOutcomeUnknown, Message: "created destination Worktree does not match the captured source"}
	}
	return CheckoutState{}, afterCheckoutMutation(&Error{
		Code:    CodeOutcomeUnknown,
		Message: "destination Worktree was created but its final state could not be verified",
		Cause:   errors.Join(publicationErr, observeErr),
		Context: map[string]string{"remote_created": "true", "remote_worktree": target},
	})
}

// initializeCheckoutHEAD runs only inside createCheckout's private, randomly
// named staging Repository. Some Git versions treat an unborn symbolic HEAD as
// an existing ref when --no-deref is combined with a zero old OID, while
// others treat its unresolved object value as absent. There is no concurrent
// destination to protect in this unpublished staging directory, so detached
// initialization deliberately omits that ambiguous old-value assertion.
// Updates to live Worktrees continue to use updateCheckoutHEAD's compare-and-
// swap behavior.
func initializeCheckoutHEAD(ctx context.Context, target string, state CheckoutState) (*checkoutHEADUpdate, error) {
	if !state.Detached {
		return updateCheckoutHEAD(ctx, target, state)
	}
	if _, err := git(ctx, commandRunner{}, target, "update-ref", "--no-deref", "HEAD", state.HEAD); err != nil {
		return nil, fmt.Errorf("initialize detached destination HEAD: %w", err)
	}
	return &checkoutHEADUpdate{target: target, newHEAD: state.HEAD, headChanged: true}, nil
}

func applyExistingCheckout(ctx context.Context, target string, payload ExtractedCheckout, expectedDigest, rewindBranch string) (CheckoutState, error) {
	destination, err := ObserveCheckout(ctx, target)
	if err != nil {
		return CheckoutState{}, err
	}
	if expectedDigest != "" && destination.RevalidationDigest != expectedDigest {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "the destination Worktree changed after workspace preflight"}
	}
	incarnation, err := captureCheckoutIncarnation(ctx, target)
	if err != nil {
		return CheckoutState{}, err
	}
	if err = PreflightCheckoutApplication(ctx, target, destination, payload.State.Files, payload.State.AbsentPaths); err != nil {
		return CheckoutState{}, err
	}
	if err = PreflightCheckoutBranch(ctx, target, payload.State); err != nil {
		return CheckoutState{}, err
	}
	pack, err := os.Open(payload.ObjectsPack)
	if err != nil {
		return CheckoutState{}, err
	}
	result, importErr := process.RunStreamingWithoutEnvironment(ctx, target, gitRepositoryEnvironment, nil, pack, io.Discard, "git", "--no-optional-locks", "-c", "core.fsmonitor=false", "index-pack", "--stdin")
	_ = pack.Close()
	if importErr != nil {
		return CheckoutState{}, gitTransferError("import workspace Git objects", result, importErr)
	}
	if err = validateImportedCheckoutObjects(ctx, target, payload.State); err != nil {
		return CheckoutState{}, err
	}
	if err = incarnation.Validate(ctx, target); err != nil {
		return CheckoutState{}, err
	}
	allowBranchRewind := rewindBranch != "" && !payload.State.Detached && payload.State.Branch == rewindBranch
	if !allowBranchRewind {
		if err = validateIncomingBranch(ctx, target, payload.State); err != nil {
			return CheckoutState{}, err
		}
	}
	preparedIndex, err := prepareCheckoutIndex(ctx, target, payload)
	if err != nil {
		return CheckoutState{}, err
	}
	defer preparedIndex.Release()
	if err = incarnation.Validate(ctx, target); err != nil {
		return CheckoutState{}, err
	}
	latest, err := ObserveCheckout(ctx, target)
	if err != nil {
		return CheckoutState{}, err
	}
	if latest.RevalidationDigest != destination.RevalidationDigest {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "the destination Worktree changed immediately before workspace application"}
	}
	if err = incarnation.Validate(ctx, target); err != nil {
		return CheckoutState{}, err
	}
	if err = PreflightCheckoutApplication(ctx, target, latest, payload.State.Files, payload.State.AbsentPaths); err != nil {
		return CheckoutState{}, err
	}
	if err = PreflightCheckoutBranch(ctx, target, payload.State); err != nil {
		return CheckoutState{}, err
	}
	destinationRoot, err := os.OpenRoot(target)
	if err != nil {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "destination Worktree could not be opened safely for workspace application", Cause: err}
	}
	defer destinationRoot.Close()
	rootInfo, err := destinationRoot.Stat(".")
	if err != nil || !os.SameFile(incarnation.worktree, rootInfo) {
		return CheckoutState{}, &Error{Code: CodeConflict, Message: "destination Worktree directory changed before workspace files were staged", Cause: err}
	}
	preparedFiles, err := prepareCheckoutFiles(destinationRoot, latest, payload)
	if err != nil {
		return CheckoutState{}, err
	}
	defer preparedFiles.Release()
	mutationStarted := false
	var headUpdate *checkoutHEADUpdate
	applyFailure := func(value error) error {
		if mutationStarted {
			var restoreErrors []error
			if restoreErr := preparedIndex.Restore(context.WithoutCancel(ctx), target, payload.State.IndexEntries); restoreErr != nil {
				restoreErrors = append(restoreErrors, restoreErr)
			}
			if headUpdate != nil {
				if restoreErr := headUpdate.Restore(context.WithoutCancel(ctx)); restoreErr != nil {
					restoreErrors = append(restoreErrors, restoreErr)
				}
			}
			if restoreErr := preparedFiles.Rollback(); restoreErr != nil {
				restoreErrors = append(restoreErrors, restoreErr)
			}
			if len(restoreErrors) == 0 {
				return value
			}
			return afterCheckoutMutation(&Error{Code: CodeOutcomeUnknown, Message: "workspace application failed and reserved rollback material could not fully restore the destination", Cause: errors.Join(append([]error{value}, restoreErrors...)...)})
		}
		return value
	}
	if err = preparedFiles.Apply(); err != nil {
		mutationStarted = preparedFiles.mutated
		return CheckoutState{}, applyFailure(err)
	}
	mutationStarted = preparedFiles.mutated
	mutationStarted = true
	if err = incarnation.Validate(ctx, target); err != nil {
		return CheckoutState{}, applyFailure(err)
	}
	rewindHEAD := ""
	if allowBranchRewind {
		rewindHEAD = destination.HEAD
	}
	headUpdate, err = updateCheckoutHEADWithBranchRewind(ctx, target, payload.State, rewindHEAD)
	if err != nil {
		return CheckoutState{}, applyFailure(err)
	}
	mutationStarted = true
	if err = incarnation.Validate(ctx, target); err != nil {
		return CheckoutState{}, applyFailure(err)
	}
	if err = preparedIndex.Promote(); err != nil {
		return CheckoutState{}, applyFailure(err)
	}
	if err = incarnation.Validate(ctx, target); err != nil {
		return CheckoutState{}, applyFailure(err)
	}
	observed, err := ObserveCheckout(ctx, target)
	if err != nil {
		return CheckoutState{}, applyFailure(err)
	}
	if err = incarnation.Validate(ctx, target); err != nil {
		return CheckoutState{}, applyFailure(err)
	}
	observed, err = preparedFiles.verificationState(observed)
	if err != nil {
		return CheckoutState{}, applyFailure(err)
	}
	if observed.Digest != payload.State.Digest {
		return CheckoutState{}, applyFailure(&Error{Code: CodeOutcomeUnknown, Message: "destination workspace did not match the captured source after application"})
	}
	return observed, nil
}

func validateImportedCheckoutObjects(ctx context.Context, target string, state CheckoutState) error {
	result, err := process.RunStreamingWithoutEnvironment(ctx, target, gitRepositoryEnvironment, gitSafeEnvironment, nil, io.Discard,
		"git", "--no-optional-locks", "--no-replace-objects", "-c", "core.fsmonitor=false", "rev-list", "--objects", "--missing=error", state.HEAD)
	if err != nil {
		return &Error{Code: CodeInvalidInput, Message: "workspace payload does not contain the complete source HEAD object graph", Cause: gitTransferError("validate workspace commit objects", result, err)}
	}
	type objectExpectation struct{ object, kind string }
	expectations := []objectExpectation{{object: state.HEAD, kind: "commit"}}
	seenBlobs := make(map[string]struct{})
	for _, entry := range state.IndexEntries {
		if _, exists := seenBlobs[entry.Object]; exists {
			continue
		}
		seenBlobs[entry.Object] = struct{}{}
		expectations = append(expectations, objectExpectation{object: entry.Object, kind: "blob"})
	}
	for start := 0; start < len(expectations); start += 256 {
		end := min(start+256, len(expectations))
		var input, output bytes.Buffer
		for _, expectation := range expectations[start:end] {
			input.WriteString(expectation.object)
			input.WriteByte('\n')
		}
		result, err = process.RunStreamingWithoutEnvironment(ctx, target, gitRepositoryEnvironment, gitSafeEnvironment, &input, &output,
			"git", "--no-optional-locks", "--no-replace-objects", "-c", "core.fsmonitor=false", "cat-file", "--batch-check=%(objectname) %(objecttype)")
		if err != nil {
			return &Error{Code: CodeInvalidInput, Message: "workspace payload Git objects cannot be inspected", Cause: gitTransferError("inspect workspace Git objects", result, err)}
		}
		lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
		if len(lines) != end-start {
			return &Error{Code: CodeInvalidInput, Message: "workspace payload Git object inventory is incomplete"}
		}
		for index, expectation := range expectations[start:end] {
			fields := strings.Fields(lines[index])
			if len(fields) != 2 || fields[0] != expectation.object || fields[1] != expectation.kind {
				return &Error{Code: CodeInvalidInput, Message: fmt.Sprintf("workspace payload object %s is missing or is not a %s", expectation.object, expectation.kind)}
			}
		}
	}
	return nil
}

// PreflightCheckoutApplication checks destination-local filesystem state that
// Git history alone cannot describe. It is read-only and is used by dry-run as
// well as immediately before apply.
func PreflightCheckoutApplication(ctx context.Context, target string, destination CheckoutState, incomingFiles []CheckoutFile, incomingAbsent []string) error {
	if err := preflightIncomingGitSemantics(ctx, target, incomingFiles); err != nil {
		return err
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return &Error{Code: CodeConflict, Message: "destination Worktree could not be opened safely for preflight", Cause: err}
	}
	defer root.Close()
	tracked := make(map[string]bool, len(destination.Files))
	for _, entry := range destination.Files {
		tracked[entry.Path] = entry.Tracked
	}
	managedDirectories := checkoutManagedDirectories(tracked)
	for _, incoming := range incomingFiles {
		_, present, statErr := checkoutFileForTransitionPreflight(root, incoming.Path, tracked, managedDirectories, true)
		if statErr == nil && present && !tracked[incoming.Path] {
			return &Error{Code: CodeConflict, Message: fmt.Sprintf("ignored destination path %q collides with the incoming workspace", incoming.Path)}
		} else if statErr != nil {
			return statErr
		}
	}
	for _, path := range incomingAbsent {
		_, present, statErr := checkoutFileForTransitionPreflight(root, path, tracked, managedDirectories, true)
		if statErr != nil {
			return statErr
		}
		if present && !tracked[path] {
			return &Error{Code: CodeConflict, Message: fmt.Sprintf("ignored destination path %q collides with an incoming tracked deletion", path)}
		}
	}
	return nil
}

type CheckoutComparison struct {
	ExistingFiles int
	MatchingFiles int
}

// PreflightCheckoutFiles checks incoming paths without observing and hashing
// the entire destination checkout. Callers page incoming manifests to keep
// remote control messages bounded.
func PreflightCheckoutFiles(ctx context.Context, target string, incomingFiles []CheckoutFile) (CheckoutComparison, error) {
	if len(incomingFiles) == 0 {
		return CheckoutComparison{}, nil
	}
	if err := preflightIncomingGitSemantics(ctx, target, incomingFiles); err != nil {
		return CheckoutComparison{}, err
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return CheckoutComparison{}, &Error{Code: CodeConflict, Message: "destination Worktree could not be opened safely for preflight", Cause: err}
	}
	defer root.Close()
	paths, err := checkoutPreflightPaths(root, incomingFiles)
	if err != nil {
		return CheckoutComparison{}, err
	}
	arguments := append([]string{"--literal-pathspecs", "ls-files", "-z", "--cached", "--"}, paths...)
	trackedOutput, err := git(ctx, commandRunner{}, target, arguments...)
	if err != nil {
		return CheckoutComparison{}, err
	}
	tracked := make(map[string]bool, len(incomingFiles))
	for _, path := range parseNULPaths(trackedOutput) {
		tracked[path] = true
	}
	managedDirectories := checkoutManagedDirectories(tracked)
	comparison := CheckoutComparison{}
	for _, incoming := range incomingFiles {
		if incoming.Kind == "absent" {
			_, present, statErr := checkoutFileForTransitionPreflight(root, incoming.Path, tracked, managedDirectories, true)
			if statErr != nil {
				return CheckoutComparison{}, statErr
			}
			if present && !tracked[incoming.Path] {
				return CheckoutComparison{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("ignored destination path %q collides with an incoming tracked deletion", incoming.Path)}
			}
			continue
		}
		observed, present, statErr := checkoutFileForTransitionPreflight(root, incoming.Path, tracked, managedDirectories, true)
		if statErr != nil {
			return CheckoutComparison{}, statErr
		}
		if !present {
			continue
		}
		if !tracked[incoming.Path] {
			return CheckoutComparison{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("ignored destination path %q collides with the incoming workspace", incoming.Path)}
		}
		comparison.ExistingFiles++
		if observed.Kind == incoming.Kind && observed.Executable == incoming.Executable && observed.Size == incoming.Size && observed.SHA256 == incoming.SHA256 {
			comparison.MatchingFiles++
		}
	}
	return comparison, nil
}

// checkoutPreflightPaths keeps each index query scoped to its manifest page.
// A directory page path includes its tracked descendants; ancestor directories
// must not be queried, since their unrelated children belong to other pages.
func checkoutPreflightPaths(root *os.Root, incomingFiles []CheckoutFile) ([]string, error) {
	var paths []string
	seen := make(map[string]bool)
	add := func(path string) {
		if !seen[path] {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	for _, incoming := range incomingFiles {
		if err := validateCheckoutPath(incoming.Path); err != nil {
			return nil, err
		}
		add(incoming.Path)
		parts := strings.Split(incoming.Path, "/")
		for i := 1; i < len(parts); i++ {
			parent := strings.Join(parts[:i], "/")
			info, err := root.Lstat(filepath.FromSlash(parent))
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				return nil, err
			}
			if !info.IsDir() {
				add(parent)
				break
			}
		}
	}
	return paths, nil
}

func preflightIncomingGitSemantics(ctx context.Context, target string, incomingFiles []CheckoutFile) error {
	if err := validateCheckoutAttributes(ctx, target, incomingFiles); err != nil {
		return err
	}
	var paths bytes.Buffer
	for _, entry := range incomingFiles {
		if entry.Kind == "absent" {
			continue
		}
		if entry.Tracked {
			continue
		}
		if err := validateCheckoutPath(entry.Path); err != nil {
			return err
		}
		paths.WriteString(entry.Path)
		paths.WriteByte(0)
	}
	if paths.Len() == 0 {
		return nil
	}
	// Incoming files were captured under the source's .gitignore rules. Check
	// only destination-local rules here; tracked .gitignore files are replaced
	// by the incoming workspace and may not even appear on this manifest page.
	ignoreRoot, err := destinationIgnoreWorktree(ctx, target, incomingFiles)
	if err != nil {
		return err
	}
	defer os.RemoveAll(ignoreRoot)
	arguments := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", target}
	// Git resolves relative exclusion paths after changing to its worktree.
	// Preserve their original destination-relative meaning in the scratch tree.
	excludes, configErr := git(ctx, commandRunner{}, target, "config", "--path", "--get", "core.excludesFile")
	if configErr != nil && exitCode(configErr) != 1 {
		return configErr
	}
	if path := strings.TrimSuffix(string(excludes), "\n"); path != "" && !filepath.IsAbs(path) {
		arguments = append(arguments, "-c", "core.excludesFile="+filepath.Join(target, path))
	}
	arguments = append(arguments, "check-ignore", "--no-index", "--stdin", "-z")
	var ignored bytes.Buffer
	result, err := process.RunStreamingWithoutEnvironment(ctx, target, gitRepositoryEnvironment, []string{"GIT_WORK_TREE=" + ignoreRoot}, &paths, &ignored,
		"git", arguments...)
	if err != nil && process.ExitCode(err) != 1 {
		return gitTransferError("check destination ignore rules", result, err)
	}
	ignoredPaths := parseNULPaths(ignored.Bytes())
	if len(ignoredPaths) != 0 {
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination ignore rules exclude incoming untracked path %q", ignoredPaths[0])}
	}
	return nil
}

// destinationIgnoreWorktree omits versioned ignore files while retaining local
// .gitignore files. Git still reads the destination's config and info/exclude.
func destinationIgnoreWorktree(ctx context.Context, target string, files []CheckoutFile) (string, error) {
	root, err := os.OpenRoot(target)
	if err != nil {
		return "", err
	}
	defer root.Close()
	var untracked []CheckoutFile
	leaves := make(map[string]bool)
	for _, entry := range files {
		if !entry.Tracked && entry.Kind != "absent" {
			untracked = append(untracked, entry)
			leaves[entry.Path] = true
		}
	}
	paths, err := checkoutPreflightPaths(root, untracked)
	if err != nil {
		return "", err
	}
	arguments := []string{"ls-files", "-z", "--cached", "--", ".gitignore", ":(glob)**/.gitignore"}
	blockers := make(map[string]bool)
	for _, path := range paths {
		if !leaves[path] {
			blockers[path] = false
			arguments = append(arguments, ":(literal)"+path)
		}
	}
	trackedOutput, err := git(ctx, commandRunner{}, target, arguments...)
	if err != nil {
		return "", err
	}
	seen := make(map[string]bool)
	for _, path := range parseNULPaths(trackedOutput) {
		seen[path] = true
		if _, candidate := blockers[path]; candidate {
			blockers[path] = true
		}
	}
	temporary, err := os.MkdirTemp("", "schooner-ignore-*")
	if err != nil {
		return "", err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(temporary)
		}
	}()
	for _, entry := range files {
		if entry.Tracked || entry.Kind == "absent" {
			continue
		}
		directory := filepath.Dir(entry.Path)
		// A tracked non-directory ancestor is an outgoing leaf. Ignore files
		// below it cannot belong to the destination; never follow its symlink.
		for parent := directory; parent != "."; parent = filepath.Dir(parent) {
			if blockers[parent] {
				directory = filepath.Dir(parent)
				break
			}
		}
		for ; ; directory = filepath.Dir(directory) {
			path := filepath.Join(directory, ".gitignore")
			if !seen[path] {
				seen[path] = true
				info, err := root.Lstat(path)
				if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
					return "", err
				}
				// Git does not follow symlinks when reading .gitignore files.
				if err == nil && info.Mode().IsRegular() {
					contents, err := root.ReadFile(path)
					if err != nil {
						return "", err
					}
					if err := os.MkdirAll(filepath.Join(temporary, directory), 0o700); err != nil {
						return "", err
					}
					if err := os.WriteFile(filepath.Join(temporary, path), contents, 0o600); err != nil {
						return "", err
					}
				}
			}
			if directory == "." {
				break
			}
		}
	}
	failed = false
	return temporary, nil
}

func validateIncomingBranch(ctx context.Context, target string, state CheckoutState) error {
	if err := PreflightCheckoutBranch(ctx, target, state); err != nil {
		return err
	}
	if state.Detached {
		return nil
	}
	ref := "refs/heads/" + state.Branch
	if _, refErr := git(ctx, commandRunner{}, target, "show-ref", "--verify", "--quiet", ref); refErr == nil {
		current, currentErr := git(ctx, commandRunner{}, target, "rev-parse", "--verify", ref)
		if currentErr != nil {
			return currentErr
		}
		old := strings.TrimSpace(string(current))
		if _, ancestorErr := git(ctx, commandRunner{}, target, "merge-base", "--is-ancestor", old, state.HEAD); ancestorErr != nil {
			return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination branch %q contains commits not reachable from the source", state.Branch)}
		}
	} else if exitCode(refErr) != 1 {
		return refErr
	}
	return nil
}

// PreflightCheckoutBranch rejects branch topology that cannot be applied to
// this Worktree without disturbing another linked Worktree.
func PreflightCheckoutBranch(ctx context.Context, target string, state CheckoutState) error {
	if state.Detached {
		return nil
	}
	if state.Branch == "" {
		return &Error{Code: CodeInvalidInput, Message: "workspace branch metadata is invalid"}
	}
	if _, err := git(ctx, commandRunner{}, target, "check-ref-format", "--branch", state.Branch); err != nil {
		return &Error{Code: CodeInvalidInput, Message: "workspace branch name is invalid", Cause: err}
	}
	incomingRef := "refs/heads/" + state.Branch
	if _, err := git(ctx, commandRunner{}, target, "symbolic-ref", "--quiet", "--no-recurse", incomingRef); err == nil {
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination branch %q is a symbolic ref and cannot be updated safely", state.Branch)}
	} else if exitCode(err) != 1 {
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("could not verify destination branch %q ref type", state.Branch), Cause: err}
	}
	raw, err := git(ctx, commandRunner{}, target, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return &Error{Code: CodeConflict, Message: "could not verify the destination Repository's complete Worktree topology", Cause: err}
	}
	worktrees, err := parseGitWorktreeList(raw)
	if err != nil {
		return err
	}
	targetPath, err := canonicalPath(target)
	if err != nil {
		return &Error{Code: CodeConflict, Message: "could not canonicalize the destination Worktree while checking branch ownership", Cause: err}
	}
	for _, worktree := range worktrees {
		if worktree.Branch != incomingRef {
			continue
		}
		worktreePath, pathErr := canonicalPath(worktree.Path)
		if pathErr == nil && worktreePath == targetPath {
			continue
		}
		if pathErr != nil && filepath.Clean(worktree.Path) == filepath.Clean(target) {
			continue
		}
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("branch %q is already checked out in Worktree %q", state.Branch, worktree.Path), Cause: pathErr}
	}
	return nil
}

type gitWorktreeRecord struct {
	Path     string
	Branch   string
	Detached bool
	Bare     bool
}

func parseGitWorktreeList(raw []byte) ([]gitWorktreeRecord, error) {
	if len(raw) < 2 || !bytes.HasSuffix(raw, []byte{0, 0}) {
		return nil, &Error{Code: CodeConflict, Message: "Git returned malformed Worktree topology"}
	}
	recordBytes := bytes.Split(raw[:len(raw)-2], []byte{0, 0})
	records := make([]gitWorktreeRecord, 0, len(recordBytes))
	for _, encoded := range recordBytes {
		fields := bytes.Split(encoded, []byte{0})
		if len(fields) < 2 || !bytes.HasPrefix(fields[0], []byte("worktree ")) {
			return nil, &Error{Code: CodeConflict, Message: "Git returned malformed Worktree topology"}
		}
		record := gitWorktreeRecord{Path: string(bytes.TrimPrefix(fields[0], []byte("worktree ")))}
		if record.Path == "" || !filepath.IsAbs(record.Path) || !utf8.ValidString(record.Path) || hasControl(record.Path) {
			return nil, &Error{Code: CodeConflict, Message: "Git returned an invalid Worktree path"}
		}
		headSeen := false
		branchSeen := false
		for _, field := range fields[1:] {
			value := string(field)
			switch {
			case strings.HasPrefix(value, "HEAD "):
				if headSeen || !validGitObjectID(strings.TrimPrefix(value, "HEAD ")) {
					return nil, &Error{Code: CodeConflict, Message: "Git returned malformed Worktree topology"}
				}
				headSeen = true
			case strings.HasPrefix(value, "branch "):
				if branchSeen || record.Detached || !strings.HasPrefix(strings.TrimPrefix(value, "branch "), "refs/heads/") {
					return nil, &Error{Code: CodeConflict, Message: "Git returned malformed Worktree topology"}
				}
				record.Branch = strings.TrimPrefix(value, "branch ")
				branchSeen = true
			case value == "detached":
				if branchSeen || record.Detached {
					return nil, &Error{Code: CodeConflict, Message: "Git returned malformed Worktree topology"}
				}
				record.Detached = true
			case value == "bare":
				if record.Bare {
					return nil, &Error{Code: CodeConflict, Message: "Git returned malformed Worktree topology"}
				}
				record.Bare = true
			case value == "locked", strings.HasPrefix(value, "locked "), value == "prunable", strings.HasPrefix(value, "prunable "):
				// These documented porcelain fields do not affect branch ownership.
			default:
				return nil, &Error{Code: CodeConflict, Message: "Git returned unknown Worktree topology data"}
			}
		}
		if record.Bare {
			if headSeen || branchSeen || record.Detached {
				return nil, &Error{Code: CodeConflict, Message: "Git returned malformed bare Repository topology"}
			}
		} else if !headSeen || (!branchSeen && !record.Detached) {
			return nil, &Error{Code: CodeConflict, Message: "Git returned incomplete Worktree topology"}
		}
		records = append(records, record)
	}
	return records, nil
}

type checkoutHEADUpdate struct {
	target             string
	oldBinding         string
	oldHEAD            string
	incomingRef        string
	oldIncomingRef     string
	incomingRefExisted bool
	newHEAD            string
	headChanged        bool
	incomingRefChanged bool
	incarnation        *checkoutIncarnation
}

func (update *checkoutHEADUpdate) Restore(ctx context.Context) error {
	if update == nil {
		return nil
	}
	if update.incarnation != nil {
		if err := update.incarnation.Validate(ctx, update.target); err != nil {
			return &Error{Code: CodeOutcomeUnknown, Message: "destination HEAD cannot be restored because its Worktree incarnation changed", Cause: err}
		}
	}
	if update.incomingRefChanged {
		var err error
		if update.incomingRefExisted {
			_, err = git(ctx, commandRunner{}, update.target, "update-ref", "--no-deref", update.incomingRef, update.oldIncomingRef, update.newHEAD)
		} else {
			_, err = git(ctx, commandRunner{}, update.target, "update-ref", "--no-deref", "-d", update.incomingRef, update.newHEAD)
		}
		if err != nil {
			return &Error{Code: CodeConflict, Message: "incoming destination branch changed independently before rollback", Cause: err}
		}
	}
	if !update.headChanged {
		return nil
	}
	if update.oldBinding != "" {
		binding, head, err := inspectCheckoutHEAD(ctx, commandRunner{}, update.target)
		if err != nil {
			return err
		}
		if update.incomingRef != "" {
			if binding != update.incomingRef {
				return &Error{Code: CodeConflict, Message: "destination HEAD binding changed independently before rollback"}
			}
		} else if binding != "" || head != update.newHEAD {
			return &Error{Code: CodeConflict, Message: "destination HEAD changed independently before rollback"}
		}
		_, err = git(ctx, commandRunner{}, update.target, "symbolic-ref", "HEAD", update.oldBinding)
		return err
	}
	expected := update.newHEAD
	if update.incomingRef != "" {
		binding, _, inspectErr := inspectCheckoutHEAD(ctx, commandRunner{}, update.target)
		if inspectErr != nil {
			return inspectErr
		}
		if binding != update.incomingRef {
			return &Error{Code: CodeConflict, Message: "destination HEAD binding changed independently before rollback"}
		}
		// HEAD is still symbolic to the incoming ref. Detach it back to the
		// captured commit without dereferencing that restored ref.
		expected = update.oldIncomingRef
		if !update.incomingRefExisted {
			expected = "0000000000000000000000000000000000000000"
		}
	}
	_, err := git(ctx, commandRunner{}, update.target, "update-ref", "--no-deref", "HEAD", update.oldHEAD, expected)
	return err
}

func updateCheckoutHEAD(ctx context.Context, target string, state CheckoutState) (*checkoutHEADUpdate, error) {
	return updateCheckoutHEADWithBranchRewind(ctx, target, state, "")
}

func updateCheckoutHEADWithBranchRewind(ctx context.Context, target string, state CheckoutState, rewindHEAD string) (*checkoutHEADUpdate, error) {
	incarnation, err := captureCheckoutIncarnation(ctx, target)
	if err != nil {
		return nil, err
	}
	update, updateErr := updateCheckoutHEADWithRunnerOption(ctx, commandRunner{}, target, state, rewindHEAD)
	if update != nil {
		update.incarnation = incarnation
	}
	return update, updateErr
}

func updateCheckoutHEADWithRunner(ctx context.Context, commands runner, target string, state CheckoutState) (*checkoutHEADUpdate, error) {
	return updateCheckoutHEADWithRunnerOption(ctx, commands, target, state, "")
}

func updateCheckoutHEADWithRunnerOption(ctx context.Context, commands runner, target string, state CheckoutState, rewindHEAD string) (*checkoutHEADUpdate, error) {
	oldRef, oldHEAD, err := inspectCheckoutHEAD(ctx, commands, target)
	if err != nil {
		return nil, err
	}
	update := &checkoutHEADUpdate{target: target, oldBinding: oldRef, oldHEAD: oldHEAD, newHEAD: state.HEAD}
	if state.Detached {
		expected := oldHEAD
		if expected == "" {
			expected = "0000000000000000000000000000000000000000"
		}
		if _, err = git(ctx, commands, target, "update-ref", "--no-deref", "HEAD", state.HEAD, expected); err != nil {
			return reconcileDetachedHEADUpdate(ctx, commands, update, err)
		}
		update.headChanged = oldRef != "" || oldHEAD != state.HEAD
		return update, nil
	}
	if err := PreflightCheckoutBranch(ctx, target, state); err != nil {
		return nil, err
	}
	ref := "refs/heads/" + state.Branch
	update.incomingRef = ref
	currentRef, exists, err := inspectCheckoutRef(ctx, commands, target, ref)
	if err != nil {
		return nil, fmt.Errorf("inspect destination branch: %w", err)
	}
	update.oldIncomingRef = currentRef
	update.incomingRefExisted = exists
	if rewindHEAD != "" && (!exists || currentRef != rewindHEAD || oldHEAD != rewindHEAD || oldRef != ref) {
		return nil, &Error{Code: CodeConflict, Message: "destination branch changed before workspace rollback or seed rewind"}
	}
	if exists && rewindHEAD == "" {
		if _, err = git(ctx, commands, target, "merge-base", "--is-ancestor", currentRef, state.HEAD); err != nil {
			return nil, &Error{Code: CodeConflict, Message: fmt.Sprintf("destination branch %q contains commits not reachable from the source", state.Branch)}
		}
	}
	// Attach HEAD before moving the shared branch ref. Git's normal Worktree
	// ownership checks can then stop another non-forced checkout from claiming
	// the branch during the final ref update.
	if _, err = git(ctx, commands, target, "symbolic-ref", "HEAD", ref); err != nil {
		return reconcileSymbolicHEADUpdate(ctx, commands, update, fmt.Errorf("bind destination HEAD to incoming branch: %w", err))
	}
	update.headChanged = oldRef != ref
	if err := PreflightCheckoutBranch(ctx, target, state); err != nil {
		return update, err
	}
	if exists {
		if _, err = git(ctx, commands, target, "update-ref", "--no-deref", ref, state.HEAD, currentRef); err != nil {
			return reconcileBranchRefUpdate(ctx, commands, update, fmt.Errorf("advance destination branch: %w", err))
		}
	} else {
		if _, err = git(ctx, commands, target, "update-ref", "--no-deref", ref, state.HEAD, "0000000000000000000000000000000000000000"); err != nil {
			return reconcileBranchRefUpdate(ctx, commands, update, fmt.Errorf("create destination branch: %w", err))
		}
	}
	update.incomingRefChanged = !exists || currentRef != state.HEAD
	return update, nil
}

func inspectCheckoutHEAD(ctx context.Context, commands runner, target string) (string, string, error) {
	refOutput, refErr := git(ctx, commands, target, "symbolic-ref", "-q", "--no-recurse", "HEAD")
	if refErr != nil && exitCode(refErr) != 1 {
		return "", "", fmt.Errorf("inspect destination HEAD binding: %w", refErr)
	}
	ref := strings.TrimSpace(string(refOutput))
	headOutput, headErr := git(ctx, commands, target, "rev-parse", "--verify", "HEAD")
	head := strings.TrimSpace(string(headOutput))
	if headErr != nil && ref == "" {
		return "", "", fmt.Errorf("inspect detached destination HEAD: %w", headErr)
	}
	return ref, head, nil
}

func inspectCheckoutRef(ctx context.Context, commands runner, target, ref string) (string, bool, error) {
	_, err := git(ctx, commands, target, "show-ref", "--verify", "--quiet", ref)
	if exitCode(err) == 1 {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	current, err := git(ctx, commands, target, "rev-parse", "--verify", ref)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(string(current)), true, nil
}

func reconcileDetachedHEADUpdate(ctx context.Context, commands runner, update *checkoutHEADUpdate, cause error) (*checkoutHEADUpdate, error) {
	binding, head, err := inspectCheckoutHEAD(context.WithoutCancel(ctx), commands, update.target)
	if err != nil {
		return update, ambiguousCheckoutHEADMutation("destination HEAD", cause, err)
	}
	if binding == "" && head == update.newHEAD {
		update.headChanged = update.oldBinding != "" || update.oldHEAD != update.newHEAD
		return update, cause
	}
	if binding == update.oldBinding && head == update.oldHEAD {
		return update, cause
	}
	return update, ambiguousCheckoutHEADMutation("destination HEAD", cause, nil)
}

func reconcileSymbolicHEADUpdate(ctx context.Context, commands runner, update *checkoutHEADUpdate, cause error) (*checkoutHEADUpdate, error) {
	binding, head, err := inspectCheckoutHEAD(context.WithoutCancel(ctx), commands, update.target)
	if err != nil {
		return update, ambiguousCheckoutHEADMutation("destination HEAD binding", cause, err)
	}
	if binding == update.incomingRef {
		update.headChanged = update.oldBinding != update.incomingRef
		return update, cause
	}
	if binding == update.oldBinding && head == update.oldHEAD {
		return update, cause
	}
	return update, ambiguousCheckoutHEADMutation("destination HEAD binding", cause, nil)
}

func reconcileBranchRefUpdate(ctx context.Context, commands runner, update *checkoutHEADUpdate, cause error) (*checkoutHEADUpdate, error) {
	current, exists, err := inspectCheckoutRef(context.WithoutCancel(ctx), commands, update.target, update.incomingRef)
	if err != nil {
		return update, ambiguousCheckoutHEADMutation("destination branch", cause, err)
	}
	if exists && current == update.newHEAD {
		update.incomingRefChanged = !update.incomingRefExisted || update.oldIncomingRef != update.newHEAD
		return update, cause
	}
	if exists == update.incomingRefExisted && (!exists || current == update.oldIncomingRef) {
		return update, cause
	}
	return update, ambiguousCheckoutHEADMutation("destination branch", cause, nil)
}

func ambiguousCheckoutHEADMutation(subject string, cause, inspectErr error) error {
	causes := []error{cause}
	if inspectErr != nil {
		causes = append(causes, inspectErr)
	}
	return &Error{Code: CodeOutcomeUnknown, Message: subject + " mutation outcome could not be reconciled safely", Cause: errors.Join(causes...)}
}

func installCheckoutEntry(target, sourceRoot string, entry CheckoutFile, previous *CheckoutFile) error {
	root, err := os.OpenRoot(target)
	if err != nil {
		return err
	}
	defer root.Close()
	return installCheckoutEntryAt(root, sourceRoot, entry, previous)
}

func installCheckoutEntryAt(root *os.Root, sourceRoot string, entry CheckoutFile, previous *CheckoutFile) error {
	if err := validateCheckoutDestinationParents(root, entry.Path); err != nil {
		return err
	}
	destination := filepath.FromSlash(entry.Path)
	parent := filepath.Dir(destination)
	if parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	temporaryPath, err := stageCheckoutEntry(root, sourceRoot, entry)
	if err != nil {
		return err
	}
	defer root.Remove(temporaryPath)
	return promoteCheckoutEntry(root, destination, temporaryPath, entry.Path, previous)
}

func stageCheckoutEntry(root *os.Root, sourceRoot string, entry CheckoutFile) (string, error) {
	return stageCheckoutEntryBeside(root, sourceRoot, entry, entry.Path)
}

func stageCheckoutEntryBeside(root *os.Root, sourceRoot string, entry CheckoutFile, beside string) (string, error) {
	source := filepath.Join(sourceRoot, filepath.FromSlash(entry.Path))
	temporaryPath, err := checkoutTemporaryPath(beside)
	if err != nil {
		return "", err
	}
	temporary, err := root.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	failed := true
	defer func() {
		if failed {
			_ = root.Remove(temporaryPath)
		}
	}()
	if entry.Kind == "symlink" {
		targetBytes, err := os.ReadFile(source)
		if err != nil {
			_ = temporary.Close()
			return "", err
		}
		targetValue := string(targetBytes)
		if err = temporary.Close(); err != nil {
			return "", err
		}
		if err = root.Remove(temporaryPath); err != nil {
			return "", err
		}
		if err = root.Symlink(targetValue, temporaryPath); err != nil {
			return "", err
		}
		failed = false
		return temporaryPath, nil
	}
	input, err := os.Open(source)
	if err != nil {
		_ = temporary.Close()
		return "", err
	}
	defer input.Close()
	mode := os.FileMode(0o644)
	if entry.Executable {
		mode = 0o755
	}
	if _, err = io.Copy(temporary, input); err == nil {
		err = temporary.Chmod(mode)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	failed = false
	return temporaryPath, nil
}

// checkoutStagingBase keeps temporary files adjacent to ordinary paths, and
// outside the highest file/directory replacement that contains a path.
func checkoutStagingBase(relative string, destination, incoming map[string]CheckoutFile) string {
	base := relative
	for parent := filepath.ToSlash(filepath.Dir(relative)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
		_, oldLeaf := destination[parent]
		_, newLeaf := incoming[parent]
		if oldLeaf || newLeaf {
			base = parent
		}
	}
	return base
}

func checkoutTemporaryPath(relative string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(filepath.FromSlash(relative)), ".schooner-file-"+hex.EncodeToString(random)), nil
}

func promoteCheckoutEntry(root *os.Root, destination, temporaryPath, relative string, previous *CheckoutFile) error {
	if err := validateCheckoutDestinationParents(root, relative); err != nil {
		return err
	}
	if previous == nil {
		if err := root.Link(temporaryPath, destination); err != nil {
			return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination path %q appeared while workspace push was applying", relative), Cause: err}
		}
		if err := root.Remove(temporaryPath); err != nil {
			return err
		}
		return nil
	}
	current, present, err := checkoutFileOnRoot(root, relative)
	if err != nil {
		return err
	}
	if !present || !checkoutFileContentEqual(current, *previous) {
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination path %q changed immediately before replacement", relative)}
	}
	return root.Rename(temporaryPath, destination)
}

func copyPayloadRegular(reader io.Reader, target string, size int64, mode os.FileMode, expectedDigest string) error {
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(file, hash), reader, size)
	closeErr := file.Close()
	if copyErr != nil || written != size {
		return &Error{Code: CodeInvalidInput, Message: "workspace payload file is truncated", Cause: copyErr}
	}
	if closeErr != nil {
		return closeErr
	}
	if expectedDigest != "" && hex.EncodeToString(hash.Sum(nil)) != expectedDigest {
		return &Error{Code: CodeInvalidInput, Message: "workspace payload file digest does not match its manifest"}
	}
	return nil
}

func ensurePayloadParents(root, parent string) error {
	if !within(root, parent) {
		return &Error{Code: CodeInvalidInput, Message: "workspace payload path escapes its staging directory"}
	}
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		rootInfo, err = os.Lstat(root)
	}
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return &Error{Code: CodeInvalidInput, Message: "workspace payload root is not a real directory"}
	}
	relative, err := filepath.Rel(root, parent)
	if err != nil {
		return &Error{Code: CodeInvalidInput, Message: "workspace payload parent is invalid", Cause: err}
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if mkdirErr := os.Mkdir(current, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return mkdirErr
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return &Error{Code: CodeInvalidInput, Message: "workspace payload parent is not a real directory"}
		}
	}
	return nil
}

func ensureNoParentSymlink(root, parent string) error {
	if !within(root, parent) {
		return &Error{Code: CodeInvalidInput, Message: "workspace path escapes its Worktree"}
	}
	for current := parent; current != root; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return &Error{Code: CodeConflict, Message: fmt.Sprintf("destination parent %q is not a real directory", current)}
		}
	}
	return nil
}
