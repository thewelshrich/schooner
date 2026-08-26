package boxtarget

import (
	"context"

	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
	"github.com/thewelshrich/schooner/internal/session"
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

func (a directAdapter) inspectWorktree(ctx context.Context, selector string) (repository.Inspection, error) {
	result, err := a.runtime.InspectWorktree(ctx, hostruntime.NewWorktreeRequest(selector, a.state.boxIdentity))
	return result.Inspection, err
}

func (a directAdapter) cloneRepository(ctx context.Context, request repository.CloneRequest) (repository.MutationResult, error) {
	remote := hostruntime.NewCloneRequest(request.Source, request.Branch, a.state.worktreeRoot, a.state.boxIdentity)
	remote.NonInteractive = a.nonInteractive
	result, err := a.runtime.CloneRepository(ctx, remote)
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
