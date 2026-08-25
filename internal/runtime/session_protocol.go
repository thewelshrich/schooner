package runtime

import (
	"fmt"

	"github.com/thewelshrich/schooner/internal/session"
)

type SessionListRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
}

type SessionStartRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
	Worktree        string `json:"worktree"`
}

type SessionTargetRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
	Selector        string `json:"selector"`
}

type SessionLogsRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
	SessionID       string `json:"session_id"`
	Lines           int    `json:"lines"`
}

type WorktreeShellRequest struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
	Worktree        string `json:"worktree"`
}

type SessionCatalog struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	session.Catalog
}

type SessionStartResult struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
	session.StartResult
}

type SessionLogsResult struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
	session.LogsResult
}

type SessionStopResult struct {
	SchemaVersion   string `json:"schema_version"`
	ProtocolVersion string `json:"protocol_version"`
	BoxIdentity     string `json:"box_identity"`
	WorktreeRoot    string `json:"worktree_root"`
	session.StopResult
}

func NewSessionListRequest(root, identity string) SessionListRequest {
	return SessionListRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: identity, WorktreeRoot: root}
}

func NewSessionStartRequest(root, identity, worktree string) SessionStartRequest {
	return SessionStartRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: identity, WorktreeRoot: root, Worktree: worktree}
}

func NewSessionTargetRequest(root, identity, selector string) SessionTargetRequest {
	return SessionTargetRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: identity, WorktreeRoot: root, Selector: selector}
}

func NewSessionLogsRequest(root, identity, id string, lines int) SessionLogsRequest {
	return SessionLogsRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: identity, WorktreeRoot: root, SessionID: id, Lines: lines}
}

func NewWorktreeShellRequest(root, identity, worktree string) WorktreeShellRequest {
	return WorktreeShellRequest{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: identity, WorktreeRoot: root, Worktree: worktree}
}

func ValidateSessionListRequest(request SessionListRequest) error {
	return validateSessionEnvelope(request.SchemaVersion, request.ProtocolVersion, request.BoxIdentity, request.WorktreeRoot)
}

func ValidateSessionStartRequest(request SessionStartRequest) error {
	if err := validateSessionEnvelope(request.SchemaVersion, request.ProtocolVersion, request.BoxIdentity, request.WorktreeRoot); err != nil {
		return err
	}
	return validateSessionSelector(request.Worktree, "Session start Worktree")
}

func ValidateSessionTargetRequest(request SessionTargetRequest) error {
	if err := validateSessionEnvelope(request.SchemaVersion, request.ProtocolVersion, request.BoxIdentity, request.WorktreeRoot); err != nil {
		return err
	}
	return validateSessionSelector(request.Selector, "Session selector")
}

func ValidateSessionLogsRequest(request SessionLogsRequest) error {
	if err := validateSessionEnvelope(request.SchemaVersion, request.ProtocolVersion, request.BoxIdentity, request.WorktreeRoot); err != nil {
		return err
	}
	if err := validateSessionSelector(request.SessionID, "Session ID"); err != nil {
		return err
	}
	if request.Lines < 1 || request.Lines > session.MaxLogLines {
		return &Error{Code: CodeInvalidInput, Message: fmt.Sprintf("Session log lines must be between 1 and %d", session.MaxLogLines)}
	}
	return nil
}

func ValidateWorktreeShellRequest(request WorktreeShellRequest) error {
	if err := validateSessionEnvelope(request.SchemaVersion, request.ProtocolVersion, request.BoxIdentity, request.WorktreeRoot); err != nil {
		return err
	}
	return validateSessionSelector(request.Worktree, "Worktree shell selector")
}

func validateSessionEnvelope(schema, protocol, identity, root string) error {
	if schema != SchemaVersion || protocol != ProtocolVersion {
		return &Error{Code: CodeUnsupportedProtocol, Message: "Session request is incompatible"}
	}
	if !identityPattern.MatchString(identity) {
		return &Error{Code: CodeInvalidIdentity, Message: "Session request Box identity is invalid"}
	}
	if !validPath(root) {
		return &Error{Code: CodeInvalidInput, Message: "Session request Worktree Root is invalid"}
	}
	return nil
}

func validateSessionSelector(value, label string) error {
	if value == "" || len(value) > 4096 || hasControl(value) {
		return &Error{Code: CodeInvalidInput, Message: label + " is invalid"}
	}
	return nil
}
