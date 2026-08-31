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
	"github.com/thewelshrich/schooner/internal/source"
	"github.com/thewelshrich/schooner/internal/workspacetransfer"
)

type executionAdapter interface {
	listWorktrees(context.Context) (repository.Catalog, error)
	listContextWorktrees(context.Context) (repository.Catalog, error)
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
	inspectSourceIdentity(context.Context, string) (source.HostIdentity, error)
	ensureSourceIdentity(context.Context, source.EnsureIdentityRequest) (source.HostIdentity, error)
	removeSourceIdentity(context.Context, source.RemoveIdentityRequest) (source.RemoveIdentityResult, error)
	verifySourceRepository(context.Context, source.VerifyRequest) (source.VerifyResult, error)
	observePushDestination(context.Context, string) (*repository.CheckoutState, error)
	preflightPushDestination(context.Context, string, repository.CheckoutState, bool) (workspacetransfer.PreflightResult, error)
	applyPush(context.Context, workspacetransfer.ApplyRequest, io.Reader) (workspacetransfer.ApplyResult, error)
	inspectPullSource(context.Context, workspacetransfer.PullInspectRequest) (workspacetransfer.PullInspection, error)
	capturePullSource(context.Context, workspacetransfer.PullCaptureRequest) (workspacetransfer.PullCapture, error)
}

type targetState struct {
	boxID        string
	boxName      string
	boxIdentity  string
	worktreeRoot string
	direct       bool
	run          executionAdapter
}

func (t Target) BoxID() string {
	if t.state == nil {
		return ""
	}
	return t.state.boxID
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

func (t Target) BoxIdentity() string {
	if t.state == nil {
		return ""
	}
	return t.state.boxIdentity
}

func (t Target) WorktreeRoot() string {
	if t.state == nil {
		return ""
	}
	return t.state.worktreeRoot
}

func (t Target) InspectSourceIdentity(ctx context.Context, provider string) (source.HostIdentity, error) {
	if err := t.valid(); err != nil {
		return source.HostIdentity{}, err
	}
	result, err := t.state.run.inspectSourceIdentity(ctx, provider)
	return result, normalizeSourceError(err)
}

func (t Target) EnsureSourceIdentity(ctx context.Context, request source.EnsureIdentityRequest) (source.HostIdentity, error) {
	if err := t.valid(); err != nil {
		return source.HostIdentity{}, err
	}
	result, err := t.state.run.ensureSourceIdentity(ctx, request)
	return result, normalizeSourceError(err)
}

func (t Target) RemoveSourceIdentity(ctx context.Context, request source.RemoveIdentityRequest) (source.RemoveIdentityResult, error) {
	if err := t.valid(); err != nil {
		return source.RemoveIdentityResult{}, err
	}
	result, err := t.state.run.removeSourceIdentity(ctx, request)
	return result, normalizeSourceError(err)
}

func (t Target) VerifySourceRepository(ctx context.Context, request source.VerifyRequest) (source.VerifyResult, error) {
	if err := t.valid(); err != nil {
		return source.VerifyResult{}, err
	}
	result, err := t.state.run.verifySourceRepository(ctx, request)
	return result, normalizeSourceError(err)
}

func normalizeSourceError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var sourceDomain *source.Error
	if errors.As(err, &sourceDomain) {
		return err
	}
	var boxDomain *box.Error
	if errors.As(err, &boxDomain) {
		code := boxDomain.Code
		switch code {
		case "not_found", "invalid_input", "conflict", "authentication_required", "permission_denied", "operation_in_progress", "outcome_unknown":
			return &source.Error{Code: code, Message: boxDomain.Message, Context: boxDomain.Context, Cause: err}
		case "host_runtime_missing", "host_runtime_incompatible", "host_runtime_install_failed", "capability_unavailable", "unsupported":
			return source.NewError("unsupported", "the Box runtime does not support this source operation", err)
		}
	}
	if code := hostruntime.ErrorCode(err); code != "" {
		switch code {
		case hostruntime.CodeNotFound, hostruntime.CodeInvalidInput, hostruntime.CodeConflict, hostruntime.CodeAuthentication, hostruntime.CodePermissionDenied, hostruntime.CodeOperationInProgress, hostruntime.CodeOutcomeUnknown:
			return source.NewError(string(code), err.Error(), err)
		case hostruntime.CodeInvalidIdentity:
			return source.NewError("conflict", err.Error(), err)
		case hostruntime.CodeUnsupportedProtocol, hostruntime.CodeCapabilityUnavailable, hostruntime.CodeInvalidMessage:
			return source.NewError("unsupported", "the Box runtime does not support this source operation", err)
		}
	}
	return source.NewError("outcome_unknown", "the Box source operation could not be completed", err)
}

