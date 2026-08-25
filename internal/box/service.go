package box

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	hostprotocol "github.com/thewelshrich/schooner/internal/runtime"
)

type Service struct {
	runtime Runtime
	store   Inventory
	now     func() time.Time
	newID   func() (string, error)
}

func New(runtime Runtime, store Inventory) *Service {
	return &Service{runtime: runtime, store: store, now: time.Now, newID: randomID}
}

func (s *Service) Add(ctx context.Context, req AddRequest) (AddResult, error) {
	if err := ValidateName(req.Name); err != nil {
		return AddResult{}, invalid(err)
	}
	if err := ValidateSSHDestination(req.SSHDestination); err != nil {
		return AddResult{}, invalid(err)
	}
	if req.WorktreeRoot == "" {
		req.WorktreeRoot = DefaultWorktreeRoot
	}
	if err := ValidateWorktreeRoot(req.WorktreeRoot); err != nil {
		return AddResult{}, invalid(err)
	}
	if _, err := s.store.FindByName(ctx, req.Name); err == nil {
		return AddResult{}, &Error{Code: "conflict", Message: fmt.Sprintf("box name %q is already in use", req.Name)}
	} else if !IsNotFound(err) {
		return AddResult{}, err
	}

	conn := Connection{Destination: req.SSHDestination, IdentityFile: req.IdentityFile, AcceptNewHostKey: req.AcceptNewHostKey, BatchMode: req.BatchMode}
	op := AddOperation{Name: req.Name, SSHDestination: req.SSHDestination, WorktreeRoot: req.WorktreeRoot, UpdatedAt: s.now().UTC()}
	if err := s.store.BeginAdd(ctx, op); err != nil {
		return AddResult{}, err
	}

	if err := s.runStep(ctx, req.Progress, StepResolve, "Resolve SSH destination", func() error { return s.runtime.Resolve(ctx, conn) }); err != nil {
		return AddResult{}, err
	}
	var capabilities Capabilities
	if err := s.runStep(ctx, req.Progress, StepConnect, "Connect and verify host trust", func() error {
		var err error
		capabilities, err = s.runtime.Inspect(ctx, conn, req.WorktreeRoot)
		return err
	}); err != nil {
		return AddResult{}, err
	}
	if err := s.runStep(ctx, req.Progress, StepInspect, "Inspect Ubuntu capabilities", func() error { return certify(capabilities) }); err != nil {
		return AddResult{}, err
	}

	identity := capabilities.RemoteIdentity
	if identity != "" {
		if existing, err := s.store.FindByRemoteIdentity(ctx, identity); err == nil {
			return AddResult{}, &Error{Code: "conflict", Message: fmt.Sprintf("this machine is already registered as %q", existing.Name), Context: map[string]string{"box": existing.Name}}
		} else if !IsNotFound(err) {
			return AddResult{}, err
		}
	}
	if err := s.runStep(ctx, req.Progress, StepIdentity, "Establish stable box identity", func() error {
		var err error
		candidate := identity
		if candidate == "" {
			candidate, err = s.newID()
			if err != nil {
				return err
			}
		}
		identity, err = s.runtime.EnsureIdentity(ctx, conn, candidate)
		return err
	}); err != nil {
		return AddResult{}, err
	}
	op.RemoteIdentity, op.Checkpoint, op.UpdatedAt = identity, StepIdentity, s.now().UTC()
	if err := s.store.CheckpointAdd(ctx, op); err != nil {
		return AddResult{}, err
	}

	prepared, err := s.prepareHost(ctx, hostPreparationRequest{
		Connection:      conn,
		Identity:        identity,
		WorktreeRoot:    req.WorktreeRoot,
		Capabilities:    capabilities,
		Mode:            HostRepair,
		Prerequisites:   true,
		Progress:        req.Progress,
		IdentityChanged: "remote box identity changed during setup",
	})
	if err != nil {
		return AddResult{}, err
	}
	capabilities = prepared.Capabilities

	now := s.now().UTC()
	recordID, err := s.newID()
	if err != nil {
		return AddResult{}, err
	}
	acquisition := req.Acquisition
	if acquisition == "" {
		acquisition = "adopted"
	}
	record := Record{ID: recordID, Name: req.Name, Acquisition: acquisition, SSHDestination: req.SSHDestination, IdentityFile: req.IdentityFile, RemoteIdentity: identity, RuntimePath: prepared.RuntimePath, WorktreeRoot: prepared.WorktreeRoot, Provider: req.Provider, ProviderResourceID: req.ProviderResourceID, ProviderCorrelationID: req.ProviderCorrelationID, CredentialProfile: req.CredentialProfile, ProviderRegion: req.ProviderRegion, CreatedAt: now, UpdatedAt: now}
	observation := Observation{BoxID: record.ID, ObservedAt: now, Capabilities: capabilities}
	if err := s.runStep(ctx, req.Progress, StepSave, "Save local inventory", func() error { return s.store.CompleteAdd(ctx, op, record, observation) }); err != nil {
		return AddResult{}, err
	}
	verified := []string{"git", "schooner", "tmux"}
	return AddResult{Box: record, Capabilities: capabilities, Installed: prepared.Installed, Verified: verified}, nil
}

