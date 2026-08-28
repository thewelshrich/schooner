package ssh

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thewelshrich/schooner/internal/artifact"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/semver"
	"github.com/thewelshrich/schooner/internal/session"
	"github.com/thewelshrich/schooner/internal/source"
)

const (
	maxArtifactBytes = 256 << 20

	// OpenSSH forwards the local TERM with an allocated PTY. Keep it when the
	// Box understands it, otherwise use the widely available 256-color entry.
	interactiveTerminalFallback = `if command -v infocmp >/dev/null 2>&1 && ! infocmp "${TERM-}" >/dev/null 2>&1 && infocmp xterm-256color >/dev/null 2>&1; then TERM=xterm-256color; export TERM; fi`
)

var (
	digestPattern           = regexp.MustCompile(`^[0-9a-f]{64}$`)
	suffixPattern           = regexp.MustCompile(`^[0-9a-f]{24}$`)
	errInstallTargetChanged = errors.New("host runtime changed during installation")
)

type ArtifactResolver interface {
	Resolve(context.Context, string, artifact.Platform) (artifact.Result, error)
}

func (r *Runtime) EnsureHost(ctx context.Context, connection box.Connection, request box.HostInstallRequest) (box.HostInstallResult, error) {
	if request.Mode == "" {
		request.Mode = box.HostRepair
	}
	if err := validateHostInstallRequest(request); err != nil {
		return box.HostInstallResult{}, err
	}
	targetVersion := r.Version
	if targetVersion == "" {
		targetVersion = "dev"
	}
	var resolved *artifact.Result
	for attempt := 0; attempt < 3; attempt++ {
		installed, remoteVersion, err := r.assessHost(ctx, connection, request)
		if err != nil {
			return box.HostInstallResult{}, err
		}
		if installed != nil {
			result, replace, decisionErr := compatibleHostDecision(targetVersion, request.Mode, *installed)
			if decisionErr != nil {
				return box.HostInstallResult{}, decisionErr
			}
			if !replace {
				return result, nil
			}
			remoteVersion = installed.Version
		} else if request.Mode == box.HostUpdate && remoteVersion == "" {
			return box.HostInstallResult{}, box.NewError("host_runtime_missing", "the host runtime is missing; run box setup before updating", nil)
		}
		if err = replacementAllowed(r.Version, remoteVersion); err != nil {
			return box.HostInstallResult{}, err
		}

		baseline, err := r.fingerprintAt(ctx, connection, request.Path)
		if err != nil {
			return box.HostInstallResult{}, err
		}
		// Reassess after taking the compare-and-swap baseline. Promotion also
		// verifies this fingerprint while holding the remote install lock.
		installed, remoteVersion, err = r.assessHost(ctx, connection, request)
		if err != nil {
			return box.HostInstallResult{}, err
		}
		if installed != nil {
			result, replace, decisionErr := compatibleHostDecision(targetVersion, request.Mode, *installed)
			if decisionErr != nil {
				return box.HostInstallResult{}, decisionErr
			}
			if !replace {
				return result, nil
			}
			remoteVersion = installed.Version
		} else if request.Mode == box.HostUpdate && remoteVersion == "" {
			return box.HostInstallResult{}, box.NewError("host_runtime_missing", "the host runtime is missing; run box setup before updating", nil)
		}
		if err = replacementAllowed(r.Version, remoteVersion); err != nil {
			return box.HostInstallResult{}, err
		}
		if r.Artifacts == nil {
			return box.HostInstallResult{}, box.NewError("artifact_unavailable", "host runtime artifacts are not configured", nil)
		}
		if resolved == nil {
			result, resolveErr := r.Artifacts.Resolve(ctx, r.Version, artifact.Platform{OS: request.OS, Arch: request.Architecture})
			if resolveErr != nil {
				return box.HostInstallResult{}, artifactError(resolveErr)
			}
			resolved = &result
			defer resolved.Release()
		}
		installedRuntime, installErr := r.installHost(ctx, connection, request, *resolved, baseline)
		if errors.Is(installErr, errInstallTargetChanged) {
			continue
		}
		if installErr != nil {
			return box.HostInstallResult{}, installErr
		}
		action := box.HostReplaced
		if baseline == "missing" {
			action = box.HostInstalled
		}
		return box.HostInstallResult{Runtime: installedRuntime, PreviousVersion: remoteVersion, TargetVersion: targetVersion, Action: action}, nil
	}
	return box.HostInstallResult{}, box.NewError("host_runtime_incompatible", "the host runtime changed repeatedly during installation; retry after the other installation completes", errInstallTargetChanged)
}

