package boxtarget

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/session"
)

type executionAdapter interface {
	listWorktrees(context.Context) (repository.Catalog, error)
	inspectWorktree(context.Context, string) (repository.Inspection, error)
	cloneRepository(context.Context, repository.CloneRequest) (repository.MutationResult, error)
	addWorktree(context.Context, repository.AddRequest) (repository.MutationResult, error)
	removeWorktree(context.Context, string) (repository.MutationResult, error)
	pruneWorktrees(context.Context) (repository.MutationResult, error)
	listSessions(context.Context) (session.Catalog, error)
	startSession(context.Context, string) (session.StartResult, error)
	resumeSession(context.Context, string, Terminal) (HandoffResult, error)
	sessionLogs(context.Context, string, int) (session.LogsResult, error)
	stopSession(context.Context, string) (session.StopResult, error)
	openWorktreeShell(context.Context, string, Terminal) (HandoffResult, error)
}

type targetState struct {
	boxName      string
	boxIdentity  string
	worktreeRoot string
	direct       bool
	run          executionAdapter
}

// Target is an immutable Box execution target with one adapter already bound.
// Its zero value is invalid.
type Target struct{ state *targetState }

// Terminal is supplied only for interactive Session and Worktree handoff.
type Terminal struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// HandoffResult preserves the native attached-process exit status.
type HandoffResult struct {
	ExitCode            int
	DiagnosticsReported bool
}

// BoxName returns the inventory name bound to this target. It is empty for a
// direct target that is not associated with an inventory record.
func (t Target) BoxName() string {
	if t.state == nil {
		return ""
	}
	return t.state.boxName
}

func (t Target) ListWorktrees(ctx context.Context) (repository.Catalog, error) {
	if err := t.valid(); err != nil {
		return repository.Catalog{}, err
	}
	result, err := t.state.run.listWorktrees(ctx)
	if err != nil {
		return repository.Catalog{}, normalizeError(err)
	}
	if err = t.state.validateRoot(result.WorktreeRoot); err != nil {
		return repository.Catalog{}, err
	}
	return result, nil
}

func (t Target) InspectWorktree(ctx context.Context, selector string) (repository.Inspection, error) {
	if err := t.valid(); err != nil {
		return repository.Inspection{}, err
	}
	result, err := t.state.run.inspectWorktree(ctx, selector)
	if err != nil {
		return repository.Inspection{}, normalizeError(err)
	}
	if err = t.state.validateRoot(result.WorktreeRoot); err != nil {
		return repository.Inspection{}, err
	}
	return result, nil
}

func (t Target) CloneRepository(ctx context.Context, request repository.CloneRequest) (repository.MutationResult, error) {
	return t.mutation(ctx, func() (repository.MutationResult, error) { return t.state.run.cloneRepository(ctx, request) })
}

func (t Target) AddWorktree(ctx context.Context, request repository.AddRequest) (repository.MutationResult, error) {
	return t.mutation(ctx, func() (repository.MutationResult, error) { return t.state.run.addWorktree(ctx, request) })
}

func (t Target) RemoveWorktree(ctx context.Context, path string) (repository.MutationResult, error) {
	return t.mutation(ctx, func() (repository.MutationResult, error) { return t.state.run.removeWorktree(ctx, path) })
}

func (t Target) PruneWorktrees(ctx context.Context) (repository.MutationResult, error) {
	return t.mutation(ctx, func() (repository.MutationResult, error) { return t.state.run.pruneWorktrees(ctx) })
}

func (t Target) mutation(ctx context.Context, invoke func() (repository.MutationResult, error)) (repository.MutationResult, error) {
	if err := t.valid(); err != nil {
		return repository.MutationResult{}, err
	}
	result, err := invoke()
	if err != nil {
		return repository.MutationResult{}, normalizeError(err)
	}
	if err = t.state.validateRoot(result.WorktreeRoot); err != nil {
		return repository.MutationResult{}, err
	}
	return result, nil
}