func (s *Service) Status(ctx context.Context, req StatusRequest) (StatusResult, error) {
	if err := ValidateName(req.Name); err != nil {
		return StatusResult{}, invalid(err)
	}
	record, err := s.store.FindByName(ctx, req.Name)
	if err != nil {
		return StatusResult{}, err
	}
	conn := Connection{Destination: record.SSHDestination, IdentityFile: record.IdentityFile, BatchMode: req.BatchMode}
	var capabilities Capabilities
	err = s.runStep(ctx, req.Progress, StepConnect, "Check live box status", func() error {
		if record.RuntimePath == "" {
			return NewError("host_runtime_missing", fmt.Sprintf("the box does not have a recorded host runtime; run \"schooner box setup %s\"", record.Name), nil)
		}
		var inspectErr error
		capabilities, inspectErr = s.runtime.InspectHost(ctx, conn, HostRuntime{Path: record.RuntimePath}, record.WorktreeRoot, record.RemoteIdentity)
		if ErrorCode(inspectErr) == "host_runtime_missing" {
			return NewError("host_runtime_missing", fmt.Sprintf("the host runtime is missing; run \"schooner box setup %s\"", record.Name), inspectErr)
		}
		if ErrorCode(inspectErr) == "host_runtime_incompatible" {
			return NewError("host_runtime_incompatible", fmt.Sprintf("the host runtime is incompatible; run \"schooner box update %s\"", record.Name), inspectErr)
		}
		return inspectErr
	})
	if err != nil {
		if last, lastErr := s.store.LastObservation(ctx, record.ID); lastErr == nil {
			var target *Error
			if e, ok := err.(*Error); ok {
				target = e
			} else {
				target = NewError("connection_failed", err.Error(), err)
			}
			if target.Context == nil {
				target.Context = map[string]string{}
			}
			target.Context["box"] = record.Name
			target.Context["last_observed_at"] = last.ObservedAt.Format(time.RFC3339)
			target.Context["last_os"] = last.Capabilities.OSID + " " + last.Capabilities.OSVersion
			target.Context["last_architecture"] = last.Capabilities.Architecture
			target.Context["last_git"] = last.Capabilities.Git.Version
			target.Context["last_tmux"] = last.Capabilities.Tmux.Version
			return StatusResult{}, target
		}
		return StatusResult{}, err
	}
	if capabilities.RemoteIdentity != record.RemoteIdentity {
		return StatusResult{}, &Error{Code: "conflict", Message: "the connected machine does not match the recorded box identity"}
	}
	if capabilities.WorktreeRoot != record.WorktreeRoot {
		return StatusResult{}, NewError("conflict", fmt.Sprintf("host worktree root differs from local inventory; run \"schooner box setup %s\"", record.Name), nil)
	}
	if err := certify(capabilities); err != nil {
		return StatusResult{}, err
	}
	observation := Observation{BoxID: record.ID, ObservedAt: s.now().UTC(), Capabilities: capabilities}
	if err := s.store.SaveObservation(ctx, observation); err != nil {
		return StatusResult{}, err
	}
	return StatusResult{Box: record, Observation: observation}, nil
}

