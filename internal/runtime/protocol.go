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

	"github.com/thewelshrich/schooner/internal/repository"
)

const (
	SchemaVersion   = "1"
	ProtocolVersion = "1"
	MaxMessageBytes = 1 << 20

	CapabilityHelloV1           = "host.hello.v1"
	CapabilityInspectV2         = "host.inspect.v2"
	CapabilityDoctorV1          = "host.doctor.v1"
	CapabilityConfigureV1       = "host.configure.v1"
	CapabilityWorktreeListV1    = "worktree.list.v1"
	CapabilityWorktreeInspectV1 = "worktree.inspect.v1"
)

var (
	identityPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	capabilityPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:\.[a-z0-9]+)*\.v[1-9][0-9]*$`)
	platformPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

type Code string

const (
	CodeInvalidMessage        Code = "invalid_message"
	CodeInvalidIdentity       Code = "invalid_identity"
	CodeUnsupportedProtocol   Code = "unsupported_protocol"
	CodeCapabilityUnavailable Code = "capability_unavailable"
	CodeNotFound              Code = "not_found"
	CodeInvalidInput          Code = "invalid_input"
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

type OperationErrorDetail struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
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
	result := []string{CapabilityConfigureV1, CapabilityDoctorV1, CapabilityHelloV1, CapabilityInspectV2, CapabilityWorktreeInspectV1, CapabilityWorktreeListV1}
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

func NewOperationError(boxIdentity string, code Code, message string) OperationError {
	return OperationError{
		SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: boxIdentity,
		Error: OperationErrorDetail{Code: code, Message: message},
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
	if (result.Error.Code != CodeNotFound && result.Error.Code != CodeInvalidInput) || result.Error.Message == "" || strings.ContainsAny(result.Error.Message, "\x00\r\n") {
		return OperationError{}, true, &Error{Code: CodeInvalidMessage, Message: "host operation error is invalid"}
	}
	return result, true, nil
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
