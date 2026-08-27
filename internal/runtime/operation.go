package runtime

import (
	"encoding/json"

	"github.com/thewelshrich/schooner/internal/source"
)

// Operation is the typed contract shared by the private host CLI and SSH
// adapters for one bounded JSON operation. It owns the fixed command,
// capability, request validation, and result invariants; adapter-specific
// transport and domain execution remain explicit.
type operationRequest interface {
	operationIdentity() string
}

type Operation[Request, Result any] struct {
	command         string
	capability      string
	requestIdentity func(Request) string
	validateRequest func(Request) error
	validateResult  func(Request, Result) error
}

type operationDescriptor interface {
	Command() string
	capabilityName() string
}

func boundedOperationContracts() []operationDescriptor {
	return []operationDescriptor{
		ConfigureOperation(),
		RepositoryCloneOperation(),
		SourceIdentityEnsureOperation(),
		SourceIdentityInspectOperation(),
		SourceIdentityRemoveOperation(),
		SourceRepositoryVerifyOperation(),
		SessionListOperation(),
		SessionLogsOperation(),
		SessionStartOperation(),
		SessionStopOperation(),
		WorktreeAddOperation(),
		WorktreeInspectOperation(),
		WorktreeListOperation(),
		WorktreePruneOperation(),
		WorktreeRemoveOperation(),
	}
}

func newOperation[Request operationRequest, Result any](
	command, capability string,
	validateRequest func(Request) error,
	validateResult func(Request, Result) error,
) Operation[Request, Result] {
	return Operation[Request, Result]{
		command:         command,
		capability:      capability,
		requestIdentity: func(request Request) string { return request.operationIdentity() },
		validateRequest: validateRequest,
		validateResult:  validateResult,
	}
}

func (o Operation[Request, Result]) Command() string        { return o.command }
func (o Operation[Request, Result]) capabilityName() string { return o.capability }

func (o Operation[Request, Result]) ValidateHello(request Request, hello Hello) error {
	return ValidateHello(hello, o.requestIdentity(request), o.capability)
}

func (o Operation[Request, Result]) ValidateRequest(request Request) error {
	return o.validateRequest(request)
}

func (o Operation[Request, Result]) ValidateResult(request Request, result Result) error {
	return o.validateResult(request, result)
}

func (o Operation[Request, Result]) DecodeRequest(data []byte) (Request, error) {
	var request Request
	if err := DecodeStrict(data, &request); err != nil {
		return request, err
	}
	return request, nil
}

func (o Operation[Request, Result]) EncodeRequest(request Request) ([]byte, error) {
	return json.Marshal(request)
}

func (o Operation[Request, Result]) DecodeResult(data []byte, request Request) (Result, *OperationError, error) {
	var result Result
	failure, present, err := DecodeOperationError(data, o.requestIdentity(request))
	if err != nil {
		return result, nil, err
	}
	if present {
		return result, &failure, nil
	}
	if err = DecodeStrict(data, &result); err != nil {
		return result, nil, err
	}
	if err = o.ValidateResult(request, result); err != nil {
		return result, nil, err
	}
	return result, nil, nil
}

func (request ConfigureRequest) operationIdentity() string              { return request.BoxIdentity }
func (request WorktreeRequest) operationIdentity() string               { return request.BoxIdentity }
func (request CloneRequest) operationIdentity() string                  { return request.BoxIdentity }
func (request WorktreeMutationRequest) operationIdentity() string       { return request.BoxIdentity }
func (request SessionListRequest) operationIdentity() string            { return request.BoxIdentity }
func (request SessionStartRequest) operationIdentity() string           { return request.BoxIdentity }
func (request SessionLogsRequest) operationIdentity() string            { return request.BoxIdentity }
func (request SessionTargetRequest) operationIdentity() string          { return request.BoxIdentity }
func (request SourceIdentityRequest) operationIdentity() string         { return request.BoxIdentity }
func (request SourceIdentityEnsureRequest) operationIdentity() string   { return request.BoxIdentity }
func (request SourceIdentityRemoveRequest) operationIdentity() string   { return request.BoxIdentity }
func (request SourceRepositoryVerifyRequest) operationIdentity() string { return request.BoxIdentity }

func ConfigureOperation() Operation[ConfigureRequest, ConfigureResult] {
	return newOperation(
		"host configure",
		CapabilityConfigureV1,
		ValidateConfigureRequest,
		func(request ConfigureRequest, result ConfigureResult) error {
			if err := validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.BoxIdentity, "host configuration returned an invalid result"); err != nil {
				return err
			}
			if result.WorktreeRoot != request.WorktreeRoot {
				return invalidOperationResult("host configuration returned an invalid result")
			}
			return nil
		},
	)
}

