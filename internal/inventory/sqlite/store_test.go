package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/box"
)

func TestStoreLifecycleAndMigrationHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	op := box.AddOperation{Name: "work", SSHDestination: "work-host", ProjectRoot: "~/schooner", UpdatedAt: now}
	if err = store.BeginAdd(t.Context(), op); err != nil {
		t.Fatalf("BeginAdd() error = %v", err)
	}
	op.RemoteIdentity, op.Checkpoint = "remote-1", box.StepIdentity
	if err = store.CheckpointAdd(t.Context(), op); err != nil {
		t.Fatalf("CheckpointAdd() error = %v", err)
	}
	record := box.Record{ID: "box-1", Name: "work", Acquisition: "adopted", SSHDestination: "work-host", RemoteIdentity: "remote-1", ProjectRoot: "/home/alice/schooner", CreatedAt: now, UpdatedAt: now}
	observation := box.Observation{BoxID: record.ID, ObservedAt: now, Capabilities: box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64", Home: "/home/alice", RemoteIdentity: "remote-1", Git: box.Tool{Available: true, Version: "git version 2.43.0"}, Tmux: box.Tool{Available: true, Version: "tmux 3.4"}, PasswordlessSudo: true}}
	if err = store.CompleteAdd(t.Context(), op, record, observation); err != nil {
		t.Fatalf("CompleteAdd() error = %v", err)
	}
	got, err := store.FindByName(t.Context(), "work")
	if err != nil || got.RemoteIdentity != "remote-1" {
		t.Fatalf("FindByName() = %+v, %v", got, err)
	}
	last, err := store.LastObservation(t.Context(), record.ID)
	if err != nil || last.Capabilities.Tmux.Version != "tmux 3.4" {
		t.Fatalf("LastObservation() = %+v, %v", last, err)
	}
	removed, err := store.Remove(t.Context(), "work")
	if err != nil || removed.Name != "work" {
		t.Fatalf("Remove() = %+v, %v", removed, err)
	}
	if _, err = store.LastObservation(t.Context(), record.ID); !box.IsNotFound(err) {
		t.Fatalf("observation survived remove: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen with applied migrations: %v", err)
	}
	defer store.Close()
	var count int
	if err = store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration count = %d, err = %v", count, err)
	}
}

func TestStoreRejectsChangedRecoveryInput(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.BeginAdd(t.Context(), box.AddOperation{Name: "work", SSHDestination: "one", ProjectRoot: "~/schooner", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	err = store.BeginAdd(t.Context(), box.AddOperation{Name: "work", SSHDestination: "two", ProjectRoot: "~/schooner", UpdatedAt: time.Now()})
	if box.ErrorCode(err) != "conflict" {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(t.Context(), `INSERT INTO schema_migrations(version,checksum,applied_at) VALUES(999,'future','2026-08-24T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(t.Context(), path); err == nil {
		t.Fatal("newer inventory schema was accepted")
	}
}
