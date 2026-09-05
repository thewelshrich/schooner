// Package acquisition obtains infrastructure and converges it on the shared
// box preparation workflow.
package acquisition

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/credentials"
	"github.com/thewelshrich/schooner/internal/provider"
)

type ProvisionOperation struct {
	Name             string
	CorrelationID    string
	Profile          provider.CredentialProfileRef
	Region           string
	Size             string
	Image            string
	NetworkID        string
	AccessKeyIDs     []string
	LocalPublicKeys  []provider.PublicKey
	AutomaticBackups bool
	IPv6             bool
	WorktreeRoot     string
	ResourceID       string
	SSHDestination   string
	IdentityFile     string
	Checkpoint       string
	Warning          string
	UpdatedAt        time.Time
}

type DestroyOperation struct {
	BoxID      string
	Name       string
	Resource   provider.ResourceRef
	Checkpoint string
	UpdatedAt  time.Time
}

type Store interface {
	FindProvision(context.Context, string) (ProvisionOperation, error)
	BeginProvision(context.Context, ProvisionOperation) (ProvisionOperation, error)
	CheckpointProvision(context.Context, ProvisionOperation) error
	CompleteProvision(context.Context, ProvisionOperation, string) error
	BeginDestroy(context.Context, DestroyOperation) error
	CheckpointDestroy(context.Context, DestroyOperation) error
	CompleteDestroy(context.Context, string) error
	FindByName(context.Context, string) (box.Record, error)
	LastObservation(context.Context, string) (box.Observation, error)
}

type CredentialResolver interface {
	Resolve(context.Context, provider.CredentialProfileRef) (credentials.Credential, error)
}

type Identity struct {
	PublicKey  string
	PrivateKey string
}

type IdentitySource interface {
	Ensure(context.Context) (Identity, error)
}

type ConnectionWaiter interface {
	WaitReady(context.Context, box.Connection) error
}

type ProvisionRequest struct {
	Name             string
	Profile          provider.CredentialProfileRef
	Region           string
	Size             string
	Image            string
	NetworkID        string
	AccessKeyIDs     []string
	LocalPublicKeys  []provider.PublicKey
	AutomaticBackups bool
	IPv6             bool
	WorktreeRoot     string
	AcceptNewHostKey bool
	BatchMode        bool
	Progress         box.Progress
}

type ProvisionResult struct {
	box.AddResult
	Resource provider.ResourceRef
	Warning  string
}

type DestroyResult struct {
	Box          box.Record
	Resource     provider.ResourceRef
	LocalRemoved bool
}

type Service struct {
	boxes       *box.Service
	store       Store
	credentials CredentialResolver
	cloud       provider.Cloud
	identity    IdentitySource
	waiter      ConnectionWaiter
	now         func() time.Time
	newID       func() (string, error)
}

func New(boxes *box.Service, store Store, resolver CredentialResolver, cloud provider.Cloud, identity IdentitySource, waiter ConnectionWaiter) *Service {
	return &Service{boxes: boxes, store: store, credentials: resolver, cloud: cloud, identity: identity, waiter: waiter, now: time.Now, newID: randomID}
}

func (s *Service) Catalog(ctx context.Context, profile provider.CredentialProfileRef) (provider.Catalog, provider.CredentialProfileRef, error) {
	credential, err := s.credentials.Resolve(ctx, profile)
	if err != nil {
		return provider.Catalog{}, "", err
	}
	catalog, err := s.cloud.Catalog(ctx, credential.Token)
	if err == nil && (len(catalog.Regions) == 0 || len(catalog.Sizes) == 0 || len(catalog.Images) == 0) {
		err = box.NewError("unsupported", "DigitalOcean returned no compatible Ubuntu Droplet options", nil)
	}
	return catalog, credential.Profile, err
}

// InterruptedProvision returns a locally recorded DigitalOcean add that has not
// completed yet. Callers can resume it with the same name and no other selections.
func (s *Service) InterruptedProvision(ctx context.Context, name string) (ProvisionOperation, error) {
	if err := box.ValidateName(name); err != nil {
		return ProvisionOperation{}, box.NewError("invalid_input", err.Error(), err)
	}
	return s.store.FindProvision(ctx, name)
}