func (s *Service) Setup(ctx context.Context, req SetupRequest) (SetupResult, error) {
	result, err := s.maintain(ctx, maintenanceRequest{Name: req.Name, BatchMode: req.BatchMode, Progress: req.Progress, Mode: HostRepair, Prerequisites: true})
	if err != nil {
		return SetupResult{}, err
	}
	return SetupResult{Box: result.Box, Capabilities: result.Capabilities, Host: result.Host, Installed: result.Installed}, nil
}

func (s *Service) Update(ctx context.Context, req UpdateRequest) (UpdateResult, error) {
	result, err := s.maintain(ctx, maintenanceRequest{Name: req.Name, BatchMode: req.BatchMode, Progress: req.Progress, Mode: HostUpdate})
	if err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Box: result.Box, Capabilities: result.Capabilities, Host: result.Host}, nil
}

type maintenanceRequest struct {
	Name          string
	BatchMode     bool
	Progress      Progress
	Mode          HostInstallMode
	Prerequisites bool
}

type maintenanceResult struct {
	Box          Record
	Capabilities Capabilities
	Host         HostInstallResult
	Installed    []string
}

func (s *Service) maintain(ctx context.Context, req maintenanceRequest) (maintenanceResult, error) {
	if err := ValidateName(req.Name); err != nil {
		return maintenanceResult{}, invalid(err)
	}
	record, err := s.store.FindByName(ctx, req.Name)
	if err != nil {
		return maintenanceResult{}, err
	}
	connection := Connection{Destination: record.SSHDestination, IdentityFile: record.IdentityFile, BatchMode: req.BatchMode}
	if err = s.runStep(ctx, req.Progress, StepResolve, "Resolve SSH destination", func() error { return s.runtime.Resolve(ctx, connection) }); err != nil {
		return maintenanceResult{}, err
	}
	var capabilities Capabilities
	if err = s.runStep(ctx, req.Progress, StepInspect, "Inspect and verify the recorded box", func() error {
		var inspectErr error
		capabilities, inspectErr = s.runtime.Inspect(ctx, connection, record.WorktreeRoot)
		if inspectErr != nil {
			return inspectErr
		}
		if inspectErr = certify(capabilities); inspectErr != nil {
			return inspectErr
		}
		if capabilities.RemoteIdentity == "" || capabilities.RemoteIdentity != record.RemoteIdentity {
			return &Error{Code: "conflict", Message: "the connected machine does not match the recorded box identity"}
		}
		return nil
	}); err != nil {
		return maintenanceResult{}, err
	}
	runtimePath := ""
	if req.Mode == HostUpdate {
		runtimePath = record.RuntimePath
	}
	prepared, err := s.prepareHost(ctx, hostPreparationRequest{
		Connection:      connection,
		Identity:        record.RemoteIdentity,
		RuntimePath:     runtimePath,
		WorktreeRoot:    record.WorktreeRoot,
		Capabilities:    capabilities,
		Mode:            req.Mode,
		Prerequisites:   req.Prerequisites,
		Progress:        req.Progress,
		IdentityChanged: "the connected machine does not match the recorded box identity",
	})
	if err != nil {
		return maintenanceResult{}, err
	}
	if record.RuntimePath != prepared.RuntimePath {
		if err = s.store.UpdateRuntimePath(ctx, record.ID, prepared.RuntimePath); err != nil {
			return maintenanceResult{}, err
		}
		record.RuntimePath = prepared.RuntimePath
	}
	observation := Observation{BoxID: record.ID, ObservedAt: s.now().UTC(), Capabilities: prepared.Capabilities}
	if err = s.store.SaveObservation(ctx, observation); err != nil {
		return maintenanceResult{}, err
	}
	return maintenanceResult{Box: record, Capabilities: prepared.Capabilities, Host: prepared.Host, Installed: prepared.Installed}, nil
}