func WorktreeListOperation() Operation[WorktreeRequest, WorktreeCatalog] {
	return worktreeListOperation(CapabilityWorktreeListV1)
}

// ContextWorktreeListOperation uses the v1 worktree-list wire shape but also
// requires the runtime to preserve credential-free SSH usernames in origins.
func ContextWorktreeListOperation() Operation[WorktreeRequest, WorktreeCatalog] {
	return worktreeListOperation(CapabilityOriginIdentityV1)
}

func worktreeListOperation(capability string) Operation[WorktreeRequest, WorktreeCatalog] {
	return newOperation(
		"host worktree list",
		capability,
		func(request WorktreeRequest) error { return ValidateWorktreeRequest(request, false) },
		func(request WorktreeRequest, result WorktreeCatalog) error {
			return validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.BoxIdentity, "worktree list returned an incompatible result")
		},
	)
}

func WorktreeInspectOperation() Operation[WorktreeRequest, WorktreeInspection] {
	return newOperation(
		"host worktree inspect",
		CapabilityWorktreeInspectV1,
		func(request WorktreeRequest) error { return ValidateWorktreeRequest(request, true) },
		func(request WorktreeRequest, result WorktreeInspection) error {
			return validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.BoxIdentity, "worktree inspection returned an incompatible result")
		},
	)
}

func RepositoryCloneOperation() Operation[CloneRequest, LifecycleResult] {
	return newLifecycleOperation("host repository clone", CapabilityRepositoryCloneV1, "clone", ValidateCloneRequest)
}

func SourceIdentityInspectOperation() Operation[SourceIdentityRequest, SourceIdentityResult] {
	return newOperation(
		"host source identity inspect", CapabilitySourceIdentityInspectV1, ValidateSourceIdentityRequest,
		func(request SourceIdentityRequest, result SourceIdentityResult) error {
			if err := validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.BoxIdentity, "source identity inspection returned an incompatible result"); err != nil {
				return err
			}
			return validateSourceIdentityResult(request.Provider, result.HostIdentity, false)
		},
	)
}

func SourceIdentityEnsureOperation() Operation[SourceIdentityEnsureRequest, SourceIdentityResult] {
	return newOperation(
		"host source identity ensure", CapabilitySourceIdentityEnsureV1, ValidateSourceIdentityEnsureRequest,
		func(request SourceIdentityEnsureRequest, result SourceIdentityResult) error {
			if err := validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.BoxIdentity, "source identity ensure returned an incompatible result"); err != nil {
				return err
			}
			return validateSourceIdentityResult(request.Provider, result.HostIdentity, true)
		},
	)
}

func SourceIdentityRemoveOperation() Operation[SourceIdentityRemoveRequest, SourceIdentityRemoveResult] {
	return newOperation(
		"host source identity remove", CapabilitySourceIdentityRemoveV1, ValidateSourceIdentityRemoveRequest,
		func(request SourceIdentityRemoveRequest, result SourceIdentityRemoveResult) error {
			if err := validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.BoxIdentity, "source identity removal returned an incompatible result"); err != nil {
				return err
			}
			if result.Provider != request.Provider || !result.Removed {
				return invalidOperationResult("source identity removal returned an invalid result")
			}
			return nil
		},
	)
}

func SourceRepositoryVerifyOperation() Operation[SourceRepositoryVerifyRequest, SourceRepositoryVerifyResult] {
	return newOperation(
		"host source repository verify", CapabilitySourceRepositoryVerifyV1, ValidateSourceRepositoryVerifyRequest,
		func(request SourceRepositoryVerifyRequest, result SourceRepositoryVerifyResult) error {
			if err := validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.BoxIdentity, "source repository verification returned an incompatible result"); err != nil {
				return err
			}
			if result.Provider != request.Provider || !result.Authenticated {
				return invalidOperationResult("source repository verification returned an invalid result")
			}
			return nil
		},
	)
}

func validateSourceIdentityResult(provider string, result source.HostIdentity, required bool) error {
	if result.Provider != provider || (required && (!result.Exists || !result.TrustConfigured || result.PublicKey == "" || result.Fingerprint == "" || len(result.HostFingerprints) == 0)) {
		return invalidOperationResult("source identity operation returned an invalid result")
	}
	if result.TrustConfigured {
		if source.ValidateHostFingerprints(result.HostFingerprints) != nil {
			return invalidOperationResult("source identity operation returned an invalid result")
		}
	} else if len(result.HostFingerprints) != 0 {
		return invalidOperationResult("source identity operation returned an invalid result")
	}
	if !result.Exists {
		if result.PublicKey != "" || result.Fingerprint != "" {
			return invalidOperationResult("source identity operation returned an invalid result")
		}
		return nil
	}
	fingerprint, err := source.PublicKeyFingerprint(result.PublicKey)
	if err != nil || fingerprint != result.Fingerprint {
		return invalidOperationResult("source identity operation returned an invalid result")
	}
	return nil
}