func compatibleHostDecision(targetVersion string, mode box.HostInstallMode, installed box.HostRuntime) (box.HostInstallResult, bool, error) {
	result := box.HostInstallResult{Runtime: installed, PreviousVersion: installed.Version, TargetVersion: targetVersion, Action: box.HostReused}
	if mode == box.HostRepair {
		if targetVersion == "dev" && installed.Version == "dev" {
			// Development builds have no stable build identity. Setup and adoption
			// promise a matching runtime, so promote the currently verified local
			// artifact instead of silently reusing an arbitrary older dev binary.
			return result, true, nil
		}
		return result, false, nil
	}
	if targetVersion == "dev" || installed.Version == "dev" {
		if targetVersion == installed.Version {
			// Development builds have no ordered version or stable build identity.
			// An explicit update must promote the currently verified artifact so a
			// rebuilt local binary is not mistaken for the installed executable.
			return result, true, nil
		}
		return box.HostInstallResult{}, false, box.NewError("host_runtime_incompatible", fmt.Sprintf("host runtime version %q cannot be safely compared with local version %q", installed.Version, targetVersion), nil)
	}
	comparison, ok := semver.Compare(installed.Version, targetVersion)
	if !ok {
		return box.HostInstallResult{}, false, box.NewError("host_runtime_incompatible", fmt.Sprintf("host runtime version %q cannot be compared with local version %q", installed.Version, targetVersion), nil)
	}
	if comparison > 0 {
		result.Action = box.HostNewerRetained
		return result, false, nil
	}
	if comparison == 0 {
		return result, installed.Version != targetVersion, nil
	}
	return result, true, nil
}

func (r *Runtime) assessHost(ctx context.Context, connection box.Connection, request box.HostInstallRequest) (*box.HostRuntime, string, error) {
	hello, attempt, decodeErr := r.helloAt(ctx, connection, request.Path)
	if decodeErr == nil && attempt.ExitCode == 0 {
		if err := validateReusableHello(hello, request.ExpectedIdentity); err == nil {
			if hello.OS != request.OS || hello.Architecture != request.Architecture {
				return nil, "", box.NewError("unsupported", fmt.Sprintf("host runtime reported %s/%s instead of %s/%s", hello.OS, hello.Architecture, request.OS, request.Architecture), nil)
			}
			installed := hostRuntime(request.Path, hello)
			return &installed, "", nil
		} else if hostruntime.ErrorCode(err) == hostruntime.CodeInvalidIdentity {
			return nil, "", protocolError(err)
		}
	}
	if decodeErr != nil && !isProtocolError(decodeErr) {
		return nil, "", decodeErr
	}

	remoteVersion := ""
	if attempt.ExitCode == 0 && decodeErr == nil && hello.SchoonerVersion != "" {
		remoteVersion = hello.SchoonerVersion
	} else {
		if attempt.ExitCode == 126 || attempt.ExitCode == 127 {
			fingerprint, err := r.fingerprintAt(ctx, connection, request.Path)
			if err != nil {
				return nil, "", err
			}
			if fingerprint == "missing" {
				return nil, "", nil
			}
		}
		var found bool
		var err error
		remoteVersion, found, err = r.versionAt(ctx, connection, request.Path)
		if err != nil {
			return nil, "", err
		}
		if !found {
			if request.Mode == box.HostRepair {
				return nil, "", nil
			}
			return nil, "", box.NewError("host_runtime_incompatible", "the existing host runtime could not be identified; inspect or remove it before retrying", decodeErr)
		}
	}
	return nil, remoteVersion, nil
}

func (r *Runtime) InspectHost(ctx context.Context, connection box.Connection, installed box.HostRuntime, worktreeRoot, expectedIdentity string) (box.Capabilities, error) {
	if err := validateRuntimePath(installed.Path); err != nil {
		return box.Capabilities{}, err
	}
	hello, attempt, err := r.helloAt(ctx, connection, installed.Path)
	if err != nil {
		if isProtocolError(err) {
			return box.Capabilities{}, protocolError(err)
		}
		return box.Capabilities{}, err
	}
	if attempt.ExitCode != 0 {
		return box.Capabilities{}, box.NewError("host_runtime_missing", "the recorded host runtime is unavailable and must be repaired", fmt.Errorf("remote exit status %d", attempt.ExitCode))
	}
	if err = validateInstalledHello(hello, expectedIdentity); err != nil {
		return box.Capabilities{}, protocolError(err)
	}
	if err = hostruntime.ValidateHello(hello, expectedIdentity, hostruntime.CapabilityInspectV2); err != nil {
		if hostruntime.ErrorCode(err) == hostruntime.CodeCapabilityUnavailable {
			return box.Capabilities{}, box.NewError("host_runtime_incompatible", "the host runtime lacks required inspection support; run box update", err)
		}
		return box.Capabilities{}, protocolError(err)
	}

	request := hostruntime.NewInspectRequest(worktreeRoot)
	payload, err := json.Marshal(request)
	if err != nil {
		return box.Capabilities{}, box.NewError("internal", "encode host inspection request", err)
	}
	command := fixedShellCommand(`runtime_path=$(printf %s "$1" | base64 -d) || exit 64; exec "$runtime_path" host inspect`, installed.Path)
	result, err := r.runRemote(ctx, connection, command, strings.NewReader(string(payload)))
	if err != nil {
		return box.Capabilities{}, err
	}
	if result.ExitCode != 0 {
		return box.Capabilities{}, remoteFailure("host inspection", result)
	}
	var inspection hostruntime.Inspection
	if err = hostruntime.DecodeStrict(result.Stdout, &inspection); err != nil {
		return box.Capabilities{}, protocolError(err)
	}
	if err = hostruntime.ValidateInspection(inspection, expectedIdentity); err != nil {
		return box.Capabilities{}, protocolError(err)
	}
	if hello.OS != "linux" || hello.Architecture != inspection.Architecture {
		return box.Capabilities{}, box.NewError("host_runtime_incompatible", "host runtime handshake and inspection reported different platforms", nil)
	}
	return capabilities(inspection, installed.Path, hello), nil
}