type hostPreparationRequest struct {
	Connection      Connection
	Identity        string
	RuntimePath     string
	WorktreeRoot    string
	Capabilities    Capabilities
	Mode            HostInstallMode
	Prerequisites   bool
	Progress        Progress
	IdentityChanged string
}

type hostPreparationResult struct {
	RuntimePath  string
	WorktreeRoot string
	Capabilities Capabilities
	Host         HostInstallResult
	Installed    []string
}

// prepareHost owns the mutating host-convergence sequence shared by initial
// adoption and explicit maintenance. Callers must inspect and authenticate the
// remote identity before entering this method.
func (s *Service) prepareHost(ctx context.Context, req hostPreparationRequest) (hostPreparationResult, error) {
	runtimePath := req.RuntimePath
	var err error
	if runtimePath == "" {
		runtimePath, err = hostprotocol.InstallPath(req.Capabilities.Home)
		if err != nil {
			return hostPreparationResult{}, NewError("unsupported", err.Error(), err)
		}
	}

	var host HostInstallResult
	if err = s.runStep(ctx, req.Progress, StepRuntime, "Install or verify the Schooner host runtime", func() error {
		var hostErr error
		host, hostErr = s.runtime.EnsureHost(ctx, req.Connection, HostInstallRequest{
			Path:             runtimePath,
			OS:               "linux",
			Architecture:     req.Capabilities.Architecture,
			ExpectedIdentity: req.Identity,
			Mode:             req.Mode,
		})
		return hostErr
	}); err != nil {
		return hostPreparationResult{}, err
	}

	installed := []string(nil)
	worktreeRoot := req.WorktreeRoot
	if req.Prerequisites {
		installed = missingTools(req.Capabilities)
		if err = s.runStep(ctx, req.Progress, StepPrerequisites, "Install or verify Git and tmux", func() error {
			if len(installed) == 0 {
				return nil
			}
			if !req.Capabilities.PasswordlessSudo {
				return &Error{Code: "permission_denied", Message: "Git or tmux is missing and passwordless sudo is unavailable; install the missing packages or enable sudo -n, then retry"}
			}
			return s.runtime.InstallTools(ctx, req.Connection, installed)
		}); err != nil {
			return hostPreparationResult{}, err
		}
	}
	if err = s.runStep(ctx, req.Progress, StepWorktreeRoot, "Prepare worktree root", func() error {
		var rootErr error
		worktreeRoot, rootErr = s.runtime.EnsureWorktreeRoot(ctx, req.Connection, req.WorktreeRoot)
		return rootErr
	}); err != nil {
		return hostPreparationResult{}, err
	}
	if err = s.runStep(ctx, req.Progress, StepConfigure, "Configure host worktree root", func() error {
		return s.runtime.ConfigureHost(ctx, req.Connection, host.Runtime, worktreeRoot, req.Identity)
	}); err != nil {
		return hostPreparationResult{}, err
	}

	capabilities := req.Capabilities
	if err = s.runStep(ctx, req.Progress, StepVerify, "Verify the Schooner host runtime", func() error {
		var verifyErr error
		capabilities, verifyErr = s.runtime.InspectHost(ctx, req.Connection, host.Runtime, worktreeRoot, req.Identity)
		if verifyErr != nil {
			return verifyErr
		}
		if verifyErr = certify(capabilities); verifyErr != nil {
			return verifyErr
		}
		if capabilities.RemoteIdentity != req.Identity {
			return &Error{Code: "conflict", Message: req.IdentityChanged}
		}
		if req.Prerequisites && (!capabilities.Git.Available || !capabilities.Tmux.Available) {
			return &Error{Code: "unsupported", Message: "Git and tmux are required but were not available after setup"}
		}
		if !capabilities.WorktreeRootExists || capabilities.WorktreeRoot != worktreeRoot {
			return &Error{Code: "unsupported", Message: "worktree root was not available after setup"}
		}
		return nil
	}); err != nil {
		return hostPreparationResult{}, err
	}

	return hostPreparationResult{
		RuntimePath:  runtimePath,
		WorktreeRoot: worktreeRoot,
		Capabilities: capabilities,
		Host:         host,
		Installed:    installed,
	}, nil
}