func (s *Service) Provision(ctx context.Context, request ProvisionRequest) (ProvisionResult, error) {
	if err := box.ValidateName(request.Name); err != nil {
		return ProvisionResult{}, box.NewError("invalid_input", err.Error(), err)
	}
	if request.Region == "" || request.Size == "" || request.Image == "" {
		return ProvisionResult{}, box.NewError("invalid_input", "DigitalOcean region, size, and image are required", nil)
	}
	if len(request.AccessKeyIDs)+len(request.LocalPublicKeys) > 15 {
		return ProvisionResult{}, box.NewError("invalid_input", "select at most 15 additional SSH keys", nil)
	}
	accessKeyIDs := append([]string(nil), request.AccessKeyIDs...)
	sort.Strings(accessKeyIDs)
	localPublicKeys := append([]provider.PublicKey(nil), request.LocalPublicKeys...)
	sort.Slice(localPublicKeys, func(i, j int) bool { return localPublicKeys[i].Fingerprint < localPublicKeys[j].Fingerprint })
	operation := ProvisionOperation{Name: request.Name, Profile: request.Profile, Region: request.Region, Size: request.Size, Image: request.Image, NetworkID: request.NetworkID, AccessKeyIDs: accessKeyIDs, LocalPublicKeys: localPublicKeys, AutomaticBackups: request.AutomaticBackups, IPv6: request.IPv6, WorktreeRoot: request.WorktreeRoot, Checkpoint: "requested", UpdatedAt: s.now().UTC()}
	if existing, findErr := s.store.FindProvision(ctx, request.Name); findErr == nil {
		if operation.Profile == "" {
			operation.Profile = existing.Profile
		}
		if err := ConflictForOperation(existing, operation); err != nil {
			return ProvisionResult{}, err
		}
		// Box preparation commits before the provisioning checkpoint is removed.
		// Resume that local completion without repeating provider or remote work.
		if record, findErr := s.store.FindByName(ctx, existing.Name); findErr == nil {
			if record.Acquisition != "provisioned" || record.Provider != string(provider.DigitalOcean) || existing.ResourceID == "" || record.ProviderResourceID != existing.ResourceID || record.ProviderCorrelationID != existing.CorrelationID || record.CredentialProfile != string(existing.Profile) {
				return ProvisionResult{}, box.NewError("conflict", "box name is already in use by a different provisioning operation", nil)
			}
			observation, observationErr := s.store.LastObservation(ctx, record.ID)
			if observationErr != nil {
				return ProvisionResult{}, observationErr
			}
			if err := s.store.CompleteProvision(ctx, existing, record.ID); err != nil {
				return ProvisionResult{}, err
			}
			return ProvisionResult{AddResult: box.AddResult{Box: record, Capabilities: observation.Capabilities, Verified: []string{"git", "schooner", "tmux"}}, Resource: provider.ResourceRef{Provider: provider.DigitalOcean, ResourceID: record.ProviderResourceID, CorrelationID: record.ProviderCorrelationID, Profile: existing.Profile}, Warning: existing.Warning}, nil
		} else if !box.IsNotFound(findErr) {
			return ProvisionResult{}, findErr
		}
	} else if !box.IsNotFound(findErr) {
		return ProvisionResult{}, findErr
	}
	credential, err := s.credentials.Resolve(ctx, request.Profile)
	if err != nil {
		return ProvisionResult{}, err
	}
	identity, err := s.identity.Ensure(ctx)
	if err != nil {
		return ProvisionResult{}, err
	}
	correlationID, err := s.newID()
	if err != nil {
		return ProvisionResult{}, err
	}
	operation.CorrelationID = correlationID
	operation.Profile = credential.Profile
	operation.IdentityFile = identity.PrivateKey
	operation, err = s.store.BeginProvision(ctx, operation)
	if err != nil {
		return ProvisionResult{}, err
	}
	operation.Checkpoint = "provider_request_pending"
	operation.UpdatedAt = s.now().UTC()
	if err = s.store.CheckpointProvision(ctx, operation); err != nil {
		return ProvisionResult{}, err
	}
	var machine provider.ProvisionedMachine
	if err = runProgress(request.Progress, box.StepProvision, "Create DigitalOcean Droplet", func() error {
		var provisionErr error
		machine, provisionErr = s.cloud.Provision(ctx, credential.Token, provider.ProvisionRequest{Name: request.Name, CorrelationID: operation.CorrelationID, Region: operation.Region, Size: operation.Size, Image: operation.Image, NetworkID: operation.NetworkID, AccessKeyIDs: operation.AccessKeyIDs, LocalPublicKeys: operation.LocalPublicKeys, AutomaticBackups: operation.AutomaticBackups, IPv6: operation.IPv6, ControlPublicKey: identity.PublicKey, KnownResourceID: operation.ResourceID})
		return provisionErr
	}); err != nil {
		operation.Checkpoint = failureCheckpoint(err)
		operation.UpdatedAt = s.now().UTC()
		_ = s.store.CheckpointProvision(ctx, operation)
		return ProvisionResult{}, err
	}
	operation.ResourceID = machine.ResourceID
	operation.Warning = machine.Warning
	operation.SSHDestination = machine.SSHUsername + "@" + machine.PublicIPv4
	operation.Checkpoint = "resource_identified"
	operation.UpdatedAt = s.now().UTC()
	if err = s.store.CheckpointProvision(ctx, operation); err != nil {
		return ProvisionResult{}, err
	}
	if err = runProgress(request.Progress, box.StepWaitSSH, "Wait for SSH readiness", func() error {
		// Provider follow-up SSH is always batch: progress UI cannot answer OpenSSH prompts.
		return s.waiter.WaitReady(ctx, box.Connection{Destination: operation.SSHDestination, IdentityFile: identity.PrivateKey, AcceptNewHostKey: request.AcceptNewHostKey, BatchMode: true})
	}); err != nil {
		operation.Checkpoint = failureCheckpoint(err)
		operation.UpdatedAt = s.now().UTC()
		_ = s.store.CheckpointProvision(ctx, operation)
		return ProvisionResult{}, err
	}
	operation.Checkpoint = "connection_ready"
	operation.UpdatedAt = s.now().UTC()
	if err = s.store.CheckpointProvision(ctx, operation); err != nil {
		return ProvisionResult{}, err
	}
	operation.Checkpoint = "remote_preparation"
	operation.UpdatedAt = s.now().UTC()
	if err = s.store.CheckpointProvision(ctx, operation); err != nil {
		return ProvisionResult{}, err
	}
	resource := provider.ResourceRef{Provider: provider.DigitalOcean, ResourceID: machine.ResourceID, CorrelationID: operation.CorrelationID, Profile: credential.Profile}
	added, err := s.boxes.Add(ctx, box.AddRequest{Name: request.Name, SSHDestination: operation.SSHDestination, IdentityFile: identity.PrivateKey, WorktreeRoot: request.WorktreeRoot, Acquisition: "provisioned", Provider: string(provider.DigitalOcean), ProviderResourceID: resource.ResourceID, ProviderCorrelationID: resource.CorrelationID, CredentialProfile: string(resource.Profile), ProviderRegion: operation.Region, AcceptNewHostKey: request.AcceptNewHostKey, BatchMode: true, Progress: request.Progress})
	if err != nil {
		operation.Checkpoint = failureCheckpoint(err)
		operation.UpdatedAt = s.now().UTC()
		_ = s.store.CheckpointProvision(ctx, operation)
		return ProvisionResult{}, err
	}
	if err = s.store.CompleteProvision(ctx, operation, added.Box.ID); err != nil {
		return ProvisionResult{}, err
	}
	return ProvisionResult{AddResult: added, Resource: resource, Warning: machine.Warning}, nil
}