func (r *Runtime) ConfigureHost(ctx context.Context, connection box.Connection, installed box.HostRuntime, worktreeRoot, expectedIdentity string) error {
	request := hostruntime.NewConfigureRequest(worktreeRoot, expectedIdentity)
	_, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.ConfigureOperation(), request)
	return err
}

func (r *Runtime) ListWorktrees(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity string) (repository.Catalog, error) {
	return r.listWorktrees(ctx, connection, installed, expectedIdentity, hostruntime.WorktreeListOperation())
}

func (r *Runtime) ListContextWorktrees(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity string) (repository.Catalog, error) {
	return r.listWorktrees(ctx, connection, installed, expectedIdentity, hostruntime.ContextWorktreeListOperation())
}

func (r *Runtime) listWorktrees(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity string, operation hostruntime.Operation[hostruntime.WorktreeRequest, hostruntime.WorktreeCatalog]) (repository.Catalog, error) {
	request := hostruntime.NewWorktreeRequest("", expectedIdentity)
	result, err := invokeHostOperation(ctx, r, connection, installed, operation, request)
	if err != nil {
		return repository.Catalog{}, err
	}
	return result.Catalog, nil
}

func (r *Runtime) InspectWorktree(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, selector string) (repository.Inspection, error) {
	request := hostruntime.NewWorktreeRequest(selector, expectedIdentity)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.WorktreeInspectOperation(), request)
	if err != nil {
		return repository.Inspection{}, err
	}
	return result.Inspection, nil
}

func (r *Runtime) CloneRepository(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, worktreeRoot, repositorySource, branch string) (repository.MutationResult, error) {
	request := hostruntime.NewCloneRequest(repositorySource, branch, worktreeRoot, expectedIdentity)
	hello, err := operationHello(ctx, r, connection, installed)
	if err != nil {
		return repository.MutationResult{}, err
	}
	operation := hostruntime.RepositoryCloneOperation()
	cloneV2 := slices.Contains(hello.Capabilities, hostruntime.CapabilityRepositoryCloneV2)
	if cloneV2 {
		operation = hostruntime.RepositoryCloneV2Operation()
	}
	result, err := invokeHostOperationWithHello(ctx, r, connection, installed, hello, operation, request)
	if err != nil {
		if !cloneV2 && legacyGitHubCloneAuthentication(repositorySource, err) {
			return repository.MutationResult{}, &box.Error{
				Code: "authentication_required", Message: "managed GitHub source recovery requires an updated Box runtime",
				Context: map[string]string{"reason": "host_runtime_update_required"}, Cause: err,
			}
		}
		return repository.MutationResult{}, err
	}
	return result.MutationResult, nil
}

func legacyGitHubCloneAuthentication(repositorySource string, err error) bool {
	var domain *box.Error
	if !errors.As(err, &domain) || domain.Code != "authentication_required" || domain.Cause != nil {
		return false
	}
	identity, network, identityErr := source.RepositoryIdentityFor(repositorySource)
	return identityErr == nil && network && identity.IsGitHub()
}

func (r *Runtime) InspectSourceIdentity(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, provider string) (source.HostIdentity, error) {
	request := hostruntime.NewSourceIdentityRequest(provider, expectedIdentity)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.SourceIdentityInspectOperation(), request)
	return result.HostIdentity, err
}

func (r *Runtime) EnsureSourceIdentity(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity string, request source.EnsureIdentityRequest) (source.HostIdentity, error) {
	remote := hostruntime.NewSourceIdentityEnsureRequest(request.Provider, expectedIdentity, request.HostKeys)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.SourceIdentityEnsureOperation(), remote)
	return result.HostIdentity, err
}

