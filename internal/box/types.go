// Package box implements Schooner's box lifecycle workflows.
package box

import (
	"context"
	"time"
)

const DefaultProjectRoot = "~/schooner"

type Step string

const (
	StepResolve       Step = "resolve"
	StepConnect       Step = "connect"
	StepInspect       Step = "inspect"
	StepIdentity      Step = "identity"
	StepPrerequisites Step = "prerequisites"
	StepProjectRoot   Step = "project_root"
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
	AcceptNewHostKey bool
	BatchMode        bool
}

type Tool struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
}

type Capabilities struct {
	OSID              string `json:"os_id"`
	OSVersion         string `json:"os_version"`
	Architecture      string `json:"architecture"`
	Home              string `json:"home"`
	RemoteIdentity    string `json:"remote_identity,omitempty"`
	ProjectRoot       string `json:"project_root,omitempty"`
	ProjectRootExists bool   `json:"project_root_exists"`
	Git               Tool   `json:"git"`
	Tmux              Tool   `json:"tmux"`
	PasswordlessSudo  bool   `json:"passwordless_sudo"`
}

type Record struct {
	ID             string
	Name           string
	Acquisition    string
	SSHDestination string
	RemoteIdentity string
	ProjectRoot    string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Observation struct {
	BoxID        string
	ObservedAt   time.Time
	Capabilities Capabilities
}

type AddOperation struct {
	Name           string
	SSHDestination string
	ProjectRoot    string
	Checkpoint     Step
	RemoteIdentity string
	UpdatedAt      time.Time
}

type AddRequest struct {
	Name             string
	SSHDestination   string
	ProjectRoot      string
	AcceptNewHostKey bool
	BatchMode        bool
	Progress         Progress
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
	EnsureProjectRoot(context.Context, Connection, string) (string, error)
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