func (s *Service) Destroy(ctx context.Context, name string) (DestroyResult, error) {
	record, err := s.store.FindByName(ctx, name)
	if err != nil {
		return DestroyResult{}, err
	}
	if record.Acquisition != "provisioned" || record.Provider != string(provider.DigitalOcean) {
		return DestroyResult{}, box.NewError("unsupported", "only provider-provisioned DigitalOcean boxes can be destroyed", nil)
	}
	ref := provider.ResourceRef{Provider: provider.ID(record.Provider), ResourceID: record.ProviderResourceID, CorrelationID: record.ProviderCorrelationID, Profile: provider.CredentialProfileRef(record.CredentialProfile)}
	if err = ref.Validate(); err != nil {
		return DestroyResult{}, box.NewError("conflict", "box has an incomplete provider resource reference", err)
	}
	credential, err := s.credentials.Resolve(ctx, ref.Profile)
	if err != nil {
		return DestroyResult{}, err
	}
	op := DestroyOperation{BoxID: record.ID, Name: record.Name, Resource: ref, Checkpoint: "requested", UpdatedAt: s.now().UTC()}
	if err = s.store.BeginDestroy(ctx, op); err != nil {
		return DestroyResult{}, err
	}
	resource, err := s.cloud.Inspect(ctx, credential.Token, ref)
	if err != nil {
		if box.IsNotFound(err) {
			op.Checkpoint = "confirmed_destroyed"
		} else {
			op.Checkpoint = failureCheckpoint(err)
			op.UpdatedAt = s.now().UTC()
			_ = s.store.CheckpointDestroy(ctx, op)
			return DestroyResult{}, err
		}
	} else if resource.ID != ref.ResourceID || resource.CorrelationID != ref.CorrelationID {
		return DestroyResult{}, box.NewError("conflict", "DigitalOcean resource verification did not match the recorded box", nil)
	} else if err = s.cloud.Destroy(ctx, credential.Token, ref); err != nil {
		op.Checkpoint = failureCheckpoint(err)
		op.UpdatedAt = s.now().UTC()
		_ = s.store.CheckpointDestroy(ctx, op)
		return DestroyResult{}, err
	} else {
		op.Checkpoint = "confirmed_destroyed"
	}
	op.UpdatedAt = s.now().UTC()
	if err = s.store.CheckpointDestroy(ctx, op); err != nil {
		return DestroyResult{}, err
	}
	removed, err := s.boxes.Remove(ctx, name)
	if err != nil {
		return DestroyResult{}, err
	}
	if err = s.store.CompleteDestroy(ctx, record.ID); err != nil {
		return DestroyResult{}, err
	}
	return DestroyResult{Box: removed.Box, Resource: ref, LocalRemoved: true}, nil
}

