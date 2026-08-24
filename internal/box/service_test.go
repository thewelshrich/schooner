package box

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestAddPreparesAndPersistsBox(t *testing.T) {
	store := newMemoryInventory()
	runtime := &fakeRuntime{capabilities: readyCapabilities()}
	runtime.capabilities.Git.Available = false
	runtime.capabilities.Git.Version = ""
	service := testService(runtime, store)
	var events []Event
	result, err := service.Add(t.Context(), AddRequest{Name: "work-api", SSHDestination: "work", WorkspaceRoot: DefaultWorkspaceRoot, Progress: func(event Event) { events = append(events, event) }})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if result.Box.Acquisition != "adopted" || result.Box.WorkspaceRoot != "/home/alice/schooner" {
		t.Fatalf("unexpected box: %+v", result.Box)
	}
	if !slices.Equal(result.Installed, []string{"git"}) {
		t.Fatalf("installed = %v", result.Installed)
	}
	if !runtime.installCalled {
		t.Fatal("InstallTools was not called")
	}
	if _, ok := store.records["work-api"]; !ok {
		t.Fatal("box was not persisted")
	}
	if len(events) != 16 || events[0].Step != StepResolve || events[len(events)-1].Step != StepSave {
		t.Fatalf("events = %+v", events)
	}
}

func TestAddRequiresPasswordlessSudoForMissingTools(t *testing.T) {
	store := newMemoryInventory()
	runtime := &fakeRuntime{capabilities: readyCapabilities()}
	runtime.capabilities.Tmux.Available = false
	runtime.capabilities.PasswordlessSudo = false
	service := testService(runtime, store)
	_, err := service.Add(t.Context(), AddRequest{Name: "work", SSHDestination: "work"})
	if ErrorCode(err) != "permission_denied" {
		t.Fatalf("error = %v, code = %s", err, ErrorCode(err))
	}
	if len(store.records) != 0 {
		t.Fatal("failed add became a visible box")
	}
	if store.operation.RemoteIdentity == "" {
		t.Fatal("recovery identity was not checkpointed")
	}
}

func TestAddRejectsDuplicateRemoteIdentity(t *testing.T) {
	store := newMemoryInventory()
	store.records["existing"] = Record{Name: "existing", RemoteIdentity: "remote-1"}
	runtime := &fakeRuntime{capabilities: readyCapabilities()}
	runtime.capabilities.RemoteIdentity = "remote-1"
	service := testService(runtime, store)
	_, err := service.Add(t.Context(), AddRequest{Name: "alias", SSHDestination: "other"})
	if ErrorCode(err) != "conflict" {
		t.Fatalf("error = %v", err)
	}
	if runtime.installCalled {
		t.Fatal("duplicate identity caused remote setup")
	}
}

func TestStatusVerifiesIdentityAndCachesObservation(t *testing.T) {
	store := newMemoryInventory()
	store.records["work"] = Record{ID: "box-1", Name: "work", SSHDestination: "work", RemoteIdentity: "remote-1", WorkspaceRoot: "/home/alice/schooner"}
	runtime := &fakeRuntime{capabilities: readyCapabilities()}
	runtime.capabilities.RemoteIdentity = "remote-1"
	service := testService(runtime, store)
	result, err := service.Status(t.Context(), StatusRequest{Name: "work"})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if result.Observation.Capabilities.OSVersion != "24.04" {
		t.Fatalf("result = %+v", result)
	}
	if store.observations["box-1"].ObservedAt.IsZero() {
		t.Fatal("observation was not cached")
	}
}

