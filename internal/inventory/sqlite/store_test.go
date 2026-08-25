package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/acquisition"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/credentials"
	"github.com/thewelshrich/schooner/internal/provider"
)

func TestStoreLifecycleAndMigrationHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	op := box.AddOperation{Name: "work", SSHDestination: "work-host", WorkspaceRoot: "~/schooner", UpdatedAt: now}
	if err = store.BeginAdd(t.Context(), op); err != nil {
		t.Fatalf("BeginAdd() error = %v", err)
	}
	op.RemoteIdentity, op.Checkpoint = "remote-1", box.StepIdentity
	if err = store.CheckpointAdd(t.Context(), op); err != nil {
		t.Fatalf("CheckpointAdd() error = %v", err)
	}
	record := box.Record{ID: "box-1", Name: "work", Acquisition: "adopted", SSHDestination: "work-host", RemoteIdentity: "remote-1", RuntimePath: "/home/alice/.local/bin/schooner", WorkspaceRoot: "/home/alice/schooner", CreatedAt: now, UpdatedAt: now}
	observation := box.Observation{BoxID: record.ID, ObservedAt: now, Capabilities: box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64", Home: "/home/alice", RemoteIdentity: "remote-1", Git: box.Tool{Available: true, Version: "git version 2.43.0"}, Tmux: box.Tool{Available: true, Version: "tmux 3.4"}, PasswordlessSudo: true, Host: box.HostRuntime{Path: record.RuntimePath, Version: "v1.2.3", ProtocolVersion: "1", Capabilities: []string{"host.inspect.v1", "host.hello.v1"}}}}
	if err = store.CompleteAdd(t.Context(), op, record, observation); err != nil {
		t.Fatalf("CompleteAdd() error = %v", err)
	}
	got, err := store.FindByName(t.Context(), "work")
	if err != nil || got.RemoteIdentity != "remote-1" || got.RuntimePath != record.RuntimePath {
		t.Fatalf("FindByName() = %+v, %v", got, err)
	}
	last, err := store.LastObservation(t.Context(), record.ID)
	if err != nil || last.Capabilities.Tmux.Version != "tmux 3.4" || last.Capabilities.Host.Version != "v1.2.3" || len(last.Capabilities.Host.Capabilities) != 2 || last.Capabilities.Host.Capabilities[0] != "host.hello.v1" {
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
	if err = store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 6 {
		t.Fatalf("migration count = %d, err = %v", count, err)
	}
}

func TestDefaultBoxSwitchingAndRemoval(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	saveStoreBox(t, store, box.Record{ID: "box-alpha", Name: "alpha", Acquisition: "adopted", SSHDestination: "alpha", RemoteIdentity: "remote-alpha", WorkspaceRoot: "/home/alice/schooner"})
	saveStoreBox(t, store, box.Record{ID: "box-beta", Name: "beta", Acquisition: "adopted", SSHDestination: "beta", RemoteIdentity: "remote-beta", WorkspaceRoot: "/home/alice/schooner"})

	selected, err := store.SetDefault(t.Context(), "alpha")
	if err != nil || !selected.Default {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
	if _, err = store.SetDefault(t.Context(), "missing"); !box.IsNotFound(err) {
		t.Fatalf("missing error=%v", err)
	}
	alpha, err := store.FindByID(t.Context(), "box-alpha")
	if err != nil || !alpha.Default {
		t.Fatalf("alpha=%+v err=%v", alpha, err)
	}
	if _, err = store.SetDefault(t.Context(), "beta"); err != nil {
		t.Fatal(err)
	}
	alpha, _ = store.FindByName(t.Context(), "alpha")
	beta, _ := store.FindByName(t.Context(), "beta")
	if alpha.Default || !beta.Default {
		t.Fatalf("alpha=%+v beta=%+v", alpha, beta)
	}
	if _, err = store.SetDefault(t.Context(), "alpha"); err != nil {
		t.Fatalf("switch default back to earlier row: %v", err)
	}
	if _, err = store.SetDefault(t.Context(), "beta"); err != nil {
		t.Fatalf("restore beta default: %v", err)
	}
	if _, err = store.Remove(t.Context(), "beta"); err != nil {
		t.Fatal(err)
	}
	alpha, _ = store.FindByName(t.Context(), "alpha")
	if alpha.Default {
		t.Fatalf("default was promoted after removal: %+v", alpha)
	}
}

func TestStoreRejectsChangedRecoveryInput(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.BeginAdd(t.Context(), box.AddOperation{Name: "work", SSHDestination: "one", WorkspaceRoot: "~/schooner", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	err = store.BeginAdd(t.Context(), box.AddOperation{Name: "work", SSHDestination: "two", WorkspaceRoot: "~/schooner", UpdatedAt: time.Now()})
	if box.ErrorCode(err) != "conflict" {
		t.Fatalf("error = %v", err)
	}
}

func TestProvisionOperationPersistsSelectedLocalPublicKeys(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := provider.PublicKey{Name: "id_ed25519", Fingerprint: "SHA256:local", PublicKey: "ssh-ed25519 AAAA"}
	op := acquisition.ProvisionOperation{Name: "work", CorrelationID: "op-1", Profile: "digitalocean/personal", Region: "fra1", Size: "small", Image: "ubuntu-24-04-x64", LocalPublicKeys: []provider.PublicKey{key}, WorkspaceRoot: box.DefaultWorkspaceRoot, UpdatedAt: time.Now().UTC()}
	if _, err = store.BeginProvision(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	got, err := store.FindProvision(t.Context(), op.Name)
	if err != nil || len(got.LocalPublicKeys) != 1 || got.LocalPublicKeys[0] != key {
		t.Fatalf("operation=%+v err=%v", got, err)
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

func TestProviderMigrationBackupAndStructuredRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if info, statErr := os.Stat(path + ".pre-v2-backup"); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup info=%v err=%v", info, statErr)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	profile := credentials.Profile{Ref: "digitalocean/personal", Provider: provider.DigitalOcean, Name: "personal", ExternalID: "team-1", AccountName: "Personal", CredentialKey: "provider:digitalocean:credential:test", Default: true, CreatedAt: now, UpdatedAt: now}
	if err = store.SaveCredentialProfile(t.Context(), profile); err != nil {
		t.Fatal(err)
	}
	gotProfile, err := store.FindCredentialProfile(t.Context(), profile.Ref)
	if err != nil || !gotProfile.Default || gotProfile.ExternalID != "team-1" {
		t.Fatalf("profile=%+v err=%v", gotProfile, err)
	}
	op := box.AddOperation{Name: "cloud", SSHDestination: "root@203.0.113.8", WorkspaceRoot: box.DefaultWorkspaceRoot, UpdatedAt: now}
	if err = store.BeginAdd(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	record := box.Record{ID: "box-cloud", Name: "cloud", Acquisition: "provisioned", SSHDestination: op.SSHDestination, IdentityFile: "/state/id_ed25519", RemoteIdentity: "remote-cloud", WorkspaceRoot: "/root/schooner", Provider: "digitalocean", ProviderResourceID: "42", ProviderCorrelationID: "op-1", CredentialProfile: string(profile.Ref), CreatedAt: now, UpdatedAt: now}
	observation := box.Observation{BoxID: record.ID, ObservedAt: now, Capabilities: box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64"}}
	if err = store.CompleteAdd(t.Context(), op, record, observation); err != nil {
		t.Fatal(err)
	}
	got, err := store.FindByName(t.Context(), "cloud")
	if err != nil || got.ProviderResourceID != "42" || got.IdentityFile != "/state/id_ed25519" {
		t.Fatalf("record=%+v err=%v", got, err)
	}
}

func TestProviderMigrationPreservesVersionOneAdoptedBox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := migrations.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(initial)); err != nil {
		t.Fatal(err)
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(initial))
	if _, err = db.Exec(`INSERT INTO schema_migrations VALUES(1,?,'2026-08-24T12:00:00Z')`, checksum); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO boxes VALUES('box-1','legacy','adopted','legacy-host','remote-1','/home/alice/schooner','2026-08-24T12:00:00Z','2026-08-24T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO observations VALUES('box-1','2026-08-24T12:00:00Z','ubuntu','24.04','amd64','/home/alice','remote-1','/home/alice/schooner',1,1,'git version 2.43.0',1,'tmux 3.4',1)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.FindByName(t.Context(), "legacy")
	if err != nil || record.Acquisition != "adopted" || record.Provider != "" || record.IdentityFile != "" || record.RuntimePath != "/home/alice/.local/bin/schooner" {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	observation, err := store.LastObservation(t.Context(), record.ID)
	if err != nil || observation.Capabilities.Host.Path != record.RuntimePath || observation.Capabilities.Host.Version != "" {
		t.Fatalf("observation=%+v err=%v", observation, err)
	}
	if _, err = os.Stat(path + ".pre-v2-backup"); err != nil {
		t.Fatal(err)
	}
}

func saveStoreBox(t *testing.T, store *Store, record box.Record) {
	t.Helper()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	record.CreatedAt, record.UpdatedAt = now, now
	op := box.AddOperation{Name: record.Name, SSHDestination: record.SSHDestination, WorkspaceRoot: record.WorkspaceRoot, UpdatedAt: now}
	if err := store.BeginAdd(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	observation := box.Observation{BoxID: record.ID, ObservedAt: now, Capabilities: box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64"}}
	if err := store.CompleteAdd(t.Context(), op, record, observation); err != nil {
		t.Fatal(err)
	}
}
