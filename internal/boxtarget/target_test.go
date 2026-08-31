package boxtarget

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/config"
	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
	"github.com/thewelshrich/schooner/internal/runtime/ssh"
	"github.com/thewelshrich/schooner/internal/session"
	"github.com/thewelshrich/schooner/internal/source"
	"github.com/thewelshrich/schooner/internal/workspacetransfer"
)

const testIdentity = "11111111-1111-4111-8111-111111111111"

func TestResolverPrefersDirectAndDoesNotRequireInventory(t *testing.T) {
	home := t.TempDir()
	writeIdentity(t, home, testIdentity)
	root := filepath.Join(home, "worktrees")
	remoteOpened := false
	resolver := NewResolver(Options{
		Direct: func() *host.Runtime { return host.NewAtHome(hostruntime.BuildInfo{}, home) },
		Remote: ssh.NewHost("ssh", nil, "dev", nil),
		OpenInventory: func(context.Context) (Inventory, error) {
			remoteOpened = true
			return nil, errors.New("inventory must not open")
		},
		OpenExistingInventory: func(context.Context) (Inventory, bool, error) { return nil, false, nil },
		ReadHostConfig:        func() (config.Host, error) { return config.Host{WorktreeRoot: root}, nil },
	})
	target, err := resolver.Resolve(t.Context(), ResolveRequest{NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if !target.state.direct || target.state.boxIdentity != testIdentity || target.state.worktreeRoot != root || remoteOpened {
		t.Fatalf("target = %+v, remote opened = %t", target.state, remoteOpened)
	}
}

func TestResolverExplicitBoxBindsSSHAndClosesInventory(t *testing.T) {
	record := box.Record{Name: "work", SSHDestination: "work-host", IdentityFile: "/keys/work", RemoteIdentity: testIdentity, RuntimePath: "/home/alice/.local/bin/schooner", WorktreeRoot: "/home/alice/schooner"}
	inventory := &memoryInventory{records: []box.Record{record}}
	resolver := NewResolver(Options{
		Direct:                func() *host.Runtime { return nil },
		Remote:                ssh.NewHost("ssh", nil, "dev", nil),
		OpenInventory:         func(context.Context) (Inventory, error) { return inventory, nil },
		OpenExistingInventory: func(context.Context) (Inventory, bool, error) { return nil, false, nil },
	})
	target, err := resolver.Resolve(t.Context(), ResolveRequest{ExplicitBox: "work", NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := target.state.run.(sshAdapter)
	if !ok || target.state.direct || adapter.connection.Destination != "work-host" || !adapter.connection.BatchMode || adapter.interactiveBatchMode || !inventory.closed {
		t.Fatalf("target = %+v, adapter = %+v, closed = %t", target.state, adapter, inventory.closed)
	}
}

func TestResolverLinkedBoxPrecedesAvailableDirectBox(t *testing.T) {
	home := t.TempDir()
	writeIdentity(t, home, testIdentity)
	linkedIdentity := "22222222-2222-4222-8222-222222222222"
	record := box.Record{ID: "box-linked", Name: "linked", SSHDestination: "linked-host", RemoteIdentity: linkedIdentity, RuntimePath: "/home/alice/.local/bin/schooner", WorktreeRoot: "/home/alice/schooner"}
	inventory := &memoryInventory{records: []box.Record{record}}
	resolver := NewResolver(Options{
		Direct:                func() *host.Runtime { return host.NewAtHome(hostruntime.BuildInfo{}, home) },
		Remote:                ssh.NewHost("ssh", nil, "dev", nil),
		OpenInventory:         func(context.Context) (Inventory, error) { return inventory, nil },
		OpenExistingInventory: func(context.Context) (Inventory, bool, error) { return nil, false, nil },
		ReadHostConfig:        func() (config.Host, error) { return config.Host{}, errors.New("direct resolution must not run") },
	})
	target, err := resolver.Resolve(t.Context(), ResolveRequest{LinkedBoxID: record.ID, NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := target.state.run.(sshAdapter)
	if !ok || target.state.direct || target.BoxID() != record.ID || target.BoxIdentity() != linkedIdentity || adapter.connection.Destination != "linked-host" {
		t.Fatalf("target = %+v, adapter = %+v", target.state, adapter)
	}
}

func TestResolverLinkedBoxMatchingDirectHostAvoidsSelfSSH(t *testing.T) {
	home := t.TempDir()
	writeIdentity(t, home, testIdentity)
	root := filepath.Join(home, "worktrees")
	record := box.Record{ID: "box-local", Name: "local", SSHDestination: "self-host", RemoteIdentity: testIdentity, RuntimePath: "/home/alice/.local/bin/schooner", WorktreeRoot: root}
	inventory := &memoryInventory{records: []box.Record{record}}
	writableOpenCalls := 0
	resolver := NewResolver(Options{
		Direct: func() *host.Runtime { return host.NewAtHome(hostruntime.BuildInfo{}, home) },
		Remote: ssh.NewHost("ssh", nil, "dev", nil),
		OpenInventory: func(context.Context) (Inventory, error) {
			writableOpenCalls++
			return nil, errors.New("linked direct Box must not resolve through SSH")
		},
		OpenExistingInventory: func(context.Context) (Inventory, bool, error) { return inventory, true, nil },
		ReadHostConfig:        func() (config.Host, error) { return config.Host{WorktreeRoot: root}, nil },
	})
	target, err := resolver.Resolve(t.Context(), ResolveRequest{LinkedBoxID: record.ID, NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	_, direct := target.state.run.(directAdapter)
	if !direct || !target.state.direct || target.BoxID() != record.ID || target.BoxIdentity() != testIdentity || writableOpenCalls != 0 || !inventory.closed {
		t.Fatalf("target = %+v, direct adapter = %t, writable opens = %d, inventory closed = %t", target.state, direct, writableOpenCalls, inventory.closed)
	}
}

func TestResolverReadOnlyUsesReadOnlyInventoryOnly(t *testing.T) {
	record := box.Record{ID: "box-work", Name: "work", SSHDestination: "work-host", RemoteIdentity: testIdentity, RuntimePath: "/home/alice/.local/bin/schooner", WorktreeRoot: "/home/alice/schooner"}
	inventory := &memoryInventory{records: []box.Record{record}}
	writableOpenCalls := 0
	existingOpenCalls := 0
	readOnlyOpenCalls := 0
	resolver := NewResolver(Options{
		Direct: func() *host.Runtime { return nil },
		Remote: ssh.NewHost("ssh", nil, "dev", nil),
		OpenInventory: func(context.Context) (Inventory, error) {
			writableOpenCalls++
			return nil, errors.New("read-only resolution must not open writable inventory")
		},
		OpenExistingInventory: func(context.Context) (Inventory, bool, error) {
			existingOpenCalls++
			return nil, false, errors.New("read-only resolution must not open existing inventory as writable")
		},
		OpenReadOnlyInventory: func(context.Context) (Inventory, bool, error) {
			readOnlyOpenCalls++
			return inventory, true, nil
		},
	})
	target, err := resolver.Resolve(t.Context(), ResolveRequest{ExplicitBox: record.Name, NonInteractive: true, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	adapter, remote := target.state.run.(sshAdapter)
	if !remote || target.state.direct || adapter.connection.Destination != record.SSHDestination || writableOpenCalls != 0 || existingOpenCalls != 0 || readOnlyOpenCalls != 1 || !inventory.closed {
		t.Fatalf("target = %+v, adapter = %+v, opens = writable:%d existing:%d read-only:%d, closed = %t", target.state, adapter, writableOpenCalls, existingOpenCalls, readOnlyOpenCalls, inventory.closed)
	}
}

func TestResolverRejectsDirectInventoryRootDrift(t *testing.T) {
	home := t.TempDir()
	writeIdentity(t, home, testIdentity)
	inventory := &memoryInventory{records: []box.Record{{Name: "work", RemoteIdentity: testIdentity, WorktreeRoot: "/inventory"}}}
	resolver := NewResolver(Options{
		Direct:                func() *host.Runtime { return host.NewAtHome(hostruntime.BuildInfo{}, home) },
		Remote:                ssh.NewHost("ssh", nil, "dev", nil),
		OpenInventory:         func(context.Context) (Inventory, error) { return inventory, nil },
		OpenExistingInventory: func(context.Context) (Inventory, bool, error) { return inventory, true, nil },
		ReadHostConfig:        func() (config.Host, error) { return config.Host{WorktreeRoot: "/configured"}, nil },
	})
	_, err := resolver.Resolve(t.Context(), ResolveRequest{})
	if box.ErrorCode(err) != "conflict" || !strings.Contains(err.Error(), "box setup work") || !inventory.closed {
		t.Fatalf("error = %v, closed = %t", err, inventory.closed)
	}
}

func TestTargetValidatesRootsAndNormalizesAdapterErrors(t *testing.T) {
	adapter := &fakeExecutionAdapter{catalog: repository.Catalog{WorktreeRoot: "/other"}}
	target := Target{state: &targetState{boxName: "work", worktreeRoot: "/expected", run: adapter}}
	if target.BoxName() != "work" || (Target{}).BoxName() != "" {
		t.Fatalf("target names = %q, %q", target.BoxName(), (Target{}).BoxName())
	}
	if _, err := target.ListWorktrees(t.Context()); box.ErrorCode(err) != "conflict" || !strings.Contains(err.Error(), "box setup work") {
		t.Fatalf("root error = %v", err)
	}
	adapter.catalog.WorktreeRoot = "/expected"
	adapter.err = &repository.Error{Code: repository.CodeNotFound, Message: "missing Worktree", Context: map[string]string{"reason": "credentials_missing"}}
	if _, err := target.ListWorktrees(t.Context()); box.ErrorCode(err) != "not_found" || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("normalized error = %v", err)
	}
	if _, err := target.ListWorktrees(t.Context()); err.(*box.Error).Context["reason"] != "credentials_missing" {
		t.Fatalf("normalized error context = %+v", err)
	}
	if _, err := (Target{}).ListWorktrees(t.Context()); box.ErrorCode(err) != "internal" {
		t.Fatalf("zero target error = %v", err)
	}
}

func TestSourceCapabilityUnavailableNormalizesToUnsupported(t *testing.T) {
	err := normalizeSourceError(&box.Error{Code: "capability_unavailable", Message: "ssh-keygen is unavailable"})
	if source.ErrorCode(err) != "unsupported" || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("normalized error = %v", err)
	}
}

func writeIdentity(t *testing.T, home, identity string) {
	t.Helper()
	path := filepath.Join(home, ".local", "state", "schooner", "identity")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(identity+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

type memoryInventory struct {
	records []box.Record
	closed  bool
}

func (m *memoryInventory) FindByName(_ context.Context, name string) (box.Record, error) {
	for _, record := range m.records {
		if record.Name == name {
			return record, nil
		}
	}
	return box.Record{}, box.NotFound(name)
}

func (m *memoryInventory) FindByID(_ context.Context, id string) (box.Record, error) {
	for _, record := range m.records {
		if record.ID == id {
			return record, nil
		}
	}
	return box.Record{}, box.NotFound(id)
}

func (m *memoryInventory) List(context.Context) ([]box.Record, error) {
	return append([]box.Record(nil), m.records...), nil
}

func (m *memoryInventory) SetDefault(_ context.Context, name string) (box.Record, error) {
	return m.FindByName(context.Background(), name)
}

func (m *memoryInventory) Close() error { m.closed = true; return nil }

type fakeExecutionAdapter struct {
	catalog repository.Catalog
	err     error
}

func (f *fakeExecutionAdapter) listWorktrees(context.Context) (repository.Catalog, error) {
	return f.catalog, f.err
}
func (f *fakeExecutionAdapter) listContextWorktrees(context.Context) (repository.Catalog, error) {
	return f.catalog, f.err
}
func (*fakeExecutionAdapter) inspectWorktree(context.Context, string) (repository.Inspection, error) {
	return repository.Inspection{}, nil
}
func (*fakeExecutionAdapter) cloneRepository(context.Context, repository.CloneRequest) (repository.MutationResult, error) {
	return repository.MutationResult{}, nil
}
func (*fakeExecutionAdapter) addWorktree(context.Context, repository.AddRequest) (repository.MutationResult, error) {
	return repository.MutationResult{}, nil
}
func (*fakeExecutionAdapter) removeWorktree(context.Context, string) (repository.MutationResult, error) {
	return repository.MutationResult{}, nil
}
func (*fakeExecutionAdapter) pruneWorktrees(context.Context) (repository.MutationResult, error) {
	return repository.MutationResult{}, nil
}
func (*fakeExecutionAdapter) listSessions(context.Context) (session.Catalog, error) {
	return session.Catalog{}, nil
}
func (*fakeExecutionAdapter) startSession(context.Context, string) (session.StartResult, error) {
	return session.StartResult{}, nil
}
func (*fakeExecutionAdapter) resumeSession(context.Context, string, Terminal) (HandoffResult, error) {
	return HandoffResult{}, nil
}
func (*fakeExecutionAdapter) sessionLogs(context.Context, string, int) (session.LogsResult, error) {
	return session.LogsResult{}, nil
}
func (*fakeExecutionAdapter) stopSession(context.Context, string) (session.StopResult, error) {
	return session.StopResult{}, nil
}
func (*fakeExecutionAdapter) openWorktreeShell(context.Context, string, Terminal) (HandoffResult, error) {
	return HandoffResult{}, nil
}
func (*fakeExecutionAdapter) inspectSourceIdentity(context.Context, string) (source.HostIdentity, error) {
	return source.HostIdentity{}, nil
}
func (*fakeExecutionAdapter) ensureSourceIdentity(context.Context, source.EnsureIdentityRequest) (source.HostIdentity, error) {
	return source.HostIdentity{}, nil
}
func (*fakeExecutionAdapter) removeSourceIdentity(context.Context, source.RemoveIdentityRequest) (source.RemoveIdentityResult, error) {
	return source.RemoveIdentityResult{}, nil
}
func (*fakeExecutionAdapter) verifySourceRepository(context.Context, source.VerifyRequest) (source.VerifyResult, error) {
	return source.VerifyResult{}, nil
}
func (*fakeExecutionAdapter) observePushDestination(context.Context, string) (*repository.CheckoutState, error) {
	return nil, nil
}
func (*fakeExecutionAdapter) preflightPushDestination(context.Context, string, repository.CheckoutState, bool) (workspacetransfer.PreflightResult, error) {
	return workspacetransfer.PreflightResult{}, nil
}
func (*fakeExecutionAdapter) applyPush(context.Context, workspacetransfer.ApplyRequest, io.Reader) (workspacetransfer.ApplyResult, error) {
	return workspacetransfer.ApplyResult{}, nil
}
func (*fakeExecutionAdapter) inspectPullSource(context.Context, workspacetransfer.PullInspectRequest) (workspacetransfer.PullInspection, error) {
	return workspacetransfer.PullInspection{}, nil
}
func (*fakeExecutionAdapter) capturePullSource(context.Context, workspacetransfer.PullCaptureRequest) (workspacetransfer.PullCapture, error) {
	return workspacetransfer.PullCapture{}, nil
}
