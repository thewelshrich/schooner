// Package runtime defines Schooner's versioned, one-shot host protocol.
package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/thewelshrich/schooner/internal/repository"
	"github.com/thewelshrich/schooner/internal/source"
)

const (
	SchemaVersion   = "1"
	ProtocolVersion = "1"
	MaxMessageBytes = 1 << 20

	CapabilityHelloV1                  = "host.hello.v1"
	CapabilityInspectV2                = "host.inspect.v2"
	CapabilityDoctorV1                 = "host.doctor.v1"
	CapabilityConfigureV1              = "host.configure.v1"
	CapabilityWorktreeListV1           = "worktree.list.v1"
	CapabilityOriginIdentityV1         = "repository.identity.v1"
	CapabilityWorktreeInspectV1        = "worktree.inspect.v1"
	CapabilityRepositoryCloneV1        = "repository.clone.v1"
	CapabilityRepositoryCloneV2        = "repository.clone.v2"
	CapabilitySourceIdentityInspectV1  = "source.identity.inspect.v1"
	CapabilitySourceIdentityEnsureV1   = "source.identity.ensure.v1"
	CapabilitySourceIdentityRemoveV1   = "source.identity.remove.v1"
	CapabilitySourceRepositoryVerifyV1 = "source.repository.verify.v1"
	CapabilityWorktreeAddV1            = "worktree.add.v1"
	CapabilityWorktreeRemoveV1         = "worktree.remove.v1"
	CapabilityWorktreePruneV1          = "worktree.prune.v1"
	CapabilitySessionListV1            = "session.list.v1"
	CapabilitySessionStartV1           = "session.start.v1"
	CapabilitySessionResumeV1          = "session.resume.v1"
	CapabilitySessionLogsV1            = "session.logs.v1"
	CapabilitySessionStopV1            = "session.stop.v1"
	CapabilityWorktreeShellV1          = "worktree.shell.v1"
	CapabilityWorkspacePushInspectV1   = "workspace.push.inspect.v1"
	CapabilityWorkspacePushApplyV1     = "workspace.push.apply.v1"
	CapabilityWorkspacePullInspectV1   = "workspace.pull.inspect.v1"
	CapabilityWorkspacePullCaptureV1   = "workspace.pull.capture.v1"
)