func (r *Runtime) RemoveSourceIdentity(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity string, request source.RemoveIdentityRequest) (source.RemoveIdentityResult, error) {
	remote := hostruntime.NewSourceIdentityRemoveRequest(request.Provider, expectedIdentity, request.ExpectedFingerprint)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.SourceIdentityRemoveOperation(), remote)
	return result.RemoveIdentityResult, err
}

func (r *Runtime) VerifySourceRepository(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity string, request source.VerifyRequest) (source.VerifyResult, error) {
	remote := hostruntime.NewSourceRepositoryVerifyRequest(request.Provider, request.Repository, expectedIdentity)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.SourceRepositoryVerifyOperation(), remote)
	return result.VerifyResult, err
}

func (r *Runtime) AddWorktree(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, worktreeRoot, repositoryPath, pathValue, branch string) (repository.MutationResult, error) {
	request := hostruntime.NewWorktreeMutationRequest(repositoryPath, pathValue, branch, worktreeRoot, expectedIdentity)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.WorktreeAddOperation(), request)
	if err != nil {
		return repository.MutationResult{}, err
	}
	return result.MutationResult, nil
}

func (r *Runtime) RemoveWorktree(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, worktreeRoot, pathValue string) (repository.MutationResult, error) {
	request := hostruntime.NewWorktreeMutationRequest("", pathValue, "", worktreeRoot, expectedIdentity)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.WorktreeRemoveOperation(), request)
	if err != nil {
		return repository.MutationResult{}, err
	}
	return result.MutationResult, nil
}

func (r *Runtime) PruneWorktrees(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, worktreeRoot string) (repository.MutationResult, error) {
	request := hostruntime.NewWorktreeMutationRequest("", "", "", worktreeRoot, expectedIdentity)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.WorktreePruneOperation(), request)
	if err != nil {
		return repository.MutationResult{}, err
	}
	return result.MutationResult, nil
}

func (r *Runtime) ListSessions(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, worktreeRoot string) (session.Catalog, error) {
	request := hostruntime.NewSessionListRequest(worktreeRoot, expectedIdentity)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.SessionListOperation(), request)
	if err != nil {
		return session.Catalog{}, err
	}
	return result.Catalog, nil
}

func (r *Runtime) StartSession(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, worktreeRoot, worktree string) (session.StartResult, error) {
	request := hostruntime.NewSessionStartRequest(worktreeRoot, expectedIdentity, worktree)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.SessionStartOperation(), request)
	if err != nil {
		return session.StartResult{}, err
	}
	return result.StartResult, nil
}

func (r *Runtime) SessionLogs(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, worktreeRoot, id string, lines int) (session.LogsResult, error) {
	request := hostruntime.NewSessionLogsRequest(worktreeRoot, expectedIdentity, id, lines)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.SessionLogsOperation(), request)
	if err != nil {
		return session.LogsResult{}, err
	}
	return result.LogsResult, nil
}

func (r *Runtime) StopSession(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, worktreeRoot, id string) (session.StopResult, error) {
	request := hostruntime.NewSessionTargetRequest(worktreeRoot, expectedIdentity, id)
	result, err := invokeHostOperation(ctx, r, connection, installed, hostruntime.SessionStopOperation(), request)
	if err != nil {
		return session.StopResult{}, err
	}
	return result.StopResult, nil
}

func (r *Runtime) ResumeSession(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, worktreeRoot, selector string, terminal TerminalIO) (ShellResult, error) {
	request := hostruntime.NewSessionTargetRequest(worktreeRoot, expectedIdentity, selector)
	return r.openHostInteractive(ctx, connection, installed, expectedIdentity, hostruntime.CapabilitySessionResumeV1, "host session resume", request, terminal)
}

func (r *Runtime) OpenWorktreeShell(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, worktreeRoot, worktree string, terminal TerminalIO) (ShellResult, error) {
	request := hostruntime.NewWorktreeShellRequest(worktreeRoot, expectedIdentity, worktree)
	return r.openHostInteractive(ctx, connection, installed, expectedIdentity, hostruntime.CapabilityWorktreeShellV1, "host worktree shell", request, terminal)
}

func invokeHostOperation[Request, Result any](ctx context.Context, r *Runtime, connection box.Connection, installed box.HostRuntime, operation hostruntime.Operation[Request, Result], request Request) (Result, error) {
	var zero Result
	hello, err := operationHello(ctx, r, connection, installed)
	if err != nil {
		return zero, err
	}
	return invokeHostOperationWithHello(ctx, r, connection, installed, hello, operation, request)
}

