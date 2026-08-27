package boxtarget

import (
	"context"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/repository"
	sshruntime "github.com/thewelshrich/schooner/internal/runtime/ssh"
	"github.com/thewelshrich/schooner/internal/session"
	"github.com/thewelshrich/schooner/internal/source"
)

type sshAdapter struct {
	runtime              *sshruntime.Runtime
	state                *targetState
	connection           box.Connection
	installed            box.HostRuntime
	interactiveBatchMode bool
}

func (a sshAdapter) listWorktrees(ctx context.Context) (repository.Catalog, error) {
	return a.runtime.ListWorktrees(ctx, a.connection, a.installed, a.state.boxIdentity)
}

func (a sshAdapter) listContextWorktrees(ctx context.Context) (repository.Catalog, error) {
	return a.runtime.ListContextWorktrees(ctx, a.connection, a.installed, a.state.boxIdentity)
}

func (a sshAdapter) inspectWorktree(ctx context.Context, selector string) (repository.Inspection, error) {
	return a.runtime.InspectWorktree(ctx, a.connection, a.installed, a.state.boxIdentity, selector)
}

func (a sshAdapter) cloneRepository(ctx context.Context, request repository.CloneRequest) (repository.MutationResult, error) {
	return a.runtime.CloneRepository(ctx, a.connection, a.installed, a.state.boxIdentity, a.state.worktreeRoot, request.Source, request.Branch)
}

func (a sshAdapter) addWorktree(ctx context.Context, request repository.AddRequest) (repository.MutationResult, error) {
	return a.runtime.AddWorktree(ctx, a.connection, a.installed, a.state.boxIdentity, a.state.worktreeRoot, request.RepositoryPath, request.Path, request.Branch)
}

func (a sshAdapter) removeWorktree(ctx context.Context, path string) (repository.MutationResult, error) {
	return a.runtime.RemoveWorktree(ctx, a.connection, a.installed, a.state.boxIdentity, a.state.worktreeRoot, path)
}

func (a sshAdapter) pruneWorktrees(ctx context.Context) (repository.MutationResult, error) {
	return a.runtime.PruneWorktrees(ctx, a.connection, a.installed, a.state.boxIdentity, a.state.worktreeRoot)
}

func (a sshAdapter) listSessions(ctx context.Context) (session.Catalog, error) {
	return a.runtime.ListSessions(ctx, a.connection, a.installed, a.state.boxIdentity, a.state.worktreeRoot)
}

func (a sshAdapter) startSession(ctx context.Context, worktree string) (session.StartResult, error) {
	return a.runtime.StartSession(ctx, a.connection, a.installed, a.state.boxIdentity, a.state.worktreeRoot, worktree)
}

func (a sshAdapter) resumeSession(ctx context.Context, selector string, terminal Terminal) (HandoffResult, error) {
	connection := a.connection
	connection.BatchMode = a.interactiveBatchMode
	result, err := a.runtime.ResumeSession(ctx, connection, a.installed, a.state.boxIdentity, a.state.worktreeRoot, selector, sshruntime.TerminalIO{In: terminal.In, Out: terminal.Out, Err: terminal.Err})
	return HandoffResult{ExitCode: result.ExitCode, DiagnosticsReported: result.DiagnosticsReported}, err
}

func (a sshAdapter) sessionLogs(ctx context.Context, id string, lines int) (session.LogsResult, error) {
	return a.runtime.SessionLogs(ctx, a.connection, a.installed, a.state.boxIdentity, a.state.worktreeRoot, id, lines)
}

func (a sshAdapter) stopSession(ctx context.Context, id string) (session.StopResult, error) {
	return a.runtime.StopSession(ctx, a.connection, a.installed, a.state.boxIdentity, a.state.worktreeRoot, id)
}

func (a sshAdapter) openWorktreeShell(ctx context.Context, worktree string, terminal Terminal) (HandoffResult, error) {
	connection := a.connection
	connection.BatchMode = a.interactiveBatchMode
	result, err := a.runtime.OpenWorktreeShell(ctx, connection, a.installed, a.state.boxIdentity, a.state.worktreeRoot, worktree, sshruntime.TerminalIO{In: terminal.In, Out: terminal.Out, Err: terminal.Err})
	return HandoffResult{ExitCode: result.ExitCode, DiagnosticsReported: result.DiagnosticsReported}, err
}

func (a sshAdapter) inspectSourceIdentity(ctx context.Context, provider string) (source.HostIdentity, error) {
	return a.runtime.InspectSourceIdentity(ctx, a.connection, a.installed, a.state.boxIdentity, provider)
}

func (a sshAdapter) ensureSourceIdentity(ctx context.Context, request source.EnsureIdentityRequest) (source.HostIdentity, error) {
	return a.runtime.EnsureSourceIdentity(ctx, a.connection, a.installed, a.state.boxIdentity, request)
}

func (a sshAdapter) removeSourceIdentity(ctx context.Context, request source.RemoveIdentityRequest) (source.RemoveIdentityResult, error) {
	return a.runtime.RemoveSourceIdentity(ctx, a.connection, a.installed, a.state.boxIdentity, request)
}

func (a sshAdapter) verifySourceRepository(ctx context.Context, request source.VerifyRequest) (source.VerifyResult, error) {
	return a.runtime.VerifySourceRepository(ctx, a.connection, a.installed, a.state.boxIdentity, request)
}
