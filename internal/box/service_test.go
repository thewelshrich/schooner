package box

import (
	"context"
	"errors"
	"slices"
	"strings"
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
	if result.Box.Acquisition != "adopted" || result.Box.WorkspaceRoot != "/home/alice/schooner" || result.Box.RuntimePath != "/home/alice/.local/bin/schooner" {
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
	if len(events) != 18 || events[0].Step != StepResolve || events[len(events)-1].Step != StepSave {
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

func TestAddRejectsClonedIdentityAfterOpenSSHTrustResolution(t *testing.T) {
	store := newMemoryInventory()
	identity := "11111111-1111-4111-8111-111111111111"
	store.records["existing"] = Record{Name: "existing", SSHDestination: "original-host", RemoteIdentity: identity}
	runtime := &fakeRuntime{capabilities: readyCapabilities()}
	runtime.capabilities.RemoteIdentity = identity
	service := testService(runtime, store)
	_, err := service.Add(t.Context(), AddRequest{Name: "clone", SSHDestination: "clone-host", AcceptNewHostKey: true})
	if ErrorCode(err) != "conflict" {
		t.Fatalf("error = %v", err)
	}
	if runtime.resolved.Destination != "clone-host" || !runtime.resolved.AcceptNewHostKey || runtime.inspected.Destination != "clone-host" {
		t.Fatalf("host trust and inspection were not established independently: resolved=%+v inspected=%+v", runtime.resolved, runtime.inspected)
	}
	if runtime.installCalled {
		t.Fatal("duplicate identity caused remote setup")
	}
}

func TestStatusVerifiesIdentityAndCachesObservation(t *testing.T) {
	store := newMemoryInventory()
	store.records["work"] = Record{ID: "box-1", Name: "work", SSHDestination: "work", RemoteIdentity: "remote-1", RuntimePath: "/home/alice/.local/bin/schooner", WorkspaceRoot: "/home/alice/schooner"}
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
	if runtime.inspectCalls != 0 || runtime.inspectHostCalls != 1 {
		t.Fatalf("status used shell after bootstrap: inspect=%d inspectHost=%d", runtime.inspectCalls, runtime.inspectHostCalls)
	}
}

func TestStatusReportsMissingRuntimeWithoutRepair(t *testing.T) {
	store := newMemoryInventory()
	store.records["work"] = Record{ID: "box-1", Name: "work", SSHDestination: "work", RemoteIdentity: "remote-1", WorkspaceRoot: "/home/alice/schooner"}
	runtime := &fakeRuntime{capabilities: readyCapabilities()}
	runtime.capabilities.RemoteIdentity = "remote-1"
	service := testService(runtime, store)
	_, err := service.Status(t.Context(), StatusRequest{Name: "work"})
	if ErrorCode(err) != "host_runtime_missing" || !strings.Contains(err.Error(), "box setup work") {
		t.Fatalf("error=%v", err)
	}
	if runtime.inspectCalls != 0 || runtime.ensureHostCalls != 0 {
		t.Fatalf("status mutated through bootstrap: inspect=%d ensure=%d", runtime.inspectCalls, runtime.ensureHostCalls)
	}
}

func TestStatusReportsUnavailableRuntimeWithoutRepair(t *testing.T) {
	store := newMemoryInventory()
	store.records["work"] = Record{ID: "box-1", Name: "work", SSHDestination: "work", RemoteIdentity: "remote-1", RuntimePath: "/home/alice/.local/bin/schooner", WorkspaceRoot: "/home/alice/schooner"}
	runtime := &fakeRuntime{capabilities: readyCapabilities(), inspectHostErr: NewError("host_runtime_missing", "missing", nil)}
	runtime.capabilities.RemoteIdentity = "remote-1"
	service := testService(runtime, store)
	_, err := service.Status(t.Context(), StatusRequest{Name: "work"})
	if ErrorCode(err) != "host_runtime_missing" || !strings.Contains(err.Error(), "box setup work") {
		t.Fatalf("error=%v", err)
	}
	if runtime.inspectCalls != 0 || runtime.ensureHostCalls != 0 {
		t.Fatalf("status repaired runtime: inspect=%d ensure=%d", runtime.inspectCalls, runtime.ensureHostCalls)
	}
}

func TestSetupRepairsRuntimePrerequisitesAndMovedHome(t *testing.T) {
	store := newMemoryInventory()
	store.records["work"] = Record{ID: "box-1", Name: "work", SSHDestination: "work", RemoteIdentity: "remote-1", RuntimePath: "/home/alice/.local/bin/schooner", WorkspaceRoot: "/home/alice/schooner"}
	runtime := &fakeRuntime{capabilities: readyCapabilities()}
	runtime.capabilities.Home = "/srv/alice"
	runtime.capabilities.RemoteIdentity = "remote-1"
	runtime.capabilities.Git = Tool{}
	service := testService(runtime, store)

	result, err := service.Setup(t.Context(), SetupRequest{Name: "work", BatchMode: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "/srv/alice/.local/bin/schooner"
	if result.Box.RuntimePath != want || store.records["work"].RuntimePath != want || runtime.ensured.Path != want || runtime.ensured.Mode != HostRepair {
		t.Fatalf("result=%+v stored=%+v ensured=%+v", result.Box, store.records["work"], runtime.ensured)
	}
	if !runtime.installCalled || !slices.Equal(result.Installed, []string{"git"}) || store.observations["box-1"].Capabilities.Host.Path != want {
		t.Fatalf("installed=%v called=%t observation=%+v", result.Installed, runtime.installCalled, store.observations["box-1"])
	}
}

func TestSetupFailsIdentityMismatchBeforeMutation(t *testing.T) {
	store := newMemoryInventory()
	store.records["work"] = Record{ID: "box-1", Name: "work", SSHDestination: "work", RemoteIdentity: "remote-1", WorkspaceRoot: "/home/alice/schooner"}
	runtime := &fakeRuntime{capabilities: readyCapabilities()}
	runtime.capabilities.RemoteIdentity = "remote-other"
	service := testService(runtime, store)

	_, err := service.Setup(t.Context(), SetupRequest{Name: "work"})
	if ErrorCode(err) != "conflict" || runtime.ensureHostCalls != 0 || runtime.installCalled {
		t.Fatalf("error=%v ensure=%d install=%t", err, runtime.ensureHostCalls, runtime.installCalled)
	}
}

func TestUpdateUsesUpdateModeWithoutPrerequisiteMutation(t *testing.T) {
	store := newMemoryInventory()
	store.records["work"] = Record{ID: "box-1", Name: "work", SSHDestination: "work", RemoteIdentity: "remote-1", RuntimePath: "/home/alice/.local/bin/schooner", WorkspaceRoot: "/home/alice/schooner"}
	runtime := &fakeRuntime{capabilities: readyCapabilities()}
	runtime.capabilities.RemoteIdentity = "remote-1"
	runtime.capabilities.Git = Tool{}
	service := testService(runtime, store)

	result, err := service.Update(t.Context(), UpdateRequest{Name: "work", BatchMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ensured.Mode != HostUpdate || runtime.installCalled || result.Host.Action != HostReused {
		t.Fatalf("ensured=%+v install=%t result=%+v", runtime.ensured, runtime.installCalled, result)
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
	store.records["work"] = Record{ID: "box-1", Name: "work", SSHDestination: "work", RemoteIdentity: "remote-1", RuntimePath: "/home/alice/.local/bin/schooner"}
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
	return Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64", Home: "/home/alice", WorkspaceRoot: "/home/alice/schooner", WorkspaceRootExists: true, Git: Tool{Available: true, Version: "git version 2.43.0"}, Tmux: Tool{Available: true, Version: "tmux 3.4"}, PasswordlessSudo: true, Host: HostRuntime{Path: "/home/alice/.local/bin/schooner", Version: "v1.2.3", ProtocolVersion: "1", Capabilities: []string{"host.doctor.v1", "host.hello.v1", "host.inspect.v1"}}}
}

type fakeRuntime struct {
	capabilities      Capabilities
	inspectErr        error
	inspectHostErr    error
	inspectHostErrors []error
	installCalled     bool
	calls             int
	inspectCalls      int
	inspectHostCalls  int
	ensureHostCalls   int
	resolved          Connection
	inspected         Connection
	ensured           HostInstallRequest
}

func (f *fakeRuntime) Resolve(_ context.Context, connection Connection) error {
	f.calls++
	f.resolved = connection
	return nil
}
func (f *fakeRuntime) Inspect(_ context.Context, connection Connection, _ string) (Capabilities, error) {
	f.calls++
	f.inspectCalls++
	f.inspected = connection
	return f.capabilities, f.inspectErr
}
func (f *fakeRuntime) EnsureIdentity(_ context.Context, _ Connection, candidate string) (string, error) {
	f.calls++
	f.capabilities.RemoteIdentity = candidate
	return candidate, nil
}
func (f *fakeRuntime) EnsureHost(_ context.Context, _ Connection, request HostInstallRequest) (HostInstallResult, error) {
	f.calls++
	f.ensureHostCalls++
	f.ensured = request
	f.capabilities.Host = HostRuntime{Path: request.Path, Version: "v1.2.3", ProtocolVersion: "1", Capabilities: []string{"host.doctor.v1", "host.hello.v1", "host.inspect.v1"}}
	return HostInstallResult{Runtime: f.capabilities.Host, TargetVersion: "v1.2.3", Action: HostReused}, nil
}
func (f *fakeRuntime) InspectHost(_ context.Context, _ Connection, installed HostRuntime, _ string, _ string) (Capabilities, error) {
	f.calls++
	f.inspectHostCalls++
	f.capabilities.Host = installed
	if f.capabilities.Host.Version == "" {
		f.capabilities.Host.Version = "v1.2.3"
		f.capabilities.Host.ProtocolVersion = "1"
		f.capabilities.Host.Capabilities = []string{"host.doctor.v1", "host.hello.v1", "host.inspect.v1"}
	}
	if len(f.inspectHostErrors) > 0 {
		err := f.inspectHostErrors[0]
		f.inspectHostErrors = f.inspectHostErrors[1:]
		if err != nil {
			return f.capabilities, err
		}
	}
	if f.inspectHostErr != nil {
		return f.capabilities, f.inspectHostErr
	}
	return f.capabilities, f.inspectErr
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
func (m *memoryInventory) FindByID(_ context.Context, id string) (Record, error) {
	for _, value := range m.records {
		if value.ID == id {
			return value, nil
		}
	}
	return Record{}, NotFound(id)
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
func (m *memoryInventory) SetDefault(_ context.Context, name string) (Record, error) {
	selected, ok := m.records[name]
	if !ok {
		return Record{}, NotFound(name)
	}
	for key, value := range m.records {
		value.Default = key == name
		m.records[key] = value
	}
	selected.Default = true
	return selected, nil
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
func (m *memoryInventory) UpdateRuntimePath(_ context.Context, boxID, runtimePath string) error {
	for name, record := range m.records {
		if record.ID == boxID {
			record.RuntimePath = runtimePath
			m.records[name] = record
			return nil
		}
	}
	return NotFound(boxID)
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