func TestListEntriesIncludesObservationAndMixedAcquisition(t *testing.T) {
	store := newMemoryInventory()
	observed := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	store.records["api"] = Record{ID: "box-ssh", Name: "api", Acquisition: "adopted", SSHDestination: "api"}
	store.records["cloud"] = Record{ID: "box-do", Name: "cloud", Acquisition: "provisioned", SSHDestination: "root@203.0.113.8", Provider: "digitalocean", ProviderRegion: "fra1"}
	store.observations["box-do"] = Observation{BoxID: "box-do", ObservedAt: observed, Capabilities: readyCapabilities()}
	service := testService(&fakeRuntime{}, store)
	entries, err := service.ListEntries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%+v", entries)
	}
	byName := map[string]ListEntry{}
	for _, entry := range entries {
		byName[entry.Box.Name] = entry
	}
	if byName["api"].Reachable || byName["api"].HasObservation || byName["api"].Box.ProviderRegion != "" {
		t.Fatalf("ssh entry=%+v", byName["api"])
	}
	if !byName["cloud"].Reachable || !byName["cloud"].HasObservation || byName["cloud"].Box.ProviderRegion != "fra1" || !byName["cloud"].LastObservedAt.Equal(observed) {
		t.Fatalf("cloud entry=%+v", byName["cloud"])
	}
}

func TestStatusFailureIncludesLastKnownTime(t *testing.T) {
	store := newMemoryInventory()
	store.records["work"] = Record{ID: "box-1", Name: "work", SSHDestination: "work", RemoteIdentity: "remote-1"}
	last := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	store.observations["box-1"] = Observation{BoxID: "box-1", ObservedAt: last, Capabilities: readyCapabilities()}
	runtime := &fakeRuntime{inspectErr: NewError("connection_failed", "offline", nil)}
	service := testService(runtime, store)
	_, err := service.Status(t.Context(), StatusRequest{Name: "work"})
	var target *Error
	if !errors.As(err, &target) || target.Context["last_observed_at"] != last.Format(time.RFC3339) {
		t.Fatalf("error = %#v", err)
	}
}