var (
	identityPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	capabilityPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:\.[a-z0-9]+)*\.v[1-9][0-9]*$`)
	platformPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sha1Pattern        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	operationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type Code string

const (
	CodeInvalidMessage        Code = "invalid_message"
	CodeInvalidIdentity       Code = "invalid_identity"
	CodeUnsupportedProtocol   Code = "unsupported_protocol"
	CodeCapabilityUnavailable Code = "capability_unavailable"
	CodeNotFound              Code = "not_found"
	CodeInvalidInput          Code = "invalid_input"
	CodeConflict              Code = "conflict"
	CodeAuthentication        Code = "authentication_required"
	CodePermissionDenied      Code = "permission_denied"
	CodeOperationInProgress   Code = "operation_in_progress"
	CodeInsufficientSpace     Code = "insufficient_space"
	CodeOutcomeUnknown        Code = "outcome_unknown"
)

type Error struct {
	Code    Code
	Message string
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func ErrorCode(err error) Code {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

type BuildInfo struct {
	Version string
	Commit  string
}

type Tool struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
}

type Hello struct {
	SchemaVersion   string   `json:"schema_version"`
	ProtocolVersion string   `json:"protocol_version"`
	SchoonerVersion string   `json:"schooner_version"`
	Commit          string   `json:"commit"`
	BoxIdentity     string   `json:"box_identity"`
	OS              string   `json:"os"`
	Architecture    string   `json:"architecture"`
	Capabilities    []string `json:"capabilities"`
}

type InspectRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	WorktreeRoot    string `json:"worktree_root"`
}

type ConfigureRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
}

type WorktreeRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	Selector        string `json:"selector,omitempty"`
}

type CloneRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
	Source          string `json:"source"`
	Branch          string `json:"branch,omitempty"`
	Destination     string `json:"destination,omitempty"`
	NonInteractive  bool   `json:"-"`
}

type WorktreeMutationRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
	RepositoryPath  string `json:"repository_path,omitempty"`
	Path            string `json:"path,omitempty"`
	Branch          string `json:"branch,omitempty"`
	NonInteractive  bool   `json:"-"`
}

type WorkspacePushInspectRequest struct {
	SchemaVersion       string                    `json:"schema_version"`
	ProtocolVersion     string                    `json:"protocol_version"`
	BoxIdentity         string                    `json:"box_identity"`
	Worktree            string                    `json:"worktree"`
	IncomingFiles       []repository.CheckoutFile `json:"incoming_files,omitempty"`
	IncomingBranch      string                    `json:"incoming_branch,omitempty"`
	IncomingDetached    bool                      `json:"incoming_detached,omitempty"`
	IncomingStateDigest string                    `json:"incoming_state_digest,omitempty"`
	PreflightBranch     bool                      `json:"preflight_branch,omitempty"`
}

type WorkspacePushApplyRequest struct {
	SchemaVersion               string `json:"schema_version"`
	ProtocolVersion             string `json:"protocol_version"`
	BoxIdentity                 string `json:"box_identity"`
	OperationID                 string `json:"operation_id"`
	Worktree                    string `json:"worktree"`
	ExpectedStateDigest         string `json:"expected_state_digest,omitempty"`
	PayloadSize                 int64  `json:"payload_size"`
	PayloadSHA256               string `json:"payload_sha256"`
	SourceStateDigest           string `json:"source_state_digest"`
	OperationCreatedDestination bool   `json:"operation_created_destination,omitempty"`
	OperationCreatedBranch      string `json:"operation_created_branch,omitempty"`
}

type WorkspacePullInspectRequest struct {
	SchemaVersion                    string `json:"schema_version"`
	ProtocolVersion                  string `json:"protocol_version"`
	BoxIdentity                      string `json:"box_identity"`
	Worktree                         string `json:"worktree"`
	DestinationHEAD                  string `json:"destination_head"`
	IncludeManifest                  bool   `json:"include_manifest,omitempty"`
	ManifestOffset                   int    `json:"manifest_offset,omitempty"`
	ExpectedSourceRevalidationDigest string `json:"expected_source_revalidation_digest,omitempty"`
}

type WorkspacePullCaptureRequest struct {
	SchemaVersion                    string `json:"schema_version"`
	ProtocolVersion                  string `json:"protocol_version"`
	BoxIdentity                      string `json:"box_identity"`
	OperationID                      string `json:"operation_id"`
	Worktree                         string `json:"worktree"`
	DestinationHEAD                  string `json:"destination_head"`
	ExpectedSourceRevalidationDigest string `json:"expected_source_revalidation_digest"`
}

type SourceIdentityRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	Provider        string `json:"provider"`
}

type SourceIdentityEnsureRequest struct {
	SchemaVersion   string           `json:"schema_version"`
	ProtocolVersion string           `json:"protocol_version"`
	BoxIdentity     string           `json:"box_identity"`
	Provider        string           `json:"provider"`
	HostKeys        []source.HostKey `json:"host_keys"`
}

type SourceIdentityRemoveRequest struct {
	SchemaVersion       string `json:"schema_version"`
	ProtocolVersion     string `json:"protocol_version"`
	BoxIdentity         string `json:"box_identity"`
	Provider            string `json:"provider"`
	ExpectedFingerprint string `json:"expected_fingerprint"`
}

type SourceRepositoryVerifyRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	Provider        string `json:"provider"`
	Repository      string `json:"repository,omitempty"`
}

type ConfigureResult struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
}

type WorktreeCatalog struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	repository.Catalog
}

type WorktreeInspection struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	repository.Inspection
}

type LifecycleResult struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	repository.MutationResult
}

type WorkspacePushInspection struct {
	SchemaVersion   string                    `json:"schema_version"`
	ProtocolVersion string                    `json:"protocol_version"`
	BoxIdentity     string                    `json:"box_identity"`
	Present         bool                      `json:"present"`
	State           *repository.CheckoutState `json:"state,omitempty"`
	ExistingFiles   int                       `json:"existing_files,omitempty"`
	MatchingFiles   int                       `json:"matching_files,omitempty"`
}

type WorkspacePushApplyResult struct {
	SchemaVersion    string                   `json:"schema_version"`
	ProtocolVersion  string                   `json:"protocol_version"`
	BoxIdentity      string                   `json:"box_identity"`
	State            repository.CheckoutState `json:"state"`
	BytesTransferred int64                    `json:"bytes_transferred"`
}

type WorkspacePullInspection struct {
	SchemaVersion       string                    `json:"schema_version"`
	ProtocolVersion     string                    `json:"protocol_version"`
	BoxIdentity         string                    `json:"box_identity"`
	State               repository.CheckoutState  `json:"state"`
	DestinationAncestor bool                      `json:"destination_ancestor"`
	Manifest            []repository.CheckoutFile `json:"manifest,omitempty"`
	NextManifestOffset  int                       `json:"next_manifest_offset,omitempty"`
	ManifestComplete    bool                      `json:"manifest_complete"`
}

type WorkspacePullCaptureResult struct {
	SchemaVersion       string                   `json:"schema_version"`
	ProtocolVersion     string                   `json:"protocol_version"`
	BoxIdentity         string                   `json:"box_identity"`
	OperationID         string                   `json:"operation_id"`
	State               repository.CheckoutState `json:"state"`
	DestinationAncestor bool                     `json:"destination_ancestor"`
	PayloadSize         int64                    `json:"payload_size"`
	PayloadSHA256       string                   `json:"payload_sha256"`
}

type SourceIdentityResult struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	source.HostIdentity
}

type SourceIdentityRemoveResult struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	source.RemoveIdentityResult
}

type SourceRepositoryVerifyResult struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	source.VerifyResult
}

type OperationErrorDetail struct {
	Code    Code              `json:"code"`
	Message string            `json:"message"`
	Context map[string]string `json:"context,omitempty"`
}

type OperationError struct {
	SchemaVersion   string               `json:"schema_version"`
	ProtocolVersion string               `json:"protocol_version"`
	BoxIdentity     string               `json:"box_identity"`
	Error           OperationErrorDetail `json:"error"`
}

type Inspection struct {
	SchemaVersion      string `json:"schema_version"`
	ProtocolVersion    string `json:"protocol_version"`
	OSID               string `json:"os_id"`
	OSVersion          string `json:"os_version"`
	Architecture       string `json:"architecture"`
	Home               string `json:"home"`
	BoxIdentity        string `json:"box_identity,omitempty"`
	WorktreeRoot       string `json:"worktree_root"`
	WorktreeRootExists bool   `json:"worktree_root_exists"`
	Git                Tool   `json:"git"`
	Tmux               Tool   `json:"tmux"`
	PasswordlessSudo   bool   `json:"passwordless_sudo"`
}

type Check struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type DoctorReport struct {
	SchemaVersion   string     `json:"schema_version"`
	ProtocolVersion string     `json:"protocol_version"`
	Healthy         bool       `json:"healthy"`
	Inspection      Inspection `json:"inspection"`
	Checks          []Check    `json:"checks"`
}

func Capabilities() []string {
	result := []string{CapabilityDoctorV1, CapabilityHelloV1, CapabilityInspectV2, CapabilityOriginIdentityV1, CapabilitySessionResumeV1, CapabilityWorktreeShellV1}
	for _, operation := range boundedOperationContracts() {
		result = append(result, operation.capabilityName())
	}
	slices.Sort(result)
	return result
}

func NewInspectRequest(worktreeRoot string) InspectRequest {
	return InspectRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, WorktreeRoot: worktreeRoot}
}

func NewConfigureRequest(worktreeRoot, boxIdentity string) ConfigureRequest {
	return ConfigureRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, WorktreeRoot: worktreeRoot}
}

func NewWorktreeRequest(selector, boxIdentity string) WorktreeRequest {
	return WorktreeRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, Selector: selector}
}

func NewCloneRequest(source, branch, worktreeRoot, boxIdentity string) CloneRequest {
	return CloneRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, WorktreeRoot: worktreeRoot, Source: source, Branch: branch}
}

func NewWorktreeMutationRequest(repositoryPath, pathValue, branch, worktreeRoot, boxIdentity string) WorktreeMutationRequest {
	return WorktreeMutationRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, WorktreeRoot: worktreeRoot, RepositoryPath: repositoryPath, Path: pathValue, Branch: branch}
}

func NewWorkspacePushInspectRequest(worktree, boxIdentity string) WorkspacePushInspectRequest {
	return WorkspacePushInspectRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, Worktree: worktree}
}

func NewWorkspacePushApplyRequest(operationID, worktree, expectedDigest, payloadDigest string, payloadSize int64, sourceStateDigest, boxIdentity string) WorkspacePushApplyRequest {
	return WorkspacePushApplyRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, OperationID: operationID, Worktree: worktree, ExpectedStateDigest: expectedDigest, PayloadSize: payloadSize, PayloadSHA256: payloadDigest, SourceStateDigest: sourceStateDigest}
}

func NewWorkspacePullInspectRequest(worktree, destinationHEAD, boxIdentity string, includeManifest bool) WorkspacePullInspectRequest {
	return WorkspacePullInspectRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, Worktree: worktree, DestinationHEAD: destinationHEAD, IncludeManifest: includeManifest}
}

func NewWorkspacePullCaptureRequest(operationID, worktree, destinationHEAD, expectedSourceDigest, boxIdentity string) WorkspacePullCaptureRequest {
	return WorkspacePullCaptureRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, OperationID: operationID, Worktree: worktree, DestinationHEAD: destinationHEAD, ExpectedSourceRevalidationDigest: expectedSourceDigest}
}

func ValidateWorkspacePushInspectRequest(request WorkspacePushInspectRequest) error {
	if request.SchemaVersion != SchemaVersion || request.ProtocolVersion != ProtocolVersion {
		return &Error{Code: CodeUnsupportedProtocol, Message: "workspace push request is incompatible"}
	}
	if !identityPattern.MatchString(request.BoxIdentity) || !validPath(request.Worktree) {
		return &Error{Code: CodeInvalidInput, Message: "workspace push request is invalid"}
	}
	if request.IncomingStateDigest == "" {
		if len(request.IncomingFiles) != 0 || request.IncomingBranch != "" || request.IncomingDetached || request.PreflightBranch {
			return &Error{Code: CodeInvalidInput, Message: "workspace push inspection cannot include preflight metadata"}
		}
		return nil
	}
	if !sha256Pattern.MatchString(request.IncomingStateDigest) || (!request.IncomingDetached && request.IncomingBranch == "") || (request.IncomingDetached && request.IncomingBranch != "") {
		return &Error{Code: CodeInvalidInput, Message: "workspace push preflight checkout metadata is invalid"}
	}
	seen := make(map[string]struct{}, len(request.IncomingFiles))
	for _, entry := range request.IncomingFiles {
		_, duplicate := seen[entry.Path]
		validEntry := entry.Kind == "absent" && entry.Tracked && entry.Size == 0 && entry.SHA256 == "" && !entry.Executable
		if entry.Kind == "file" || entry.Kind == "symlink" {
			validEntry = entry.Size >= 0 && sha256Pattern.MatchString(entry.SHA256) && (entry.Kind != "symlink" || !entry.Executable)
		}
		if !validCheckoutPath(entry.Path) || !validEntry || duplicate {
			return &Error{Code: CodeInvalidInput, Message: "workspace push preflight manifest is invalid"}
		}
		seen[entry.Path] = struct{}{}
	}
	return nil
}

func ValidateWorkspacePushApplyRequest(request WorkspacePushApplyRequest) error {
	if err := ValidateWorkspacePushInspectRequest(WorkspacePushInspectRequest{SchemaVersion: request.SchemaVersion, ProtocolVersion: request.ProtocolVersion, BoxIdentity: request.BoxIdentity, Worktree: request.Worktree}); err != nil {
		return err
	}
	if !operationIDPattern.MatchString(request.OperationID) || request.PayloadSize <= 0 || request.PayloadSize > 1<<40 || !sha256Pattern.MatchString(request.PayloadSHA256) || (request.ExpectedStateDigest != "" && !sha256Pattern.MatchString(request.ExpectedStateDigest)) || !sha256Pattern.MatchString(request.SourceStateDigest) {
		return &Error{Code: CodeInvalidInput, Message: "workspace push payload declaration is invalid"}
	}
	if request.OperationCreatedDestination && (request.ExpectedStateDigest == "" || len(request.OperationCreatedBranch) > 1024 || hasControl(request.OperationCreatedBranch)) {
		return &Error{Code: CodeInvalidInput, Message: "operation-created workspace push requires an expected destination state"}
	}
	if !request.OperationCreatedDestination && request.OperationCreatedBranch != "" {
		return &Error{Code: CodeInvalidInput, Message: "workspace push branch rewind declaration is invalid"}
	}
	return nil
}

func ValidateWorkspacePullInspectRequest(request WorkspacePullInspectRequest) error {
	if request.SchemaVersion != SchemaVersion || request.ProtocolVersion != ProtocolVersion {
		return &Error{Code: CodeUnsupportedProtocol, Message: "workspace pull request is incompatible"}
	}
	if !identityPattern.MatchString(request.BoxIdentity) || !validPath(request.Worktree) || !sha1Pattern.MatchString(request.DestinationHEAD) || request.ManifestOffset < 0 {
		return &Error{Code: CodeInvalidInput, Message: "workspace pull request is invalid"}
	}
	if !request.IncludeManifest && (request.ManifestOffset != 0 || request.ExpectedSourceRevalidationDigest != "") {
		return &Error{Code: CodeInvalidInput, Message: "workspace pull summary request cannot include manifest continuation metadata"}
	}
	if request.ManifestOffset > 0 && !sha256Pattern.MatchString(request.ExpectedSourceRevalidationDigest) {
		return &Error{Code: CodeInvalidInput, Message: "workspace pull manifest continuation is invalid"}
	}
	if request.ExpectedSourceRevalidationDigest != "" && !sha256Pattern.MatchString(request.ExpectedSourceRevalidationDigest) {
		return &Error{Code: CodeInvalidInput, Message: "workspace pull expected source state is invalid"}
	}
	return nil
}

func ValidateWorkspacePullCaptureRequest(request WorkspacePullCaptureRequest) error {
	base := NewWorkspacePullInspectRequest(request.Worktree, request.DestinationHEAD, request.BoxIdentity, false)
	if err := ValidateWorkspacePullInspectRequest(base); err != nil {
		return err
	}
	if !operationIDPattern.MatchString(request.OperationID) || !sha256Pattern.MatchString(request.ExpectedSourceRevalidationDigest) {
		return &Error{Code: CodeInvalidInput, Message: "workspace pull capture request is invalid"}
	}
	return nil
}

func NewSourceIdentityRequest(provider, boxIdentity string) SourceIdentityRequest {
	return SourceIdentityRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, Provider: provider}
}

func NewSourceIdentityEnsureRequest(provider, boxIdentity string, hostKeys []source.HostKey) SourceIdentityEnsureRequest {
	return SourceIdentityEnsureRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, Provider: provider, HostKeys: append([]source.HostKey(nil), hostKeys...)}
}

func NewSourceIdentityRemoveRequest(provider, boxIdentity, expectedFingerprint string) SourceIdentityRemoveRequest {
	return SourceIdentityRemoveRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, Provider: provider, ExpectedFingerprint: expectedFingerprint}
}

func NewSourceRepositoryVerifyRequest(provider, repository, boxIdentity string) SourceRepositoryVerifyRequest {
	return SourceRepositoryVerifyRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity, Provider: provider, Repository: repository}
}

func NewOperationError(boxIdentity string, code Code, message string) OperationError {
	return NewOperationErrorWithContext(boxIdentity, code, message, nil)
}

func NewOperationErrorWithContext(boxIdentity string, code Code, message string, contextValues map[string]string) OperationError {
	return OperationError{
		SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity,
		Error: OperationErrorDetail{Code: code, Message: message, Context: contextValues},
	}
}

func DecodeOperationError(data []byte, expectedIdentity string) (OperationError, bool, error) {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || len(probe.Error) == 0 || bytes.Equal(probe.Error, []byte("null")) {
		return OperationError{}, false, nil
	}
	var result OperationError
	if err := DecodeStrict(data, &result); err != nil {
		return OperationError{}, true, err
	}
	if result.SchemaVersion != SchemaVersion || result.ProtocolVersion != ProtocolVersion {
		return OperationError{}, true, &Error{Code: CodeUnsupportedProtocol, Message: "host operation error is incompatible"}
	}
	if result.BoxIdentity != expectedIdentity || !identityPattern.MatchString(result.BoxIdentity) {
		return OperationError{}, true, &Error{Code: CodeInvalidIdentity, Message: "host operation error Box identity is invalid"}
	}
	if !slices.Contains([]Code{CodeNotFound, CodeInvalidInput, CodeConflict, CodeAuthentication, CodePermissionDenied, CodeOperationInProgress, CodeInsufficientSpace, CodeOutcomeUnknown, CodeCapabilityUnavailable}, result.Error.Code) || result.Error.Message == "" || len(result.Error.Message) > 4096 || strings.ContainsAny(result.Error.Message, "\x00\r\n") {
		return OperationError{}, true, &Error{Code: CodeInvalidMessage, Message: "host operation error is invalid"}
	}
	if len(result.Error.Context) > 8 {
		return OperationError{}, true, &Error{Code: CodeInvalidMessage, Message: "host operation error context is invalid"}
	}
	for key, value := range result.Error.Context {
		if !validOperationErrorContext(key, value) {
			return OperationError{}, true, &Error{Code: CodeInvalidMessage, Message: "host operation error context is invalid"}
		}
	}
	return result, true, nil
}

func validOperationErrorContext(key, value string) bool {
	if len(value) > 256 || hasControl(value) {
		return false
	}
	switch key {
	case "reason":
		return slices.Contains([]string{"ambient_host_key_changed", "credentials_missing", "github_saml_sso", "host_key_changed"}, value)
	case "organization":
		return platformPattern.MatchString(value)
	default:
		return false
	}
}

func ValidateSourceIdentityRequest(request SourceIdentityRequest) error {
	if err := validateSourceEnvelope(request.SchemaVersion, request.ProtocolVersion, request.BoxIdentity, request.Provider); err != nil {
		return err
	}
	return nil
}

func ValidateSourceIdentityEnsureRequest(request SourceIdentityEnsureRequest) error {
	if err := validateSourceEnvelope(request.SchemaVersion, request.ProtocolVersion, request.BoxIdentity, request.Provider); err != nil {
		return err
	}
	if err := source.ValidateHostKeys(request.HostKeys); err != nil {
		return &Error{Code: CodeInvalidInput, Message: "source identity host keys are invalid", Cause: err}
	}
	return nil
}

func ValidateSourceIdentityRemoveRequest(request SourceIdentityRemoveRequest) error {
	if err := validateSourceEnvelope(request.SchemaVersion, request.ProtocolVersion, request.BoxIdentity, request.Provider); err != nil {
		return err
	}
	if err := source.ValidateFingerprint(request.ExpectedFingerprint); err != nil {
		return &Error{Code: CodeInvalidInput, Message: "source identity removal fingerprint is invalid", Cause: err}
	}
	return nil
}

func ValidateSourceRepositoryVerifyRequest(request SourceRepositoryVerifyRequest) error {
	if err := validateSourceEnvelope(request.SchemaVersion, request.ProtocolVersion, request.BoxIdentity, request.Provider); err != nil {
		return err
	}
	if len(request.Repository) > 4096 || hasControl(request.Repository) {
		return &Error{Code: CodeInvalidInput, Message: "source repository verification request is invalid"}
	}
	return nil
}

func validateSourceEnvelope(schema, protocol, identity, provider string) error {
	if schema != SchemaVersion || protocol != ProtocolVersion {
		return &Error{Code: CodeUnsupportedProtocol, Message: "source operation request is incompatible"}
	}
	if !identityPattern.MatchString(identity) {
		return &Error{Code: CodeInvalidIdentity, Message: "source operation Box identity is invalid"}
	}
	if provider != source.GitHub {
		return &Error{Code: CodeInvalidInput, Message: "source provider is unsupported"}
	}
	return nil
}

func ValidateCloneRequest(request CloneRequest) error {
	if request.SchemaVersion != SchemaVersion || request.ProtocolVersion != ProtocolVersion {
		return &Error{Code: CodeUnsupportedProtocol, Message: "clone request is incompatible"}
	}
	if !identityPattern.MatchString(request.BoxIdentity) {
		return &Error{Code: CodeInvalidIdentity, Message: "clone request Box identity is invalid"}
	}
	if !validPath(request.WorktreeRoot) || request.Source == "" || len(request.Source) > 4096 || hasControl(request.Source) || len(request.Branch) > 1024 || hasControl(request.Branch) {
		return &Error{Code: CodeInvalidInput, Message: "clone request is invalid"}
	}
	if request.Destination != "" {
		prefix := strings.TrimSuffix(request.WorktreeRoot, "/") + "/"
		if !validPath(request.Destination) || request.Destination == request.WorktreeRoot || !strings.HasPrefix(request.Destination, prefix) {
			return &Error{Code: CodeInvalidInput, Message: "clone destination is outside the Worktree Root"}
		}
	}
	return nil
}

func ValidateWorktreeMutationRequest(request WorktreeMutationRequest, operation string) error {
	if request.SchemaVersion != SchemaVersion || request.ProtocolVersion != ProtocolVersion {
		return &Error{Code: CodeUnsupportedProtocol, Message: "Worktree mutation request is incompatible"}
	}
	if !identityPattern.MatchString(request.BoxIdentity) {
		return &Error{Code: CodeInvalidIdentity, Message: "Worktree mutation request Box identity is invalid"}
	}
	if !validPath(request.WorktreeRoot) {
		return &Error{Code: CodeInvalidInput, Message: "Worktree mutation request root is invalid"}
	}
	for _, value := range []string{request.RepositoryPath, request.Path, request.Branch} {
		if len(value) > 4096 || hasControl(value) {
			return &Error{Code: CodeInvalidInput, Message: "Worktree mutation request is invalid"}
		}
	}
	switch operation {
	case "add":
		if request.RepositoryPath == "" || request.Path == "" {
			return &Error{Code: CodeInvalidInput, Message: "Worktree add requires repository and destination paths"}
		}
	case "remove":
		if request.RepositoryPath != "" || request.Path == "" || request.Branch != "" {
			return &Error{Code: CodeInvalidInput, Message: "Worktree remove request is invalid"}
		}
	case "prune":
		if request.RepositoryPath != "" || request.Path != "" || request.Branch != "" {
			return &Error{Code: CodeInvalidInput, Message: "Worktree prune request is invalid"}
		}
	default:
		return &Error{Code: CodeInvalidInput, Message: "Worktree mutation operation is invalid"}
	}
	return nil
}

func ValidateConfigureRequest(request ConfigureRequest) error {
	if request.SchemaVersion != SchemaVersion || request.ProtocolVersion != ProtocolVersion {
		return &Error{Code: CodeUnsupportedProtocol, Message: "host configuration request is incompatible"}
	}
	if !identityPattern.MatchString(request.BoxIdentity) {
		return &Error{Code: CodeInvalidIdentity, Message: "host configuration Box identity is invalid"}
	}
	if !validPath(request.WorktreeRoot) {
		return &Error{Code: CodeInvalidMessage, Message: "host configuration worktree root is invalid"}
	}
	return nil
}

func ValidateWorktreeRequest(request WorktreeRequest, selectorRequired bool) error {
	if request.SchemaVersion != SchemaVersion || request.ProtocolVersion != ProtocolVersion {
		return &Error{Code: CodeUnsupportedProtocol, Message: "worktree request is incompatible"}
	}
	if !identityPattern.MatchString(request.BoxIdentity) {
		return &Error{Code: CodeInvalidIdentity, Message: "worktree request Box identity is invalid"}
	}
	if len(request.Selector) > 4096 || hasControl(request.Selector) || (selectorRequired && request.Selector == "") || (!selectorRequired && request.Selector != "") {
		return &Error{Code: CodeInvalidInput, Message: "worktree request selector is invalid"}
	}
	return nil
}

func InstallPath(home string) (string, error) {
	if !validPath(home) {
		return "", &Error{Code: CodeInvalidMessage, Message: "remote home directory is invalid"}
	}
	return path.Join(home, ".local", "bin", "schooner"), nil
}

func ValidateInspectRequest(request InspectRequest) error {
	if request.SchemaVersion != SchemaVersion {
		return &Error{Code: CodeInvalidMessage, Message: "host request uses an unsupported schema version"}
	}
	if request.ProtocolVersion != ProtocolVersion {
		return &Error{Code: CodeUnsupportedProtocol, Message: "host request uses an unsupported protocol version"}
	}
	if request.WorktreeRoot == "" || len(request.WorktreeRoot) > 4096 || hasControl(request.WorktreeRoot) {
		return &Error{Code: CodeInvalidMessage, Message: "host request worktree root is invalid"}
	}
	if request.WorktreeRoot != "~" && !strings.HasPrefix(request.WorktreeRoot, "~/") && !path.IsAbs(request.WorktreeRoot) {
		return &Error{Code: CodeInvalidMessage, Message: "host request worktree root must be absolute or begin with ~/"}
	}
	return nil
}

func ValidateHello(hello Hello, expectedIdentity string, requiredCapabilities ...string) error {
	if hello.SchemaVersion != SchemaVersion {
		return &Error{Code: CodeInvalidMessage, Message: "host hello returned an unsupported schema version"}
	}
	if hello.ProtocolVersion != ProtocolVersion {
		return &Error{Code: CodeUnsupportedProtocol, Message: fmt.Sprintf("host protocol %q is incompatible with local protocol %q", hello.ProtocolVersion, ProtocolVersion)}
	}
	if !validText(hello.SchoonerVersion, 128) || !validText(hello.Commit, 128) {
		return &Error{Code: CodeInvalidMessage, Message: "host hello returned invalid build information"}
	}
	if !identityPattern.MatchString(hello.BoxIdentity) {
		return &Error{Code: CodeInvalidIdentity, Message: "host hello returned an invalid box identity"}
	}
	if expectedIdentity != "" && hello.BoxIdentity != expectedIdentity {
		return &Error{Code: CodeInvalidIdentity, Message: "the connected machine does not match the expected box identity"}
	}
	if !platformPattern.MatchString(hello.OS) || !platformPattern.MatchString(hello.Architecture) {
		return &Error{Code: CodeInvalidMessage, Message: "host hello returned an invalid platform"}
	}
	if err := validateCapabilities(hello.Capabilities); err != nil {
		return err
	}
	for _, required := range requiredCapabilities {
		if !slices.Contains(hello.Capabilities, required) {
			return &Error{Code: CodeCapabilityUnavailable, Message: fmt.Sprintf("host runtime does not provide required capability %s", required)}
		}
	}
	return nil
}

func ValidateInspection(inspection Inspection, expectedIdentity string) error {
	if inspection.SchemaVersion != SchemaVersion {
		return &Error{Code: CodeInvalidMessage, Message: "host inspection returned an unsupported schema version"}
	}
	if inspection.ProtocolVersion != ProtocolVersion {
		return &Error{Code: CodeUnsupportedProtocol, Message: "host inspection returned an unsupported protocol version"}
	}
	if !platformPattern.MatchString(inspection.OSID) || len(inspection.OSVersion) > 64 || hasControl(inspection.OSVersion) || !platformPattern.MatchString(inspection.Architecture) || !validPath(inspection.Home) || !validPath(inspection.WorktreeRoot) {
		return &Error{Code: CodeInvalidMessage, Message: "host inspection returned invalid platform paths"}
	}
	if !validTool(inspection.Git) || !validTool(inspection.Tmux) {
		return &Error{Code: CodeInvalidMessage, Message: "host inspection returned invalid tool information"}
	}
	if inspection.BoxIdentity != "" && !identityPattern.MatchString(inspection.BoxIdentity) {
		return &Error{Code: CodeInvalidIdentity, Message: "host inspection returned an invalid box identity"}
	}
	if expectedIdentity != "" && inspection.BoxIdentity != expectedIdentity {
		return &Error{Code: CodeInvalidIdentity, Message: "host inspection returned a different box identity"}
	}
	return nil
}

func DecodeStrict(data []byte, target any) error {
	if len(data) == 0 || len(data) > MaxMessageBytes {
		return &Error{Code: CodeInvalidMessage, Message: "host protocol message is empty or exceeds 1 MiB"}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &Error{Code: CodeInvalidMessage, Message: "host protocol message is invalid", Cause: err}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return &Error{Code: CodeInvalidMessage, Message: "host protocol message contains trailing data", Cause: err}
	}
	return nil
}

func validateCapabilities(capabilities []string) error {
	if len(capabilities) == 0 || len(capabilities) > 64 || !slices.IsSorted(capabilities) {
		return &Error{Code: CodeInvalidMessage, Message: "host hello capabilities must be non-empty and sorted"}
	}
	for index, capability := range capabilities {
		if !capabilityPattern.MatchString(capability) || (index > 0 && capabilities[index-1] == capability) {
			return &Error{Code: CodeInvalidMessage, Message: "host hello returned an invalid capability set"}
		}
	}
	return nil
}

func validPath(value string) bool {
	return value != "" && len(value) <= 4096 && path.IsAbs(value) && path.Clean(value) == value && !hasControl(value)
}

func validCheckoutPath(value string) bool {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, '\\') || path.IsAbs(value) || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") || hasControl(value) {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if strings.EqualFold(component, ".git") {
			return false
		}
	}
	return true
}

func validText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && !hasControl(value)
}

func validTool(tool Tool) bool {
	if !tool.Available {
		return tool.Version == ""
	}
	return validText(tool.Version, 256)
}

func hasControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}