// PrepareSSH loads the authoritative local connection inputs for an
// interactive system-OpenSSH handoff. It deliberately performs no live probe:
// OpenSSH owns authentication and host trust for the resulting connection.
func (s *Service) PrepareSSH(ctx context.Context, req SSHRequest) (SSHLaunch, error) {
	if err := ValidateName(req.Name); err != nil {
		return SSHLaunch{}, invalid(err)
	}
	record, err := s.store.FindByName(ctx, req.Name)
	if err != nil {
		return SSHLaunch{}, err
	}
	return SSHLaunch{
		Connection: Connection{
			Destination:  record.SSHDestination,
			IdentityFile: record.IdentityFile,
			BatchMode:    req.BatchMode,
		},
	}, nil
}

func (s *Service) Remove(ctx context.Context, name string) (RemoveResult, error) {
	if err := ValidateName(name); err != nil {
		return RemoveResult{}, invalid(err)
	}
	record, err := s.store.Remove(ctx, name)
	if err != nil {
		return RemoveResult{}, err
	}
	return RemoveResult{Box: record, RemoteUnchanged: true}, nil
}

func (s *Service) List(ctx context.Context) ([]Record, error) { return s.store.List(ctx) }

func (s *Service) ListEntries(ctx context.Context) ([]ListEntry, error) {
	records, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]ListEntry, 0, len(records))
	for _, record := range records {
		entry := ListEntry{Box: record}
		observation, obsErr := s.store.LastObservation(ctx, record.ID)
		if obsErr == nil {
			entry.HasObservation = true
			entry.Reachable = true
			entry.LastObservedAt = observation.ObservedAt
		} else if !IsNotFound(obsErr) {
			return nil, obsErr
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *Service) Get(ctx context.Context, name string) (Record, error) {
	if err := ValidateName(name); err != nil {
		return Record{}, invalid(err)
	}
	return s.store.FindByName(ctx, name)
}

func (s *Service) runStep(ctx context.Context, progress Progress, step Step, message string, action func() error) error {
	if progress != nil {
		progress(Event{Step: step, State: EventStarted, Message: message})
	}
	err := action()
	if err != nil {
		if progress != nil {
			progress(Event{Step: step, State: EventFailed, Message: message})
		}
		return err
	}
	if progress != nil {
		progress(Event{Step: step, State: EventCompleted, Message: message})
	}
	return nil
}

func certify(c Capabilities) error {
	if c.OSID != "ubuntu" || (c.OSVersion != "24.04" && c.OSVersion != "26.04") {
		return &Error{Code: "unsupported", Message: fmt.Sprintf("unsupported remote operating system %s %s; expected Ubuntu 24.04 or 26.04", c.OSID, c.OSVersion)}
	}
	if c.Architecture != "amd64" && c.Architecture != "arm64" {
		return &Error{Code: "unsupported", Message: fmt.Sprintf("unsupported remote architecture %s; expected amd64 or arm64", c.Architecture)}
	}
	return nil
}

func missingTools(c Capabilities) []string {
	var result []string
	if !c.Git.Available {
		result = append(result, "git")
	}
	if !c.Tmux.Available {
		result = append(result, "tmux")
	}
	sort.Strings(result)
	return result
}

func invalid(err error) error { return &Error{Code: "invalid_input", Message: err.Error(), Cause: err} }

func randomID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", &Error{Code: "internal", Message: "generate random identifier", Cause: err}
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	buf := make([]byte, 36)
	hex.Encode(buf[0:8], data[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], data[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], data[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], data[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], data[10:16])
	return string(buf), nil
}