func operationHello(ctx context.Context, r *Runtime, connection box.Connection, installed box.HostRuntime) (hostruntime.Hello, error) {
	if err := validateRuntimePath(installed.Path); err != nil {
		return hostruntime.Hello{}, err
	}
	hello, attempt, err := r.helloAt(ctx, connection, installed.Path)
	if err != nil {
		if isProtocolError(err) {
			return hostruntime.Hello{}, protocolError(err)
		}
		return hostruntime.Hello{}, err
	}
	if attempt.ExitCode != 0 {
		return hostruntime.Hello{}, box.NewError("host_runtime_missing", "the recorded host runtime is unavailable and must be repaired", fmt.Errorf("remote exit status %d", attempt.ExitCode))
	}
	return hello, nil
}

func invokeHostOperationWithHello[Request, Result any](ctx context.Context, r *Runtime, connection box.Connection, installed box.HostRuntime, hello hostruntime.Hello, operation hostruntime.Operation[Request, Result], request Request) (Result, error) {
	var zero Result
	if err := operation.ValidateHello(request, hello); err != nil {
		if hostruntime.ErrorCode(err) == hostruntime.CodeCapabilityUnavailable {
			return zero, box.NewError("host_runtime_incompatible", "the host runtime lacks required support; run box update", err)
		}
		return zero, protocolError(err)
	}
	payload, err := operation.EncodeRequest(request)
	if err != nil {
		return zero, box.NewError("internal", "encode host operation request", err)
	}
	hostOperation := operation.Command()
	if connection.BatchMode {
		hostOperation = "--no-input " + hostOperation
	}
	command := fixedShellCommand(`runtime_path=$(printf %s "$1" | base64 -d) || exit 64; exec "$runtime_path" `+hostOperation, installed.Path)
	result, err := r.runRemote(ctx, connection, command, strings.NewReader(string(payload)))
	if err != nil {
		return zero, err
	}
	if result.ExitCode != 0 {
		return zero, remoteFailure(operation.Command(), result)
	}
	decoded, operationError, err := operation.DecodeResult(result.Stdout, request)
	if err != nil {
		return zero, protocolError(err)
	}
	if operationError != nil {
		return zero, &box.Error{Code: string(operationError.Error.Code), Message: operationError.Error.Message, Context: operationError.Error.Context}
	}
	return decoded, nil
}

