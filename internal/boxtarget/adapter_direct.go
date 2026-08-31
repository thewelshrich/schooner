package boxtarget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
	"github.com/thewelshrich/schooner/internal/session"
	"github.com/thewelshrich/schooner/internal/source"
	"github.com/thewelshrich/schooner/internal/workspacetransfer"
)

type directAdapter struct {
	runtime        *host.Runtime
	state          *targetState
	nonInteractive bool
}

func (a directAdapter) listWorktrees(ctx context.Context) (repository.Catalog, error) {
	result, err := a.runtime.ListWorktrees(ctx, hostruntime.NewWorktreeRequest("", a.state.boxIdentity))
	return result.Catalog, err
}

func (a directAdapter) listContextWorktrees(ctx context.Context) (repository.Catalog, error) {
	return a.listWorktrees(ctx)
}

func (a directAdapter) inspectWorktree(ctx context.Context, selector string) (repository.Inspection, error) {
	result, err := a.runtime.InspectWorktree(ctx, hostruntime.NewWorktreeRequest(selector, a.state.boxIdentity))
	return result.Inspection, err
}

func (a directAdapter) observePushDestination(ctx context.Context, worktree string) (*repository.CheckoutState, error) {
	result, err := a.runtime.InspectWorkspacePush(ctx, hostruntime.NewWorkspacePushInspectRequest(worktree, a.state.boxIdentity))
	return result.State, err
}

func (a directAdapter) preflightPushDestination(ctx context.Context, worktree string, source repository.CheckoutState, branch bool) (workspacetransfer.PreflightResult, error) {
	request := hostruntime.NewWorkspacePushInspectRequest(worktree, a.state.boxIdentity)
	request.IncomingFiles = source.Files
	request.IncomingBranch = source.Branch
	request.IncomingDetached = source.Detached
	request.IncomingStateDigest = source.Digest
	request.PreflightBranch = branch
	result, err := a.runtime.InspectWorkspacePush(ctx, request)
	return workspacetransfer.PreflightResult{ExistingFiles: result.ExistingFiles, MatchingFiles: result.MatchingFiles}, err
}

func (a directAdapter) applyPush(ctx context.Context, request workspacetransfer.ApplyRequest, payload io.Reader) (workspacetransfer.ApplyResult, error) {
	remote := hostruntime.NewWorkspacePushApplyRequest(request.OperationID, request.RemoteWorktree, request.ExpectedStateDigest, request.PayloadSHA256, request.PayloadSize, request.SourceState.Digest, a.state.boxIdentity)
	remote.OperationCreatedDestination = request.OperationCreatedDestination
	remote.OperationCreatedBranch = request.OperationCreatedBranch
	result, err := a.runtime.ApplyWorkspacePush(ctx, remote, payload)
	return workspacetransfer.ApplyResult{State: result.State, BytesTransferred: result.BytesTransferred}, err
}

func (a directAdapter) inspectPullSource(ctx context.Context, request workspacetransfer.PullInspectRequest) (workspacetransfer.PullInspection, error) {
	remote := hostruntime.NewWorkspacePullInspectRequest(request.RemoteWorktree, request.DestinationHEAD, a.state.boxIdentity, request.IncludeManifest)
	var state repository.CheckoutState
	ancestor := false
	for {
		result, err := a.runtime.InspectWorkspacePull(ctx, remote)
		if err != nil {
			return workspacetransfer.PullInspection{}, err
		}
		if state.Digest == "" {
			state = result.State
			ancestor = result.DestinationAncestor
		}
		for _, entry := range result.Manifest {
			if entry.Kind == "absent" {
				state.AbsentPaths = append(state.AbsentPaths, entry.Path)
			} else {
				state.Files = append(state.Files, entry)
			}
		}
		if !request.IncludeManifest || result.ManifestComplete {
			return workspacetransfer.PullInspection{State: state, DestinationAncestor: ancestor}, nil
		}
		remote.ManifestOffset = result.NextManifestOffset
		remote.ExpectedSourceRevalidationDigest = state.RevalidationDigest
	}
}

func (a directAdapter) capturePullSource(ctx context.Context, request workspacetransfer.PullCaptureRequest) (workspacetransfer.PullCapture, error) {
	remote := hostruntime.NewWorkspacePullCaptureRequest(request.OperationID, request.RemoteWorktree, request.DestinationHEAD, request.ExpectedSourceRevalidationDigest, a.state.boxIdentity)
	result, captured, err := a.runtime.CaptureWorkspacePull(ctx, remote)
	if err != nil {
		return workspacetransfer.PullCapture{}, err
	}
	defer captured.Release()
	if err = os.MkdirAll(request.Staging, 0o700); err != nil {
		return workspacetransfer.PullCapture{}, err
	}
	source, err := os.Open(captured.PayloadPath)
	if err != nil {
		return workspacetransfer.PullCapture{}, err
	}
	defer source.Close()
	destination, err := os.CreateTemp(request.Staging, ".pull-download-*.tar")
	if err != nil {
		return workspacetransfer.PullCapture{}, err
	}
	path := destination.Name()
	failed := true
	defer func() {
		_ = destination.Close()
		if failed {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, hash), source)
	if closeErr := destination.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return workspacetransfer.PullCapture{}, fmt.Errorf("copy workspace pull payload: %w", copyErr)
	}
	if written != result.PayloadSize || hex.EncodeToString(hash.Sum(nil)) != result.PayloadSHA256 {
		return workspacetransfer.PullCapture{}, &repository.Error{Code: repository.CodeConflict, Message: "workspace pull payload failed integrity verification"}
	}
	failed = false
	return workspacetransfer.PullCapture{Capture: repository.CheckoutCapture{State: result.State, PayloadPath: path, PayloadSize: written, PayloadSHA256: result.PayloadSHA256}, DestinationAncestor: result.DestinationAncestor}, nil
}