func failureCheckpoint(err error) string {
	if box.ErrorCode(err) == "outcome_unknown" {
		return "outcome_unknown"
	}
	return "failed"
}

func runProgress(progress box.Progress, step box.Step, message string, action func() error) error {
	if progress != nil {
		progress(box.Event{Step: step, State: box.EventStarted, Message: message})
	}
	err := action()
	if err != nil {
		if progress != nil {
			progress(box.Event{Step: step, State: box.EventFailed, Message: message})
		}
		return err
	}
	if progress != nil {
		progress(box.Event{Step: step, State: box.EventCompleted, Message: message})
	}
	return nil
}

func sameOperation(a, b ProvisionOperation) bool {
	return a.Name == b.Name && a.Profile == b.Profile && a.Region == b.Region && a.Size == b.Size && a.Image == b.Image && a.NetworkID == b.NetworkID && slices.Equal(a.AccessKeyIDs, b.AccessKeyIDs) && slices.Equal(a.LocalPublicKeys, b.LocalPublicKeys) && a.AutomaticBackups == b.AutomaticBackups && a.IPv6 == b.IPv6 && a.WorktreeRoot == b.WorktreeRoot
}

func randomID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func ConflictForOperation(existing, requested ProvisionOperation) error {
	if sameOperation(existing, requested) {
		return nil
	}
	return box.NewError("conflict", fmt.Sprintf("an interrupted DigitalOcean add for %q uses different provisioning selections", requested.Name), nil)
}