func (t Target) ListSessions(ctx context.Context) (session.Catalog, error) {
	if err := t.valid(); err != nil {
		return session.Catalog{}, err
	}
	result, err := t.state.run.listSessions(ctx)
	if err != nil {
		return session.Catalog{}, normalizeError(err)
	}
	if err = t.state.validateRoot(result.WorktreeRoot); err != nil {
		return session.Catalog{}, err
	}
	return result, nil
}

func (t Target) StartSession(ctx context.Context, worktree string) (session.StartResult, error) {
	if err := t.valid(); err != nil {
		return session.StartResult{}, err
	}
	result, err := t.state.run.startSession(ctx, worktree)
	return result, normalizeError(err)
}

func (t Target) ResumeSession(ctx context.Context, selector string, terminal Terminal) (HandoffResult, error) {
	if err := t.valid(); err != nil {
		return HandoffResult{}, err
	}
	result, err := t.state.run.resumeSession(ctx, selector, terminal)
	return result, normalizeError(err)
}

func (t Target) SessionLogs(ctx context.Context, id string, lines int) (session.LogsResult, error) {
	if err := t.valid(); err != nil {
		return session.LogsResult{}, err
	}
	result, err := t.state.run.sessionLogs(ctx, id, lines)
	return result, normalizeError(err)
}

func (t Target) StopSession(ctx context.Context, id string) (session.StopResult, error) {
	if err := t.valid(); err != nil {
		return session.StopResult{}, err
	}
	result, err := t.state.run.stopSession(ctx, id)
	return result, normalizeError(err)
}

func (t Target) OpenWorktreeShell(ctx context.Context, worktree string, terminal Terminal) (HandoffResult, error) {
	if err := t.valid(); err != nil {
		return HandoffResult{}, err
	}
	result, err := t.state.run.openWorktreeShell(ctx, worktree, terminal)
	return result, normalizeError(err)
}

func (t Target) valid() error {
	if t.state == nil || t.state.run == nil {
		return box.NewError("internal", "Box execution target is invalid", nil)
	}
	return nil
}

func (s *targetState) validateRoot(actual string) error {
	if actual == s.worktreeRoot {
		return nil
	}
	if s.direct {
		if s.boxName != "" {
			return box.NewError("conflict", fmt.Sprintf("direct Box worktree root differs from local inventory; run \"schooner box setup %s\" from a workstation", s.boxName), nil)
		}
		return box.NewError("conflict", "direct Box worktree root differs from host configuration; run box setup from a workstation", nil)
	}
	return box.NewError("conflict", fmt.Sprintf("remote Box worktree root differs from local inventory; run \"schooner box setup %s\"", s.boxName), nil)
}

func normalizeError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var public *box.Error
	if errors.As(err, &public) {
		return err
	}
	if code := repository.ErrorCode(err); code != "" {
		switch code {
		case repository.CodeNotFound, repository.CodeInvalidInput, repository.CodeConflict, repository.CodeAuthentication, repository.CodePermissionDenied, repository.CodeOperationInProgress, repository.CodeOutcomeUnknown:
			return box.NewError(string(code), err.Error(), err)
		}
	}
	if code := hostruntime.ErrorCode(err); code != "" {
		switch code {
		case hostruntime.CodeNotFound, hostruntime.CodeInvalidInput, hostruntime.CodeConflict, hostruntime.CodeAuthentication, hostruntime.CodePermissionDenied, hostruntime.CodeOperationInProgress, hostruntime.CodeOutcomeUnknown:
			return box.NewError(string(code), err.Error(), err)
		case hostruntime.CodeInvalidIdentity:
			return box.NewError("conflict", err.Error(), err)
		case hostruntime.CodeUnsupportedProtocol, hostruntime.CodeCapabilityUnavailable, hostruntime.CodeInvalidMessage:
			return box.NewError("host_runtime_incompatible", err.Error(), err)
		}
	}
	return box.NewError("internal", err.Error(), err)
}
