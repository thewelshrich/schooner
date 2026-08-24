package box

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"time"
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
	if req.ProjectRoot == "" {
		req.ProjectRoot = DefaultProjectRoot
	}
	if err := ValidateProjectRoot(req.ProjectRoot); err != nil {
		return AddResult{}, invalid(err)
	}
	if _, err := s.store.FindByName(ctx, req.Name); err == nil {
		return AddResult{}, &Error{Code: "conflict", Message: fmt.Sprintf("box name %q is already in use", req.Name)}
	} else if !IsNotFound(err) {
		return AddResult{}, err
	}

	conn := Connection{Destination: req.SSHDestination, IdentityFile: req.IdentityFile, AcceptNewHostKey: req.AcceptNewHostKey, BatchMode: req.BatchMode}
	op := AddOperation{Name: req.Name, SSHDestination: req.SSHDestination, ProjectRoot: req.ProjectRoot, UpdatedAt: s.now().UTC()}
	if err := s.store.BeginAdd(ctx, op); err != nil {
		return AddResult{}, err
	}

	if err := s.runStep(ctx, req.Progress, StepResolve, "Resolve SSH destination", func() error { return s.runtime.Resolve(ctx, conn) }); err != nil {
		return AddResult{}, err
	}
	var capabilities Capabilities
	if err := s.runStep(ctx, req.Progress, StepConnect, "Connect and verify host trust", func() error {
		var err error
		capabilities, err = s.runtime.Inspect(ctx, conn, req.ProjectRoot)
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

	missing := missingTools(capabilities)
	if err := s.runStep(ctx, req.Progress, StepPrerequisites, "Install or verify Git and tmux", func() error {
		if len(missing) == 0 {
			return nil
		}
		if !capabilities.PasswordlessSudo {
			return &Error{Code: "permission_denied", Message: "Git or tmux is missing and passwordless sudo is unavailable; install the missing packages or enable sudo -n, then retry"}
		}
		return s.runtime.InstallTools(ctx, conn, missing)
	}); err != nil {
		return AddResult{}, err
	}

	var projectRoot string
	if err := s.runStep(ctx, req.Progress, StepProjectRoot, "Prepare project root", func() error {
		var err error
		projectRoot, err = s.runtime.EnsureProjectRoot(ctx, conn, req.ProjectRoot)
		return err
	}); err != nil {
		return AddResult{}, err
	}

	if err := s.runStep(ctx, req.Progress, StepVerify, "Verify box readiness", func() error {
		var err error
		capabilities, err = s.runtime.Inspect(ctx, conn, projectRoot)
		if err != nil {
			return err
		}
		if err = certify(capabilities); err != nil {
			return err
		}
		if capabilities.RemoteIdentity != identity {
			return &Error{Code: "conflict", Message: "remote box identity changed during setup"}
		}
		if !capabilities.Git.Available || !capabilities.Tmux.Available {
			return &Error{Code: "unsupported", Message: "Git and tmux are required but were not available after setup"}
		}
		if !capabilities.ProjectRootExists {
			return &Error{Code: "unsupported", Message: "project root was not available after setup"}
		}
		return nil
	}); err != nil {
		return AddResult{}, err
	}

	now := s.now().UTC()
	recordID, err := s.newID()
	if err != nil {
		return AddResult{}, err
	}
	acquisition := req.Acquisition
	if acquisition == "" {
		acquisition = "adopted"
	}
	record := Record{ID: recordID, Name: req.Name, Acquisition: acquisition, SSHDestination: req.SSHDestination, IdentityFile: req.IdentityFile, RemoteIdentity: identity, ProjectRoot: projectRoot, Provider: req.Provider, ProviderResourceID: req.ProviderResourceID, ProviderCorrelationID: req.ProviderCorrelationID, CredentialProfile: req.CredentialProfile, ProviderRegion: req.ProviderRegion, CreatedAt: now, UpdatedAt: now}
	observation := Observation{BoxID: record.ID, ObservedAt: now, Capabilities: capabilities}
	if err := s.runStep(ctx, req.Progress, StepSave, "Save local inventory", func() error { return s.store.CompleteAdd(ctx, op, record, observation) }); err != nil {
		return AddResult{}, err
	}
	verified := []string{"git", "tmux"}
	return AddResult{Box: record, Capabilities: capabilities, Installed: missing, Verified: verified}, nil
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
		var inspectErr error
		capabilities, inspectErr = s.runtime.Inspect(ctx, conn, record.ProjectRoot)
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
	if err := certify(capabilities); err != nil {
		return StatusResult{}, err
	}
	observation := Observation{BoxID: record.ID, ObservedAt: s.now().UTC(), Capabilities: capabilities}
	if err := s.store.SaveObservation(ctx, observation); err != nil {
		return StatusResult{}, err
	}
	return StatusResult{Box: record, Observation: observation}, nil
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