func TestPrepareSSHUsesRecordedConnectionForEveryAcquisition(t *testing.T) {
	store := newMemoryInventory()
	store.records["adopted"] = Record{Name: "adopted", Acquisition: "adopted", SSHDestination: "work-alias"}
	store.records["cloud"] = Record{Name: "cloud", Acquisition: "provisioned", SSHDestination: "root@203.0.113.8", IdentityFile: "/state/ssh/id_ed25519"}
	service := testService(&fakeRuntime{}, store)

	adopted, err := service.PrepareSSH(t.Context(), SSHRequest{Name: "adopted"})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Connection.Destination != "work-alias" || adopted.Connection.IdentityFile != "" || adopted.Connection.BatchMode {
		t.Fatalf("adopted launch = %+v", adopted)
	}

	cloud, err := service.PrepareSSH(t.Context(), SSHRequest{Name: "cloud", BatchMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if cloud.Connection.Destination != "root@203.0.113.8" || cloud.Connection.IdentityFile != "/state/ssh/id_ed25519" || !cloud.Connection.BatchMode {
		t.Fatalf("provisioned launch = %+v", cloud)
	}
	if runtime := service.runtime.(*fakeRuntime); runtime.calls != 0 {
		t.Fatalf("PrepareSSH performed %d remote operations", runtime.calls)
	}
}

func TestPrepareSSHRejectsInvalidOrMissingBox(t *testing.T) {
	service := testService(&fakeRuntime{}, newMemoryInventory())
	if _, err := service.PrepareSSH(t.Context(), SSHRequest{Name: "Not Valid"}); ErrorCode(err) != "invalid_input" {
		t.Fatalf("invalid name error = %v", err)
	}
	if _, err := service.PrepareSSH(t.Context(), SSHRequest{Name: "missing"}); ErrorCode(err) != "not_found" {
		t.Fatalf("missing box error = %v", err)
	}
}

func TestRemoveDoesNotCallRuntime(t *testing.T) {
	store := newMemoryInventory()
	store.records["work"] = Record{ID: "box-1", Name: "work"}
	runtime := &fakeRuntime{}
	service := testService(runtime, store)
	result, err := service.Remove(t.Context(), "work")
	if err != nil || !result.RemoteUnchanged {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if runtime.calls != 0 {
		t.Fatalf("runtime calls = %d", runtime.calls)
	}
}

func testService(runtime Runtime, store Inventory) *Service {
	service := New(runtime, store)
	service.now = func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }
	ids := []string{"remote-generated", "box-generated"}
	service.newID = func() (string, error) { value := ids[0]; ids = ids[1:]; return value, nil }
	return service
}

func readyCapabilities() Capabilities {
	return Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64", Home: "/home/alice", WorkspaceRoot: "/home/alice/schooner", WorkspaceRootExists: true, Git: Tool{Available: true, Version: "git version 2.43.0"}, Tmux: Tool{Available: true, Version: "tmux 3.4"}, PasswordlessSudo: true}
}

type fakeRuntime struct {
	capabilities  Capabilities
	inspectErr    error
	installCalled bool
	calls         int
}

func (f *fakeRuntime) Resolve(context.Context, Connection) error { f.calls++; return nil }
func (f *fakeRuntime) Inspect(context.Context, Connection, string) (Capabilities, error) {
	f.calls++
	return f.capabilities, f.inspectErr
}
func (f *fakeRuntime) EnsureIdentity(_ context.Context, _ Connection, candidate string) (string, error) {
	f.calls++
	f.capabilities.RemoteIdentity = candidate
	return candidate, nil
}
func (f *fakeRuntime) InstallTools(_ context.Context, _ Connection, tools []string) error {
	f.calls++
	f.installCalled = true
	for _, tool := range tools {
		if tool == "git" {
			f.capabilities.Git = Tool{Available: true, Version: "git version 2.43.0"}
		}
		if tool == "tmux" {
			f.capabilities.Tmux = Tool{Available: true, Version: "tmux 3.4"}
		}
	}
	return nil
}
func (f *fakeRuntime) EnsureWorkspaceRoot(context.Context, Connection, string) (string, error) {
	f.calls++
	return "/home/alice/schooner", nil
}

type memoryInventory struct {
	records      map[string]Record
	observations map[string]Observation
	operation    AddOperation
}

func newMemoryInventory() *memoryInventory {
	return &memoryInventory{records: map[string]Record{}, observations: map[string]Observation{}}
}
func (m *memoryInventory) FindByName(_ context.Context, name string) (Record, error) {
	value, ok := m.records[name]
	if !ok {
		return Record{}, NotFound(name)
	}
	return value, nil
}
func (m *memoryInventory) FindByRemoteIdentity(_ context.Context, identity string) (Record, error) {
	for _, value := range m.records {
		if value.RemoteIdentity == identity {
			return value, nil
		}
	}
	return Record{}, NotFound(identity)
}
func (m *memoryInventory) List(context.Context) ([]Record, error) {
	result := make([]Record, 0, len(m.records))
	for _, value := range m.records {
		result = append(result, value)
	}
	return result, nil
}
func (m *memoryInventory) BeginAdd(_ context.Context, op AddOperation) error {
	if m.operation.Name != "" && (m.operation.Name != op.Name || m.operation.SSHDestination != op.SSHDestination) {
		return NewError("conflict", "different operation", nil)
	}
	m.operation = op
	return nil
}
func (m *memoryInventory) CheckpointAdd(_ context.Context, op AddOperation) error {
	m.operation = op
	return nil
}
func (m *memoryInventory) CompleteAdd(_ context.Context, _ AddOperation, record Record, observation Observation) error {
	m.records[record.Name] = record
	m.observations[record.ID] = observation
	m.operation = AddOperation{}
	return nil
}
func (m *memoryInventory) SaveObservation(_ context.Context, observation Observation) error {
	m.observations[observation.BoxID] = observation
	return nil
}
func (m *memoryInventory) LastObservation(_ context.Context, boxID string) (Observation, error) {
	value, ok := m.observations[boxID]
	if !ok {
		return Observation{}, NotFound(boxID)
	}
	return value, nil
}
func (m *memoryInventory) Remove(_ context.Context, name string) (Record, error) {
	value, ok := m.records[name]
	if !ok {
		return Record{}, NotFound(name)
	}
	delete(m.records, name)
	delete(m.observations, value.ID)
	return value, nil
}
