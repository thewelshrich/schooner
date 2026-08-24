// Package box implements Schooner's box lifecycle workflows.
package box

import (
	"context"
	"time"
)

const DefaultWorkspaceRoot = "~/schooner"

type Step string

const (
	StepProvision     Step = "provision"
	StepWaitSSH       Step = "wait_ssh"
	StepResolve       Step = "resolve"
	StepConnect       Step = "connect"
	StepInspect       Step = "inspect"
	StepIdentity      Step = "identity"
	StepPrerequisites Step = "prerequisites"
	StepWorkspaceRoot Step = "workspace_root"
	StepVerify        Step = "verify"
	StepSave          Step = "save"
)

type EventState string

const (
	EventStarted   EventState = "started"
	EventCompleted EventState = "completed"
	EventFailed    EventState = "failed"
)

type Event struct {
	Step    Step
	State   EventState
	Message string
}

type Progress func(Event)

type Connection struct {
	Destination      string
	IdentityFile     string
	AcceptNewHostKey bool
	BatchMode        bool
}

type Tool struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
}

type Capabilities struct {
	OSID                string `json:"os_id"`
	OSVersion           string `json:"os_version"`
	Architecture        string `json:"architecture"`
	Home                string `json:"home"`
	RemoteIdentity      string `json:"remote_identity,omitempty"`
	WorkspaceRoot       string `json:"workspace_root,omitempty"`
	WorkspaceRootExists bool   `json:"workspace_root_exists"`
	Git                 Tool   `json:"git"`
	Tmux                Tool   `json:"tmux"`
	PasswordlessSudo    bool   `json:"passwordless_sudo"`
}

type Record struct {
	ID                    string
	Name                  string
	Acquisition           string
	SSHDestination        string
	IdentityFile          string
	RemoteIdentity        string
	WorkspaceRoot         string
	Provider              string
	ProviderResourceID    string
	ProviderCorrelationID string
	CredentialProfile     string
	ProviderRegion        string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type Observation struct {
	BoxID        string
	ObservedAt   time.Time
	Capabilities Capabilities
}

type AddOperation struct {
	Name           string
	SSHDestination string
	WorkspaceRoot  string
	Checkpoint     Step
	RemoteIdentity string
	UpdatedAt      time.Time
}

type AddRequest struct {
	Name                  string
	SSHDestination        string
	IdentityFile          string
	WorkspaceRoot         string
	Acquisition           string
	Provider              string
	ProviderResourceID    string
	ProviderCorrelationID string
	CredentialProfile     string
	ProviderRegion        string
	AcceptNewHostKey      bool
	BatchMode             bool
	Progress              Progress
}

// ListEntry is a local inventory row with the latest cached observation.
// Reachable is last-known only: true when Schooner has a successful observation
// on record. Live reachability remains box status.
type ListEntry struct {
	Box            Record
	Reachable      bool
	LastObservedAt time.Time
	HasObservation bool
}

type AddResult struct {
	Box          Record
	Capabilities Capabilities
	Installed    []string
	Verified     []string
}

type StatusResult struct {
	Box         Record
	Observation Observation
}

type StatusRequest struct {
	Name      string
	BatchMode bool
	Progress  Progress
}

// SSHRequest identifies a recorded box for an interactive OpenSSH handoff.
type SSHRequest struct {
	Name      string
	BatchMode bool
}

// SSHLaunch contains only the trusted connection inputs recorded for a box.
// Interactive process execution remains the responsibility of the OpenSSH
// adapter and cannot carry a remote command.
type SSHLaunch struct {
	Connection Connection
}

type RemoveResult struct {
	Box             Record
	RemoteUnchanged bool
}

// Runtime is the consumer-owned seam for bounded remote box operations.
type Runtime interface {
	Resolve(context.Context, Connection) error
	Inspect(context.Context, Connection, string) (Capabilities, error)
	EnsureIdentity(context.Context, Connection, string) (string, error)
	InstallTools(context.Context, Connection, []string) error
	EnsureWorkspaceRoot(context.Context, Connection, string) (string, error)
}

// Inventory is the consumer-owned seam for durable local box state.
type Inventory interface {
	FindByName(context.Context, string) (Record, error)
	FindByRemoteIdentity(context.Context, string) (Record, error)
	List(context.Context) ([]Record, error)
	BeginAdd(context.Context, AddOperation) error
	CheckpointAdd(context.Context, AddOperation) error
	CompleteAdd(context.Context, AddOperation, Record, Observation) error
	SaveObservation(context.Context, Observation) error
	LastObservation(context.Context, string) (Observation, error)
	Remove(context.Context, string) (Record, error)
}