func WorktreeAddOperation() Operation[WorktreeMutationRequest, LifecycleResult] {
	return newLifecycleOperation("host worktree add", CapabilityWorktreeAddV1, "worktree_add", func(request WorktreeMutationRequest) error {
		return ValidateWorktreeMutationRequest(request, "add")
	})
}

func WorktreeRemoveOperation() Operation[WorktreeMutationRequest, LifecycleResult] {
	return newLifecycleOperation("host worktree remove", CapabilityWorktreeRemoveV1, "worktree_remove", func(request WorktreeMutationRequest) error {
		return ValidateWorktreeMutationRequest(request, "remove")
	})
}

func WorktreePruneOperation() Operation[WorktreeMutationRequest, LifecycleResult] {
	return newLifecycleOperation("host worktree prune", CapabilityWorktreePruneV1, "worktree_prune", func(request WorktreeMutationRequest) error {
		return ValidateWorktreeMutationRequest(request, "prune")
	})
}

func newLifecycleOperation[Request interface {
	operationRequest
	CloneRequest | WorktreeMutationRequest
}](
	command, capability, action string,
	validateRequest func(Request) error,
) Operation[Request, LifecycleResult] {
	return newOperation(
		command,
		capability,
		validateRequest,
		func(request Request, result LifecycleResult) error {
			if err := validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.operationIdentity(), "Git lifecycle operation returned an incompatible result"); err != nil {
				return err
			}
			if result.Action != action {
				return invalidOperationResult("Git lifecycle operation returned an incompatible result")
			}
			return nil
		},
	)
}

func SessionListOperation() Operation[SessionListRequest, SessionCatalog] {
	return newOperation(
		"host session list",
		CapabilitySessionListV1,
		ValidateSessionListRequest,
		func(request SessionListRequest, result SessionCatalog) error {
			if err := validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.BoxIdentity, "Session list returned an incompatible result"); err != nil {
				return err
			}
			if result.WorktreeRoot != request.WorktreeRoot {
				return invalidOperationResult("Session list returned an incompatible result")
			}
			return nil
		},
	)
}

func SessionStartOperation() Operation[SessionStartRequest, SessionStartResult] {
	return newOperation(
		"host session start",
		CapabilitySessionStartV1,
		ValidateSessionStartRequest,
		func(request SessionStartRequest, result SessionStartResult) error {
			if err := validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.BoxIdentity, "Session start returned an incompatible result"); err != nil {
				return err
			}
			if result.WorktreeRoot != request.WorktreeRoot || result.Session.WorktreePath == "" {
				return invalidOperationResult("Session start returned an incompatible result")
			}
			return nil
		},
	)
}

func SessionLogsOperation() Operation[SessionLogsRequest, SessionLogsResult] {
	return newOperation(
		"host session logs",
		CapabilitySessionLogsV1,
		ValidateSessionLogsRequest,
		func(request SessionLogsRequest, result SessionLogsResult) error {
			if err := validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.BoxIdentity, "Session logs returned an incompatible result"); err != nil {
				return err
			}
			if result.WorktreeRoot != request.WorktreeRoot || result.SessionID != request.SessionID {
				return invalidOperationResult("Session logs returned an incompatible result")
			}
			return nil
		},
	)
}

func SessionStopOperation() Operation[SessionTargetRequest, SessionStopResult] {
	return newOperation(
		"host session stop",
		CapabilitySessionStopV1,
		ValidateSessionTargetRequest,
		func(request SessionTargetRequest, result SessionStopResult) error {
			if err := validateOperationEnvelope(result.SchemaVersion, result.ProtocolVersion, result.BoxIdentity, request.BoxIdentity, "Session stop returned an incompatible result"); err != nil {
				return err
			}
			if result.WorktreeRoot != request.WorktreeRoot || result.SessionID != request.Selector || !result.Stopped {
				return invalidOperationResult("Session stop returned an incompatible result")
			}
			return nil
		},
	)
}

func validateOperationEnvelope(schema, protocol, identity, expectedIdentity, message string) error {
	if schema != SchemaVersion || protocol != ProtocolVersion || identity != expectedIdentity || !identityPattern.MatchString(identity) {
		return invalidOperationResult(message)
	}
	return nil
}

func invalidOperationResult(message string) error {
	return &Error{Code: CodeInvalidMessage, Message: message}
}