func (t Target) ListWorktrees(ctx context.Context) (repository.Catalog, error) {
	return t.listWorktrees(ctx, false)
}

// ListContextWorktrees requires repository origins with the identity semantics
// used by contextual start and resume.
func (t Target) ListContextWorktrees(ctx context.Context) (repository.Catalog, error) {
	return t.listWorktrees(ctx, true)
}

func (t Target) listWorktrees(ctx context.Context, contextual bool) (repository.Catalog, error) {
	if err := t.valid(); err != nil {
		return repository.Catalog{}, err
	}
	var result repository.Catalog
	var err error
	if contextual {
		result, err = t.state.run.listContextWorktrees(ctx)
	} else {
		result, err = t.state.run.listWorktrees(ctx)
	}
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

func (t Target) ObservePushDestination(ctx context.Context, worktree string) (*repository.CheckoutState, error) {
	if err := t.valid(); err != nil {
		return nil, err
	}
	result, err := t.state.run.observePushDestination(ctx, worktree)
	return result, normalizeError(err)
}

func (t Target) PreflightPushDestination(ctx context.Context, worktree string, source repository.CheckoutState, branch bool) (workspacetransfer.PreflightResult, error) {
	if err := t.valid(); err != nil {
		return workspacetransfer.PreflightResult{}, err
	}
	result, err := t.state.run.preflightPushDestination(ctx, worktree, source, branch)
	return result, normalizeError(err)
}

func (t Target) ApplyPush(ctx context.Context, request workspacetransfer.ApplyRequest, payload io.Reader) (workspacetransfer.ApplyResult, error) {
	if err := t.valid(); err != nil {
		return workspacetransfer.ApplyResult{}, err
	}
	result, err := t.state.run.applyPush(ctx, request, payload)
	return result, normalizeError(err)
}

func (t Target) InspectPullSource(ctx context.Context, request workspacetransfer.PullInspectRequest) (workspacetransfer.PullInspection, error) {
	if err := t.valid(); err != nil {
		return workspacetransfer.PullInspection{}, err
	}
	result, err := t.state.run.inspectPullSource(ctx, request)
	return result, normalizeError(err)
}

func (t Target) CapturePullSource(ctx context.Context, request workspacetransfer.PullCaptureRequest) (workspacetransfer.PullCapture, error) {
	if err := t.valid(); err != nil {
		return workspacetransfer.PullCapture{}, err
	}
	result, err := t.state.run.capturePullSource(ctx, request)
	return result, normalizeError(err)
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
		var domain *repository.Error
		_ = errors.As(err, &domain)
		switch code {
		case repository.CodeNotFound, repository.CodeInvalidInput, repository.CodeConflict, repository.CodeAuthentication, repository.CodePermissionDenied, repository.CodeOperationInProgress, repository.CodeOutcomeUnknown, repository.CodeUnsupported:
			return &box.Error{Code: string(code), Message: err.Error(), Context: domain.Context, Cause: err}
		}
	}
	if code := source.ErrorCode(err); code != "" {
		var domain *source.Error
		_ = errors.As(err, &domain)
		switch code {
		case "not_found", "invalid_input", "conflict", "authentication_required", "permission_denied", "operation_in_progress", "outcome_unknown":
			return &box.Error{Code: code, Message: err.Error(), Context: domain.Context, Cause: err}
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