func (a directAdapter) cloneRepository(ctx context.Context, request repository.CloneRequest) (repository.MutationResult, error) {
	remote := hostruntime.NewCloneRequest(request.Source, request.Branch, a.state.worktreeRoot, a.state.boxIdentity)
	remote.Destination = request.Destination
	remote.NonInteractive = a.nonInteractive
	result, err := a.runtime.CloneRepositoryV2(ctx, remote)
	return result.MutationResult, err
}

func (a directAdapter) addWorktree(ctx context.Context, request repository.AddRequest) (repository.MutationResult, error) {
	remote := hostruntime.NewWorktreeMutationRequest(request.RepositoryPath, request.Path, request.Branch, a.state.worktreeRoot, a.state.boxIdentity)
	remote.NonInteractive = a.nonInteractive
	result, err := a.runtime.AddWorktree(ctx, remote)
	return result.MutationResult, err
}

func (a directAdapter) removeWorktree(ctx context.Context, path string) (repository.MutationResult, error) {
	request := hostruntime.NewWorktreeMutationRequest("", path, "", a.state.worktreeRoot, a.state.boxIdentity)
	request.NonInteractive = a.nonInteractive
	result, err := a.runtime.RemoveWorktree(ctx, request)
	return result.MutationResult, err
}

func (a directAdapter) pruneWorktrees(ctx context.Context) (repository.MutationResult, error) {
	request := hostruntime.NewWorktreeMutationRequest("", "", "", a.state.worktreeRoot, a.state.boxIdentity)
	request.NonInteractive = a.nonInteractive
	result, err := a.runtime.PruneWorktrees(ctx, request)
	return result.MutationResult, err
}

func (a directAdapter) listSessions(ctx context.Context) (session.Catalog, error) {
	result, err := a.runtime.ListSessions(ctx, hostruntime.NewSessionListRequest(a.state.worktreeRoot, a.state.boxIdentity))
	return result.Catalog, err
}

func (a directAdapter) startSession(ctx context.Context, worktree string) (session.StartResult, error) {
	result, err := a.runtime.StartSession(ctx, hostruntime.NewSessionStartRequest(a.state.worktreeRoot, a.state.boxIdentity, worktree))
	if err == nil {
		err = a.state.validateRoot(result.WorktreeRoot)
	}
	return result.StartResult, err
}

func (a directAdapter) resumeSession(ctx context.Context, selector string, terminal Terminal) (HandoffResult, error) {
	result, err := a.runtime.ResumeSession(ctx, hostruntime.NewSessionTargetRequest(a.state.worktreeRoot, a.state.boxIdentity, selector), host.TerminalIO{In: terminal.In, Out: terminal.Out, Err: terminal.Err})
	return HandoffResult{ExitCode: result.ExitCode}, err
}

func (a directAdapter) sessionLogs(ctx context.Context, id string, lines int) (session.LogsResult, error) {
	result, err := a.runtime.SessionLogs(ctx, hostruntime.NewSessionLogsRequest(a.state.worktreeRoot, a.state.boxIdentity, id, lines))
	if err == nil {
		err = a.state.validateRoot(result.WorktreeRoot)
	}
	return result.LogsResult, err
}

func (a directAdapter) stopSession(ctx context.Context, id string) (session.StopResult, error) {
	result, err := a.runtime.StopSession(ctx, hostruntime.NewSessionTargetRequest(a.state.worktreeRoot, a.state.boxIdentity, id))
	if err == nil {
		err = a.state.validateRoot(result.WorktreeRoot)
	}
	return result.StopResult, err
}

func (a directAdapter) openWorktreeShell(ctx context.Context, worktree string, terminal Terminal) (HandoffResult, error) {
	result, err := a.runtime.OpenWorktreeShell(ctx, hostruntime.NewWorktreeShellRequest(a.state.worktreeRoot, a.state.boxIdentity, worktree), host.TerminalIO{In: terminal.In, Out: terminal.Out, Err: terminal.Err})
	return HandoffResult{ExitCode: result.ExitCode}, err
}

func (a directAdapter) inspectSourceIdentity(ctx context.Context, provider string) (source.HostIdentity, error) {
	result, err := a.runtime.InspectSourceIdentity(ctx, hostruntime.NewSourceIdentityRequest(provider, a.state.boxIdentity))
	return result.HostIdentity, err
}

func (a directAdapter) ensureSourceIdentity(ctx context.Context, request source.EnsureIdentityRequest) (source.HostIdentity, error) {
	remote := hostruntime.NewSourceIdentityEnsureRequest(request.Provider, a.state.boxIdentity, request.HostKeys)
	result, err := a.runtime.EnsureSourceIdentity(ctx, remote)
	return result.HostIdentity, err
}

func (a directAdapter) removeSourceIdentity(ctx context.Context, request source.RemoveIdentityRequest) (source.RemoveIdentityResult, error) {
	result, err := a.runtime.RemoveSourceIdentity(ctx, hostruntime.NewSourceIdentityRemoveRequest(request.Provider, a.state.boxIdentity, request.ExpectedFingerprint))
	return result.RemoveIdentityResult, err
}

func (a directAdapter) verifySourceRepository(ctx context.Context, request source.VerifyRequest) (source.VerifyResult, error) {
	remote := hostruntime.NewSourceRepositoryVerifyRequest(request.Provider, request.Repository, a.state.boxIdentity)
	result, err := a.runtime.VerifySourceRepository(ctx, remote)
	return result.VerifyResult, err
}
