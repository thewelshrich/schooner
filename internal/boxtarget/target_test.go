package boxtarget

import (
	"context"
	"errors"
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
	adapter.err = &repository.Error{Code: repository.CodeNotFound, Message: "missing Worktree"}
	if _, err := target.ListWorktrees(t.Context()); box.ErrorCode(err) != "not_found" || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("normalized error = %v", err)
	}
	if _, err := (Target{}).ListWorktrees(t.Context()); box.ErrorCode(err) != "internal" {
		t.Fatalf("zero target error = %v", err)
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
