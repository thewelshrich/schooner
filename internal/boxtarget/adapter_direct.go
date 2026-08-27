package boxtarget

import (
	"context"

	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
	"github.com/thewelshrich/schooner/internal/session"
	"github.com/thewelshrich/schooner/internal/source"
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

func (a directAdapter) cloneRepository(ctx context.Context, request repository.CloneRequest) (repository.MutationResult, error) {
	remote := hostruntime.NewCloneRequest(request.Source, request.Branch, a.state.worktreeRoot, a.state.boxIdentity)
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
