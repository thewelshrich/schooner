package acquisition

import (
	"context"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/credentials"
	"github.com/thewelshrich/schooner/internal/provider"
)

func TestProvisionConvergesOnBoxPreparationAndDestroy(t *testing.T) {
	store := newTestStore()
	runtime := &testRuntime{capabilities: box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64", Home: "/root", WorkspaceRoot: "/root/schooner", WorkspaceRootExists: true, Git: box.Tool{Available: true}, Tmux: box.Tool{Available: true}}}
	boxService := box.New(runtime, store)
	cloud := &testCloud{}
	waiter := &testWaiter{}
	service := New(boxService, store, testResolver{}, cloud, testIdentity{}, waiter)
	service.newID = func() (string, error) { return "correlation-1", nil }
	localKey := provider.PublicKey{Name: "id_ed25519", Fingerprint: "SHA256:local", PublicKey: "ssh-ed25519 CCCC"}
	var events []box.Event
	result, err := service.Provision(t.Context(), ProvisionRequest{Name: "work", Region: "fra1", Size: "small", Image: "ubuntu-24-04-x64", LocalPublicKeys: []provider.PublicKey{localKey}, IPv6: true, WorkspaceRoot: box.DefaultWorkspaceRoot, AcceptNewHostKey: true, Progress: func(event box.Event) { events = append(events, event) }})
	if err != nil {
		t.Fatal(err)
	}
	if result.Box.Acquisition != "provisioned" || result.Box.ProviderResourceID != "42" || result.Box.IdentityFile != "/state/id_ed25519" || result.Box.RuntimePath != "/root/.local/bin/schooner" {
		t.Fatalf("box=%+v", result.Box)
	}
	if result.Box.ProviderRegion != "fra1" {
		t.Fatalf("provider region = %q", result.Box.ProviderRegion)
	}
	if cloud.provision.CorrelationID != "correlation-1" || runtime.connection.IdentityFile != "/state/id_ed25519" {
		t.Fatalf("provision=%+v connection=%+v", cloud.provision, runtime.connection)
	}
	if len(cloud.provision.LocalPublicKeys) != 1 || cloud.provision.LocalPublicKeys[0] != localKey {
		t.Fatalf("local public keys were not handed to provider: %+v", cloud.provision.LocalPublicKeys)
	}
	if waiter.connection.Destination != "root@203.0.113.8" || waiter.connection.IdentityFile != "/state/id_ed25519" || !waiter.connection.BatchMode || !waiter.connection.AcceptNewHostKey {
		t.Fatalf("readiness connection = %+v", waiter.connection)
	}
	if !runtime.connection.BatchMode || !runtime.connection.AcceptNewHostKey {
		t.Fatalf("box preparation connection = %+v", runtime.connection)
	}
	if len(events) < 2 || events[0].Step != box.StepProvision || events[0].State != box.EventStarted || events[1].Step != box.StepProvision || events[1].State != box.EventCompleted {
		t.Fatalf("expected DigitalOcean provision progress before box prep, got %+v", events)
	}
	if len(events) < 5 || events[2].Step != box.StepWaitSSH || events[2].State != box.EventStarted || events[3].Step != box.StepWaitSSH || events[3].State != box.EventCompleted || events[4].Step != box.StepResolve {
		t.Fatalf("expected SSH readiness before box preparation, got %+v", events)
	}
	destroyed, err := service.Destroy(t.Context(), "work")
	if err != nil || !destroyed.LocalRemoved || !cloud.destroyed {
		t.Fatalf("destroy=%+v cloud=%t err=%v", destroyed, cloud.destroyed, err)
	}
}

func TestInterruptedProvisionReturnsRecordedSelections(t *testing.T) {
	store := newTestStore()
	service := New(box.New(&testRuntime{}, store), store, testResolver{}, &testCloud{}, testIdentity{}, &testWaiter{})
	if _, err := service.InterruptedProvision(t.Context(), "work"); !box.IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
	op := ProvisionOperation{Name: "work", CorrelationID: "correlation-1", Profile: "digitalocean/default", Region: "fra1", Size: "small", Image: "ubuntu-24-04-x64", WorkspaceRoot: box.DefaultWorkspaceRoot, Checkpoint: "provider_request_pending"}
	if _, err := store.BeginProvision(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	got, err := service.InterruptedProvision(t.Context(), "work")
	if err != nil || got.CorrelationID != "correlation-1" || got.Region != "fra1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestProvisionResumesExistingCorrelationWithoutDuplicateSelections(t *testing.T) {
	store := newTestStore()
	runtime := &testRuntime{capabilities: box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64", Home: "/root", WorkspaceRoot: "/root/schooner", WorkspaceRootExists: true, Git: box.Tool{Available: true}, Tmux: box.Tool{Available: true}}}
	cloud := &testCloud{}
	service := New(box.New(runtime, store), store, testResolver{}, cloud, testIdentity{}, &testWaiter{})
	service.newID = func() (string, error) { return "correlation-new", nil }
	existing := ProvisionOperation{Name: "work", CorrelationID: "correlation-1", Profile: "digitalocean/default", Region: "fra1", Size: "small", Image: "ubuntu-24-04-x64", WorkspaceRoot: box.DefaultWorkspaceRoot, ResourceID: "42", Checkpoint: "resource_identified", IdentityFile: "/state/id_ed25519"}
	if _, err := store.BeginProvision(t.Context(), existing); err != nil {
		t.Fatal(err)
	}
	result, err := service.Provision(t.Context(), ProvisionRequest{Name: "work", Region: "fra1", Size: "small", Image: "ubuntu-24-04-x64", WorkspaceRoot: box.DefaultWorkspaceRoot, AcceptNewHostKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if cloud.provision.CorrelationID != "correlation-1" || cloud.provision.KnownResourceID != "42" {
		t.Fatalf("provision=%+v", cloud.provision)
	}
	if result.Box.Name != "work" {
		t.Fatalf("box=%+v", result.Box)
	}
}

type testResolver struct{}

func (testResolver) Resolve(context.Context, provider.CredentialProfileRef) (credentials.Credential, error) {
	return credentials.Credential{Profile: "digitalocean/default", Token: "token", Account: provider.Account{ExternalID: "team-1"}}, nil
}

type testIdentity struct{}

func (testIdentity) Ensure(context.Context) (Identity, error) {
	return Identity{PublicKey: "ssh-ed25519 AAAA", PrivateKey: "/state/id_ed25519"}, nil
}

type testWaiter struct{ connection box.Connection }

func (w *testWaiter) WaitReady(_ context.Context, connection box.Connection) error {
	w.connection = connection
	return nil
}

type testCloud struct {
	provision provider.ProvisionRequest
	destroyed bool
}

func (*testCloud) Identify(context.Context, string) (provider.Account, error) {
	return provider.Account{}, nil
}

func (*testCloud) Verify(context.Context, string) (provider.Account, error) {
	return provider.Account{}, nil
}
func (*testCloud) Catalog(context.Context, string) (provider.Catalog, error) {
	return provider.Catalog{}, nil
}
func (f *testCloud) Provision(_ context.Context, _ string, request provider.ProvisionRequest) (provider.ProvisionedMachine, error) {
	f.provision = request
	return provider.ProvisionedMachine{ResourceID: "42", PublicIPv4: "203.0.113.8", SSHUsername: "root"}, nil
}
func (*testCloud) Inspect(_ context.Context, _ string, ref provider.ResourceRef) (provider.Resource, error) {
	return provider.Resource{ID: ref.ResourceID, CorrelationID: ref.CorrelationID}, nil
}
func (f *testCloud) Destroy(context.Context, string, provider.ResourceRef) error {
	f.destroyed = true
	return nil
}

type testRuntime struct {
	capabilities box.Capabilities
	connection   box.Connection
}

func (f *testRuntime) Resolve(_ context.Context, connection box.Connection) error {
	f.connection = connection
	return nil
}
func (f *testRuntime) Inspect(_ context.Context, connection box.Connection, _ string) (box.Capabilities, error) {
	f.connection = connection
	return f.capabilities, nil
}
func (f *testRuntime) EnsureIdentity(_ context.Context, connection box.Connection, candidate string) (string, error) {
	f.connection = connection
	f.capabilities.RemoteIdentity = candidate
	return candidate, nil
}
func (f *testRuntime) EnsureHost(_ context.Context, connection box.Connection, request box.HostInstallRequest) (box.HostRuntime, error) {
	f.connection = connection
	f.capabilities.Host = box.HostRuntime{Path: request.Path, Version: "v1.2.3", ProtocolVersion: "1", Capabilities: []string{"host.doctor.v1", "host.hello.v1", "host.inspect.v1"}}
	return f.capabilities.Host, nil
}
func (f *testRuntime) InspectHost(_ context.Context, connection box.Connection, installed box.HostRuntime, _ string, _ string) (box.Capabilities, error) {
	f.connection = connection
	f.capabilities.Host = installed
	return f.capabilities, nil
}
func (*testRuntime) InstallTools(context.Context, box.Connection, []string) error { return nil }
func (f *testRuntime) EnsureWorkspaceRoot(_ context.Context, connection box.Connection, _ string) (string, error) {
	f.connection = connection
	return "/root/schooner", nil
}

type testStore struct {
	records      map[string]box.Record
	observations map[string]box.Observation
	add          box.AddOperation
	provision    *ProvisionOperation
	destroy      *DestroyOperation
}

func newTestStore() *testStore {
	return &testStore{records: map[string]box.Record{}, observations: map[string]box.Observation{}}
}
func (s *testStore) FindByName(_ context.Context, name string) (box.Record, error) {
	value, ok := s.records[name]
	if !ok {
		return box.Record{}, box.NotFound(name)
	}
	return value, nil
}
func (s *testStore) FindByRemoteIdentity(_ context.Context, id string) (box.Record, error) {
	for _, value := range s.records {
		if value.RemoteIdentity == id {
			return value, nil
		}
	}
	return box.Record{}, box.NotFound(id)
}
func (s *testStore) List(context.Context) ([]box.Record, error) {
	values := []box.Record{}
	for _, value := range s.records {
		values = append(values, value)
	}
	return values, nil
}
func (s *testStore) BeginAdd(_ context.Context, op box.AddOperation) error { s.add = op; return nil }
func (s *testStore) CheckpointAdd(_ context.Context, op box.AddOperation) error {
	s.add = op
	return nil
}
func (s *testStore) CompleteAdd(_ context.Context, _ box.AddOperation, record box.Record, observation box.Observation) error {
	s.records[record.Name] = record
	s.observations[record.ID] = observation
	return nil
}
func (s *testStore) UpdateRuntimePath(_ context.Context, id, runtimePath string) error {
	for name, record := range s.records {
		if record.ID == id {
			record.RuntimePath = runtimePath
			s.records[name] = record
			return nil
		}
	}
	return box.NotFound(id)
}
func (s *testStore) SaveObservation(_ context.Context, value box.Observation) error {
	s.observations[value.BoxID] = value
	return nil
}
func (s *testStore) LastObservation(_ context.Context, id string) (box.Observation, error) {
	value, ok := s.observations[id]
	if !ok {
		return box.Observation{}, box.NotFound(id)
	}
	return value, nil
}
func (s *testStore) Remove(_ context.Context, name string) (box.Record, error) {
	value, ok := s.records[name]
	if !ok {
		return box.Record{}, box.NotFound(name)
	}
	delete(s.records, name)
	return value, nil
}
func (s *testStore) FindProvision(_ context.Context, name string) (ProvisionOperation, error) {
	if s.provision == nil || s.provision.Name != name {
		return ProvisionOperation{}, box.NotFound(name)
	}
	return *s.provision, nil
}
func (s *testStore) BeginProvision(_ context.Context, value ProvisionOperation) (ProvisionOperation, error) {
	if s.provision != nil {
		return *s.provision, ConflictForOperation(*s.provision, value)
	}
	s.provision = &value
	return value, nil
}
func (s *testStore) CheckpointProvision(_ context.Context, value ProvisionOperation) error {
	s.provision = &value
	return nil
}
func (s *testStore) CompleteProvision(context.Context, string) error { s.provision = nil; return nil }
func (s *testStore) BeginDestroy(_ context.Context, value DestroyOperation) error {
	s.destroy = &value
	return nil
}
func (s *testStore) CheckpointDestroy(_ context.Context, value DestroyOperation) error {
	s.destroy = &value
	return nil
}
func (s *testStore) CompleteDestroy(context.Context, string) error { s.destroy = nil; return nil }
