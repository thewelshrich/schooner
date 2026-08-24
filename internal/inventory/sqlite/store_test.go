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
	if err = store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 3 {
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

func TestProvisionOperationPersistsSelectedLocalPublicKeys(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := provider.PublicKey{Name: "id_ed25519", Fingerprint: "SHA256:local", PublicKey: "ssh-ed25519 AAAA"}
	op := acquisition.ProvisionOperation{Name: "work", CorrelationID: "op-1", Profile: "digitalocean/personal", Region: "fra1", Size: "small", Image: "ubuntu-24-04-x64", LocalPublicKeys: []provider.PublicKey{key}, ProjectRoot: box.DefaultProjectRoot, UpdatedAt: time.Now().UTC()}
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
	op := box.AddOperation{Name: "cloud", SSHDestination: "root@203.0.113.8", ProjectRoot: box.DefaultProjectRoot, UpdatedAt: now}
	if err = store.BeginAdd(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	record := box.Record{ID: "box-cloud", Name: "cloud", Acquisition: "provisioned", SSHDestination: op.SSHDestination, IdentityFile: "/state/id_ed25519", RemoteIdentity: "remote-cloud", ProjectRoot: "/root/schooner", Provider: "digitalocean", ProviderResourceID: "42", ProviderCorrelationID: "op-1", CredentialProfile: string(profile.Ref), CreatedAt: now, UpdatedAt: now}
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
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.FindByName(t.Context(), "legacy")
	if err != nil || record.Acquisition != "adopted" || record.Provider != "" || record.IdentityFile != "" {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if _, err = os.Stat(path + ".pre-v2-backup"); err != nil {
		t.Fatal(err)
	}
}
