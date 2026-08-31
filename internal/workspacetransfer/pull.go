package workspacetransfer

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	"github.com/thewelshrich/schooner/internal/repository"
)

type PullInspectRequest struct {
	RemoteWorktree  string
	DestinationHEAD string
	IncludeManifest bool
}

type PullInspection struct {
	State               repository.CheckoutState
	DestinationAncestor bool
}

type PullCaptureRequest struct {
	OperationID                      string
	RemoteWorktree                   string
	DestinationHEAD                  string
	ExpectedSourceRevalidationDigest string
	Staging                          string
}

type PullCapture struct {
	Capture             repository.CheckoutCapture
	DestinationAncestor bool
}

// PullSource is the direct/SSH seam for authoritative remote observation and
// one-shot capture. Transport-specific paging and streaming remain hidden
// behind these two operations.
type PullSource interface {
	InspectPullSource(context.Context, PullInspectRequest) (PullInspection, error)
	CapturePullSource(context.Context, PullCaptureRequest) (PullCapture, error)
}

type PullRequest struct {
	LocalWorktree      string
	RemoteWorktree     string
	Staging            string
	LockStateDirectory string
	DryRun             bool
	Source             PullSource
}

type PullResult struct {
	Action           Action
	Source           repository.CheckoutState
	Destination      repository.CheckoutState
	RemoteWorktree   string
	FilesChanged     int
	BytesTransferred int64
}

func Pull(ctx context.Context, request PullRequest) (PullResult, error) {
	if request.Source == nil || request.LocalWorktree == "" || request.RemoteWorktree == "" || request.Staging == "" || (!request.DryRun && request.LockStateDirectory == "") {
		return PullResult{}, fmt.Errorf("workspace pull is not configured")
	}
	destination, err := repository.ObserveCheckout(ctx, request.LocalWorktree)
	if err != nil {
		return PullResult{}, err
	}
	inspection, err := request.Source.InspectPullSource(ctx, PullInspectRequest{
		RemoteWorktree: request.RemoteWorktree, DestinationHEAD: destination.HEAD, IncludeManifest: request.DryRun,
	})
	if err != nil {
		return PullResult{}, err
	}
	result := PullResult{Source: inspection.State, Destination: destination, RemoteWorktree: request.RemoteWorktree}
	if inspection.State.RepositoryIdentity != "" && destination.RepositoryIdentity != "" && inspection.State.RepositoryIdentity != destination.RepositoryIdentity {
		return pullConflict(result, "Pull stopped: the local and remote Worktrees belong to different network repositories", nil)
	}
	if inspection.State.Digest == destination.Digest {
		result.Action = ActionNoChange
		return result, nil
	}
	if dirty(destination.Status) {
		return pullConflict(result, "Pull stopped: the local Worktree contains changes that would be overwritten", statusContext(destination.Status))
	}
	if !inspection.DestinationAncestor {
		return pullConflict(result, "Pull stopped: the local Worktree contains commits not reachable from the remote Worktree", nil)
	}
	if request.DryRun {
		if err = repository.PreflightCheckoutApplication(ctx, request.LocalWorktree, destination, inspection.State.Files, inspection.State.AbsentPaths); err != nil {
			return pullRepositoryConflict(result, err)
		}
		result.FilesChanged = checkoutFilesChanged(inspection.State, destination)
		result.Action = ActionWouldPull
		return result, nil
	}

	operationID, err := newOperationID()
	if err != nil {
		return result, err
	}
	downloaded, err := request.Source.CapturePullSource(ctx, PullCaptureRequest{
		OperationID: operationID, RemoteWorktree: request.RemoteWorktree, DestinationHEAD: destination.HEAD,
		ExpectedSourceRevalidationDigest: inspection.State.RevalidationDigest, Staging: request.Staging,
	})
	if err != nil {
		return pullOperationFailure(result, err)
	}
	defer downloaded.Capture.Release()
	if !downloaded.DestinationAncestor || downloaded.Capture.State.RevalidationDigest != inspection.State.RevalidationDigest {
		return pullConflict(result, "Pull stopped: the remote Worktree changed before its workspace could be captured", nil)
	}
	extracted, err := repository.ExtractCheckoutPayload(downloaded.Capture.PayloadPath, request.Staging)
	if err != nil {
		return pullOperationFailure(result, err)
	}
	defer extracted.Release()
	if extracted.State.Digest != inspection.State.Digest || extracted.State.RevalidationDigest != inspection.State.RevalidationDigest || extracted.State.Digest != downloaded.Capture.State.Digest {
		return pullConflict(result, "Pull stopped: the downloaded workspace does not match the inspected remote state", nil)
	}
	result.Source = extracted.State
	if err = repository.PreflightCheckoutApplication(ctx, request.LocalWorktree, destination, extracted.State.Files, extracted.State.AbsentPaths); err != nil {
		return pullRepositoryConflict(result, err)
	}
	result.FilesChanged = checkoutFilesChanged(extracted.State, destination)
	applied, err := repository.ApplyCheckoutTransaction(ctx, request.LocalWorktree, extracted, repository.CheckoutTransactionOptions{
		ExpectedStateDigest: destination.RevalidationDigest, LockStateDirectory: request.LockStateDirectory, StagingDirectory: request.Staging,
	})
	if err != nil {
		return pullOperationFailure(result, err)
	}
	if applied.Digest != extracted.State.Digest {
		return result, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "local workspace verification returned a different state"}
	}
	result.Action = ActionPulled
	result.Destination = applied
	result.BytesTransferred = downloaded.Capture.PayloadSize
	return result, nil
}

func pullConflict(result PullResult, message string, contextValues map[string]string) (PullResult, error) {
	result.Action = ActionConflict
	return result, &Error{Code: CodeConflict, Operation: "pull", Message: message, Context: contextValues}
}

func pullRepositoryConflict(result PullResult, err error) (PullResult, error) {
	if repository.ErrorCode(err) != repository.CodeConflict {
		return result, err
	}
	return pullConflict(result, "Pull stopped: "+err.Error(), nil)
}

func pullOperationFailure(result PullResult, err error) (PullResult, error) {
	if repository.ErrorCode(err) == repository.CodeOutcomeUnknown {
		return result, err
	}
	if errors.Is(err, syscall.ENOSPC) {
		return result, &Error{Code: CodeInsufficientSpace, Operation: "pull", Message: "Pull stopped: there is not enough disk space to transfer the workspace", Cause: err}
	}
	return pullRepositoryConflict(result, err)
}

func checkoutFilesChanged(source, destination repository.CheckoutState) int {
	destinationFiles := make(map[string]repository.CheckoutFile, len(destination.Files))
	for _, entry := range destination.Files {
		destinationFiles[entry.Path] = entry
	}
	existing, matching := 0, 0
	for _, entry := range source.Files {
		other, ok := destinationFiles[entry.Path]
		if !ok {
			continue
		}
		existing++
		if entry.Kind == other.Kind && entry.Executable == other.Executable && entry.Size == other.Size && entry.SHA256 == other.SHA256 {
			matching++
		}
	}
	return source.FileCount + destination.FileCount - existing - matching
}