func (r *Runtime) openHostInteractive(ctx context.Context, connection box.Connection, installed box.HostRuntime, expectedIdentity, capability, operation string, request any, terminal TerminalIO) (ShellResult, error) {
	if err := validateRuntimePath(installed.Path); err != nil {
		return ShellResult{}, err
	}
	hello, attempt, err := r.helloAt(ctx, connection, installed.Path)
	if err != nil {
		if isProtocolError(err) {
			return ShellResult{}, protocolError(err)
		}
		return ShellResult{}, err
	}
	if attempt.ExitCode != 0 {
		return ShellResult{}, box.NewError("host_runtime_missing", "the recorded host runtime is unavailable and must be repaired", fmt.Errorf("remote exit status %d", attempt.ExitCode))
	}
	if err = hostruntime.ValidateHello(hello, expectedIdentity, capability); err != nil {
		if hostruntime.ErrorCode(err) == hostruntime.CodeCapabilityUnavailable {
			return ShellResult{}, box.NewError("host_runtime_incompatible", "the host runtime lacks required support; run box update", err)
		}
		return ShellResult{}, protocolError(err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return ShellResult{}, box.NewError("internal", "encode interactive host operation request", err)
	}
	hostOperation := operation
	if connection.BatchMode {
		hostOperation = "--no-input " + hostOperation
	}
	// The host CLI consumes this Base64 protocol value. fixedShellCommand adds
	// a separate transport wrapper, which the remote shell removes first.
	encoded := base64.StdEncoding.EncodeToString(payload)
	commandBody := strings.Join([]string{
		`runtime_path=$(printf %s "$1" | base64 -d) || exit 64`,
		`request=$(printf %s "$2" | base64 -d) || exit 64`,
		interactiveTerminalFallback,
		`exec "$runtime_path" ` + hostOperation + ` "$request"`,
	}, "; ")
	command := fixedShellCommand(commandBody, installed.Path, encoded)
	return r.openInteractiveCommand(ctx, connection, command, terminal)
}

func (r *Runtime) installHost(ctx context.Context, connection box.Connection, request box.HostInstallRequest, result artifact.Result, baseline string) (box.HostRuntime, error) {
	if result.Version != r.Version || result.Platform.OS != request.OS || result.Platform.Arch != request.Architecture || !digestPattern.MatchString(result.SHA256) {
		return box.HostRuntime{}, box.NewError("artifact_unavailable", "artifact resolver returned inconsistent metadata", nil)
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		return box.HostRuntime{}, box.NewError("artifact_unavailable", "inspect the verified host runtime artifact", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxArtifactBytes {
		return box.HostRuntime{}, box.NewError("artifact_unavailable", "verified host runtime artifact is empty, non-regular, or exceeds 256 MiB", nil)
	}
	suffix := r.randomSuffix
	if suffix == nil {
		suffix = secureSuffix
	}
	random, err := suffix()
	if err != nil {
		return box.HostRuntime{}, box.NewError("internal", "create a remote staging path", err)
	}
	if !suffixPattern.MatchString(random) {
		return box.HostRuntime{}, box.NewError("internal", "create a safe remote staging path", nil)
	}
	stagingPath := request.Path + ".stage-" + random
	defer r.cleanupStage(connection, stagingPath)

	file, err := os.Open(result.Path)
	if err != nil {
		return box.HostRuntime{}, box.NewError("artifact_unavailable", "open the verified host runtime artifact", err)
	}
	stageResult, stageErr := r.runRemote(ctx, connection, stageCommand(request.Path, stagingPath, result.SHA256), io.LimitReader(file, maxArtifactBytes+1))
	closeErr := file.Close()
	if stageErr != nil {
		return box.HostRuntime{}, stageErr
	}
	if closeErr != nil {
		return box.HostRuntime{}, box.NewError("artifact_unavailable", "finish reading the verified host runtime artifact", closeErr)
	}
	if stageResult.ExitCode != 0 {
		return box.HostRuntime{}, installFailure("stage", stageResult)
	}

	candidate, attempt, err := r.helloAt(ctx, connection, stagingPath)
	if err != nil {
		return box.HostRuntime{}, helloError(err)
	}
	if attempt.ExitCode != 0 {
		return box.HostRuntime{}, box.NewError("unsupported", fmt.Sprintf("the staged host runtime could not start as %s/%s", request.OS, request.Architecture), fmt.Errorf("remote exit status %d", attempt.ExitCode))
	}
	if err = validateCandidateHello(candidate, request, result); err != nil {
		return box.HostRuntime{}, err
	}

	promoteResult, err := r.runRemote(ctx, connection, promoteCommand(request.Path, stagingPath, result.SHA256, baseline), nil)
	if err != nil {
		return box.HostRuntime{}, err
	}
	if promoteResult.ExitCode == 76 {
		return box.HostRuntime{}, errInstallTargetChanged
	}
	if promoteResult.ExitCode != 0 {
		return box.HostRuntime{}, installFailure("promote", promoteResult)
	}

	installed, attempt, err := r.helloAt(ctx, connection, request.Path)
	if err != nil {
		return box.HostRuntime{}, helloError(err)
	}
	if attempt.ExitCode != 0 {
		return box.HostRuntime{}, box.NewError("host_runtime_install_failed", "the promoted host runtime did not complete its handshake", fmt.Errorf("remote exit status %d", attempt.ExitCode))
	}
	if err = validateCandidateHello(installed, request, result); err != nil {
		return box.HostRuntime{}, err
	}
	return hostRuntime(request.Path, installed), nil
}

func (r *Runtime) helloAt(ctx context.Context, connection box.Connection, runtimePath string) (hostruntime.Hello, remoteResult, error) {
	if err := validateRuntimePath(runtimePath); err != nil {
		return hostruntime.Hello{}, remoteResult{}, err
	}
	result, err := r.runRemote(ctx, connection, fixedShellCommand(`runtime_path=$(printf %s "$1" | base64 -d) || exit 64; exec "$runtime_path" host hello`, runtimePath), nil)
	if err != nil || result.ExitCode != 0 {
		return hostruntime.Hello{}, result, err
	}
	var hello hostruntime.Hello
	if err = hostruntime.DecodeStrict(result.Stdout, &hello); err != nil {
		return hostruntime.Hello{}, result, err
	}
	return hello, result, nil
}

func (r *Runtime) versionAt(ctx context.Context, connection box.Connection, runtimePath string) (string, bool, error) {
	result, err := r.runRemote(ctx, connection, fixedShellCommand(`runtime_path=$(printf %s "$1" | base64 -d) || exit 64; exec "$runtime_path" --output json version`, runtimePath), nil)
	if err != nil {
		return "", false, err
	}
	if result.ExitCode != 0 {
		return "", false, nil
	}
	var document struct {
		SchemaVersion string  `json:"schema_version"`
		Version       string  `json:"version"`
		Commit        string  `json:"commit"`
		BuiltAt       *string `json:"built_at"`
		GoVersion     string  `json:"go_version"`
		OS            string  `json:"os"`
		Arch          string  `json:"arch"`
	}
	if err = hostruntime.DecodeStrict(result.Stdout, &document); err != nil || document.SchemaVersion != "1" || document.Version == "" {
		return "", false, nil
	}
	return document.Version, true, nil
}

func (r *Runtime) fingerprintAt(ctx context.Context, connection box.Connection, runtimePath string) (string, error) {
	body := `runtime_path=$(printf %s "$1" | base64 -d) || exit 64; if [ ! -e "$runtime_path" ]; then printf "%s\n" missing; exit 0; fi; actual=$(sha256sum -- "$runtime_path" 2>/dev/null) || exit 74; printf "%s\n" "${actual%% *}"`
	result, err := r.runRemote(ctx, connection, fixedShellCommand(body, runtimePath), nil)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", remoteFailure("host runtime fingerprint", result)
	}
	fingerprint := strings.TrimSpace(string(result.Stdout))
	if fingerprint != "missing" && !digestPattern.MatchString(fingerprint) {
		return "", box.NewError("host_runtime_incompatible", "the existing host runtime returned an invalid fingerprint", nil)
	}
	return fingerprint, nil
}

func validateHostInstallRequest(request box.HostInstallRequest) error {
	if request.Mode != box.HostRepair && request.Mode != box.HostUpdate {
		return box.NewError("invalid_input", "host runtime maintenance mode is invalid", nil)
	}
	if request.OS != "linux" || (request.Architecture != "amd64" && request.Architecture != "arm64") {
		return box.NewError("unsupported", fmt.Sprintf("host runtime artifacts do not support %s/%s", request.OS, request.Architecture), nil)
	}
	if !identityPattern.MatchString(request.ExpectedIdentity) {
		return box.NewError("conflict", "expected box identity is malformed", nil)
	}
	return validateRuntimePath(request.Path)
}

func validateRuntimePath(runtimePath string) error {
	if !path.IsAbs(runtimePath) || path.Clean(runtimePath) != runtimePath || strings.ContainsAny(runtimePath, "\x00\r\n") {
		return box.NewError("invalid_input", "host runtime path must be a clean absolute path", nil)
	}
	return nil
}

func validateInstalledHello(hello hostruntime.Hello, expectedIdentity string) error {
	return hostruntime.ValidateHello(hello, expectedIdentity, hostruntime.CapabilityHelloV1)
}

func validateReusableHello(hello hostruntime.Hello, expectedIdentity string) error {
	return hostruntime.ValidateHello(hello, expectedIdentity, hostruntime.CapabilityHelloV1, hostruntime.CapabilityConfigureV1, hostruntime.CapabilityInspectV2)
}

func validateCandidateHello(hello hostruntime.Hello, request box.HostInstallRequest, result artifact.Result) error {
	if err := validateInstalledHello(hello, request.ExpectedIdentity); err != nil {
		return protocolError(err)
	}
	if hello.SchoonerVersion != result.Version {
		return box.NewError("host_runtime_install_failed", fmt.Sprintf("staged host runtime reported version %q instead of %q", hello.SchoonerVersion, result.Version), nil)
	}
	if hello.OS != result.Platform.OS || hello.Architecture != result.Platform.Arch {
		return box.NewError("unsupported", fmt.Sprintf("staged host runtime reported %s/%s instead of %s/%s", hello.OS, hello.Architecture, result.Platform.OS, result.Platform.Arch), nil)
	}
	return nil
}

func replacementAllowed(localVersion, remoteVersion string) error {
	if localVersion == "" {
		localVersion = "dev"
	}
	if remoteVersion == "" {
		return nil
	}
	if localVersion == "dev" {
		if remoteVersion == "dev" {
			return nil
		}
		return box.NewError("host_runtime_incompatible", fmt.Sprintf("host runtime %s cannot be safely replaced by an unversioned development build", remoteVersion), nil)
	}
	comparison, ok := semver.Compare(remoteVersion, localVersion)
	if !ok {
		return box.NewError("host_runtime_incompatible", fmt.Sprintf("host runtime version %q cannot be compared with local version %q", remoteVersion, localVersion), nil)
	}
	if comparison > 0 {
		return box.NewError("host_runtime_incompatible", fmt.Sprintf("host runtime %s is newer than local Schooner %s; upgrade the local CLI instead of downgrading the host", remoteVersion, localVersion), nil)
	}
	return nil
}

func stageCommand(targetPath, stagingPath, digest string) string {
	body := `target_path=$(printf %s "$1" | base64 -d) || exit 64; staging_path=$(printf %s "$2" | base64 -d) || exit 64; digest=$(printf %s "$3" | base64 -d) || exit 64; directory=${target_path%/*}; umask 077; mkdir -p -- "$directory" || exit 73; set -C; exec 3>"$staging_path" || exit 73; set +C; cleanup_stage() { rm -f -- "$staging_path"; }; trap cleanup_stage EXIT HUP INT TERM; cat >&3 || exit 74; exec 3>&-; actual=$(sha256sum -- "$staging_path" 2>/dev/null) || exit 74; actual=${actual%% *}; [ "$actual" = "$digest" ] || exit 65; chmod 0755 -- "$staging_path" || exit 74; trap - EXIT HUP INT TERM`
	return fixedShellCommand(body, targetPath, stagingPath, digest)
}

func promoteCommand(targetPath, stagingPath, digest, baseline string) string {
	body := `target_path=$(printf %s "$1" | base64 -d) || exit 64; staging_path=$(printf %s "$2" | base64 -d) || exit 64; digest=$(printf %s "$3" | base64 -d) || exit 64; baseline=$(printf %s "$4" | base64 -d) || exit 64; lock_path=${target_path}.install.lock; umask 077; exec 9>"$lock_path" || exit 74; flock -x 9 || exit 74; if [ "$baseline" = missing ]; then [ ! -e "$target_path" ] || exit 76; else current=$(sha256sum -- "$target_path" 2>/dev/null) || exit 76; current=${current%% *}; [ "$current" = "$baseline" ] || exit 76; fi; actual=$(sha256sum -- "$staging_path" 2>/dev/null) || exit 74; actual=${actual%% *}; [ "$actual" = "$digest" ] || exit 65; chmod 0755 -- "$staging_path" || exit 74; mv -f -- "$staging_path" "$target_path" || exit 74`
	return fixedShellCommand(body, targetPath, stagingPath, digest, baseline)
}

func (r *Runtime) cleanupStage(connection box.Connection, stagingPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body := `staging_path=$(printf %s "$1" | base64 -d) || exit 64; rm -f -- "$staging_path"`
	_, _ = r.runRemote(ctx, connection, fixedShellCommand(body, stagingPath), nil)
}

func installFailure(action string, result remoteResult) error {
	if result.ExitCode == 65 {
		return box.NewError("checksum_mismatch", "remote host runtime checksum verification failed", fmt.Errorf("remote exit status %d", result.ExitCode))
	}
	failure := remoteFailure("host runtime installation", result)
	message := fmt.Sprintf("failed to %s the host runtime", action)
	if diagnostic := safeDiagnostic(string(result.Stderr)); diagnostic != "" {
		message += ": " + diagnostic
	}
	return box.NewError("host_runtime_install_failed", message, failure)
}

func artifactError(err error) error {
	switch artifact.ErrorCode(err) {
	case artifact.CodeChecksumMismatch, artifact.CodeInvalidManifest:
		return box.NewError("checksum_mismatch", err.Error(), err)
	case artifact.CodeUnsupportedPlatform:
		return box.NewError("unsupported", err.Error(), err)
	default:
		return box.NewError("artifact_unavailable", err.Error(), err)
	}
}

func protocolError(err error) error {
	switch hostruntime.ErrorCode(err) {
	case hostruntime.CodeInvalidIdentity:
		return box.NewError("conflict", err.Error(), err)
	case hostruntime.CodeUnsupportedProtocol, hostruntime.CodeCapabilityUnavailable, hostruntime.CodeInvalidMessage:
		return box.NewError("host_runtime_incompatible", err.Error(), err)
	default:
		return box.NewError("host_runtime_incompatible", "host runtime handshake failed", err)
	}
}

func isProtocolError(err error) bool {
	var target *hostruntime.Error
	return errors.As(err, &target)
}

func helloError(err error) error {
	if isProtocolError(err) {
		return protocolError(err)
	}
	return err
}

func hostRuntime(runtimePath string, hello hostruntime.Hello) box.HostRuntime {
	return box.HostRuntime{Path: runtimePath, Version: hello.SchoonerVersion, ProtocolVersion: hello.ProtocolVersion, Capabilities: append([]string(nil), hello.Capabilities...)}
}

func capabilities(inspection hostruntime.Inspection, runtimePath string, hello hostruntime.Hello) box.Capabilities {
	return box.Capabilities{
		OSID:               inspection.OSID,
		OSVersion:          inspection.OSVersion,
		Architecture:       inspection.Architecture,
		Home:               inspection.Home,
		RemoteIdentity:     inspection.BoxIdentity,
		WorktreeRoot:       inspection.WorktreeRoot,
		WorktreeRootExists: inspection.WorktreeRootExists,
		Git:                box.Tool{Available: inspection.Git.Available, Version: inspection.Git.Version},
		Tmux:               box.Tool{Available: inspection.Tmux.Available, Version: inspection.Tmux.Version},
		PasswordlessSudo:   inspection.PasswordlessSudo,
		Host:               hostRuntime(runtimePath, hello),
	}
}

func secureSuffix() (string, error) {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}
