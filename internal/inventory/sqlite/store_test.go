package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/acquisition"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/credentials"
	"github.com/thewelshrich/schooner/internal/link"
	"github.com/thewelshrich/schooner/internal/provider"
	"github.com/thewelshrich/schooner/internal/source"
)

func TestStoreLifecycleAndMigrationHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	op := box.AddOperation{Name: "work", SSHDestination: "work-host", WorktreeRoot: "~/schooner", UpdatedAt: now}
	if err = store.BeginAdd(t.Context(), op); err != nil {
		t.Fatalf("BeginAdd() error = %v", err)
	}
	op.RemoteIdentity, op.Checkpoint = "remote-1", box.StepIdentity
	if err = store.CheckpointAdd(t.Context(), op); err != nil {
		t.Fatalf("CheckpointAdd() error = %v", err)
	}
	record := box.Record{ID: "box-1", Name: "work", Acquisition: "adopted", SSHDestination: "work-host", RemoteIdentity: "remote-1", RuntimePath: "/home/alice/.local/bin/schooner", WorktreeRoot: "/home/alice/schooner", CreatedAt: now, UpdatedAt: now}
	observation := box.Observation{BoxID: record.ID, ObservedAt: now, Capabilities: box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64", Home: "/home/alice", RemoteIdentity: "remote-1", Git: box.Tool{Available: true, Version: "git version 2.43.0"}, Tmux: box.Tool{Available: true, Version: "tmux 3.4"}, PasswordlessSudo: true, Host: box.HostRuntime{Path: record.RuntimePath, Version: "v1.2.3", ProtocolVersion: "1", Capabilities: []string{"host.inspect.v2", "host.hello.v1"}}}}
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
	if err = store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 9 {
		t.Fatalf("migration count = %d, err = %v", count, err)
	}
}

func TestOpenReadOnlyMissingInventoryCreatesNothing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "state.db")
	store, exists, err := OpenReadOnly(t.Context(), path)
	if err != nil || exists || store != nil {
		t.Fatalf("OpenReadOnly() = %+v, %t, %v; want nil, false, nil", store, exists, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("OpenReadOnly() created entries: %+v", entries)
	}
	if _, err = os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("missing inventory directory stat error = %v; want not exist", err)
	}
}

func TestOpenReadOnlyReadsBoxesAndLocalLinksWithoutChangingInventory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	record := box.Record{ID: "box-1", Name: "work", Acquisition: "adopted", SSHDestination: "work-host", RemoteIdentity: "remote-1", RuntimePath: "/home/alice/.local/bin/schooner", WorktreeRoot: "/home/alice/schooner"}
	saveStoreBox(t, store, record)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	localLink := link.LocalLink{LocalWorktree: "/local/repo", BoxID: record.ID, ExpectedBoxIdentity: record.RemoteIdentity, RemoteWorktree: "/home/alice/schooner/repo", RepositoryIdentity: "github.com/owner/repo", CreatedAt: now, UpdatedAt: now}
	if err = store.SaveLocalLink(t.Context(), localLink); err != nil {
		t.Fatal(err)
	}
	result, err := store.db.ExecContext(t.Context(), `DELETE FROM schema_migrations WHERE version=8`)
	if err != nil {
		t.Fatal(err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		t.Fatalf("removed migration rows = %d, %v; want 1, nil", affected, affectedErr)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	before := inventoryPersistentTreeSnapshot(t, root)
	readOnly, exists, err := OpenReadOnly(t.Context(), path)
	if err != nil || !exists || readOnly == nil {
		t.Fatalf("OpenReadOnly() = %+v, %t, %v", readOnly, exists, err)
	}
	var migrationCount int
	if err = readOnly.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil || migrationCount != 8 {
		t.Fatalf("migration count = %d, %v; want 8, nil", migrationCount, err)
	}
	gotRecord, err := readOnly.FindByID(t.Context(), record.ID)
	if err != nil || gotRecord.Name != record.Name || gotRecord.RemoteIdentity != record.RemoteIdentity {
		t.Fatalf("FindByID() = %+v, %v", gotRecord, err)
	}
	gotLink, err := readOnly.FindLocalLink(t.Context(), localLink.LocalWorktree)
	if err != nil || gotLink != localLink {
		t.Fatalf("FindLocalLink() = %+v, %v; want %+v, nil", gotLink, err, localLink)
	}
	if _, err = readOnly.db.ExecContext(t.Context(), `DELETE FROM local_links`); err == nil {
		t.Fatal("read-only inventory accepted a logical write")
	}
	if err = readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	assertInventoryPersistentTreeUnchanged(t, root, before)
}

func TestOpenReadOnlyReadsCommittedWALRecords(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.db.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(t.Context(), `PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}

	record := box.Record{ID: "box-wal", Name: "wal", Acquisition: "adopted", SSHDestination: "wal-host", RemoteIdentity: "remote-wal", RuntimePath: "/home/alice/.local/bin/schooner", WorktreeRoot: "/home/alice/schooner"}
	saveStoreBox(t, store, record)
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	want := link.LocalLink{LocalWorktree: "/local/wal", BoxID: record.ID, ExpectedBoxIdentity: record.RemoteIdentity, RemoteWorktree: "/home/alice/schooner/wal", RepositoryIdentity: "github.com/owner/wal", CreatedAt: now, UpdatedAt: now}
	if err = store.SaveLocalLink(t.Context(), want); err != nil {
		t.Fatal(err)
	}

	before := inventoryPersistentTreeSnapshot(t, root)
	readOnly, exists, err := OpenReadOnly(t.Context(), path)
	if err != nil || !exists || readOnly == nil {
		t.Fatalf("OpenReadOnly() = %+v, %t, %v", readOnly, exists, err)
	}
	got, err := readOnly.FindLocalLink(t.Context(), want.LocalWorktree)
	if err != nil || got != want {
		t.Fatalf("FindLocalLink() = %+v, %v; want %+v, nil", got, err, want)
	}
	if _, err = readOnly.db.ExecContext(t.Context(), `DELETE FROM local_links`); err == nil {
		t.Fatal("read-only inventory accepted a logical write")
	}
	if err = readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	assertInventoryPersistentTreeUnchanged(t, root, before)
}

func TestDefaultBoxSwitchingAndRemoval(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	saveStoreBox(t, store, box.Record{ID: "box-alpha", Name: "alpha", Acquisition: "adopted", SSHDestination: "alpha", RemoteIdentity: "remote-alpha", WorktreeRoot: "/home/alice/schooner"})
	saveStoreBox(t, store, box.Record{ID: "box-beta", Name: "beta", Acquisition: "adopted", SSHDestination: "beta", RemoteIdentity: "remote-beta", WorktreeRoot: "/home/alice/schooner"})

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

func TestLocalLinkPersistsStableBoxRoutingAcrossRenameAndRemoval(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	saveStoreBox(t, store, box.Record{ID: "box-1", Name: "before-rename", Acquisition: "adopted", SSHDestination: "box-host", RemoteIdentity: "remote-1", WorktreeRoot: "/home/alice/schooner"})
	value := link.LocalLink{LocalWorktree: "/local/repo", BoxID: "box-1", ExpectedBoxIdentity: "remote-1", RemoteWorktree: "/home/alice/schooner/repo", RepositoryIdentity: "github.com/owner/repo", CreatedAt: now, UpdatedAt: now}
	if err = store.SaveLocalLink(t.Context(), value); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(t.Context(), `UPDATE boxes SET name='after-rename' WHERE id=?`, value.BoxID); err != nil {
		t.Fatal(err)
	}
	got, err := store.FindLocalLink(t.Context(), value.LocalWorktree)
	if err != nil || got != value {
		t.Fatalf("link after Box rename = %+v, %v", got, err)
	}
	if _, err = store.Remove(t.Context(), "after-rename"); err != nil {
		t.Fatal(err)
	}
	got, err = store.FindLocalLink(t.Context(), value.LocalWorktree)
	if err != nil || got != value {
		t.Fatalf("link after Box removal = %+v, %v", got, err)
	}
	value.RemoteWorktree = "/home/alice/schooner/repo-2"
	value.UpdatedAt = now.Add(time.Hour)
	if err = store.SaveLocalLink(t.Context(), value); err != nil {
		t.Fatal(err)
	}
	got, err = store.FindLocalLink(t.Context(), value.LocalWorktree)
	if err != nil || got != value {
		t.Fatalf("updated link = %+v, %v", got, err)
	}
}

func TestSourceAccountAndBoxIdentitySurviveBoxRemoval(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	account := source.Account{Provider: source.GitHub, ExternalID: "42", Login: "octocat", CredentialKey: "opaque", CredentialGeneration: "generation", Status: "connected", CreatedAt: now, UpdatedAt: now}
	if err = store.SaveSourceAccount(t.Context(), account); err != nil {
		t.Fatal(err)
	}
	identity := source.BoxIdentity{BoxIdentity: "remote-source", BoxName: "work", Provider: source.GitHub, AccountExternalID: "42", Fingerprint: "SHA256:test", RemoteKeyID: 99, RemoteKeyTitle: "Schooner / work", State: source.StateConnected, CreatedAt: now, UpdatedAt: now}
	if err = store.SaveBoxSourceIdentity(t.Context(), identity); err != nil {
		t.Fatal(err)
	}
	gotAccount, err := store.FindSourceAccount(t.Context(), source.GitHub)
	if err != nil || gotAccount.Login != "octocat" || gotAccount.CredentialKey != "opaque" || gotAccount.CredentialGeneration != "generation" {
		t.Fatalf("account=%+v err=%v", gotAccount, err)
	}
	gotIdentity, err := store.FindBoxSourceIdentityByName(t.Context(), "work", source.GitHub)
	if err != nil || gotIdentity.BoxIdentity != "remote-source" || gotIdentity.State != source.StateConnected {
		t.Fatalf("identity=%+v err=%v", gotIdentity, err)
	}

	saveStoreBox(t, store, box.Record{ID: "box-source", Name: "work", Acquisition: "adopted", SSHDestination: "work", RemoteIdentity: "remote-source", WorktreeRoot: "/home/alice/schooner"})
	if _, err = store.Remove(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FindBoxSourceIdentity(t.Context(), "remote-source", source.GitHub); err != nil {
		t.Fatalf("source identity should survive ordinary Box removal: %v", err)
	}
	if err = store.DeleteBoxSourceIdentity(t.Context(), "remote-source", source.GitHub); err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteSourceAccount(t.Context(), source.GitHub); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FindSourceAccount(t.Context(), source.GitHub); !source.IsNotFound(err) {
		t.Fatalf("source account survived delete: %v", err)
	}
}

func TestSourceTablesContainOnlySafeCorrelationMetadata(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, table := range []string{"source_accounts", "box_source_identities"} {
		var schema string
		if err = store.db.QueryRowContext(t.Context(), `SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&schema); err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(schema)
		for _, forbidden := range []string{"access_token", "refresh_token", "public_key", "private_key", "filesystem_path", "known_hosts"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("%s schema contains forbidden source material %q: %s", table, forbidden, schema)
			}
		}
	}
}

func TestFormerBoxNameLookupFailsClosedWhenRetainedBindingsAreAmbiguous(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	for index, identity := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		if err = store.SaveBoxSourceIdentity(t.Context(), source.BoxIdentity{
			BoxIdentity: identity, BoxName: "former", Provider: source.GitHub,
			AccountExternalID: "42", Fingerprint: fmt.Sprintf("SHA256:key-%d", index),
			State: source.StateConnected, CreatedAt: now, UpdatedAt: now.Add(time.Duration(index) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.FindBoxSourceIdentityByName(t.Context(), "former", source.GitHub); source.ErrorCode(err) != "conflict" {
		t.Fatalf("err = %v", err)
	}
}

func TestStoreRejectsChangedRecoveryInput(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.BeginAdd(t.Context(), box.AddOperation{Name: "work", SSHDestination: "one", WorktreeRoot: "~/schooner", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	err = store.BeginAdd(t.Context(), box.AddOperation{Name: "work", SSHDestination: "two", WorktreeRoot: "~/schooner", UpdatedAt: time.Now()})
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
	op := acquisition.ProvisionOperation{Name: "work", CorrelationID: "op-1", Profile: "digitalocean/personal", Region: "fra1", Size: "small", Image: "ubuntu-24-04-x64", LocalPublicKeys: []provider.PublicKey{key}, WorktreeRoot: box.DefaultWorktreeRoot, UpdatedAt: time.Now().UTC()}
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
	op := box.AddOperation{Name: "cloud", SSHDestination: "root@203.0.113.8", WorktreeRoot: box.DefaultWorktreeRoot, UpdatedAt: now}
	if err = store.BeginAdd(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	record := box.Record{ID: "box-cloud", Name: "cloud", Acquisition: "provisioned", SSHDestination: op.SSHDestination, IdentityFile: "/state/id_ed25519", RemoteIdentity: "remote-cloud", WorktreeRoot: "/root/schooner", Provider: "digitalocean", ProviderResourceID: "42", ProviderCorrelationID: "op-1", CredentialProfile: string(profile.Ref), CreatedAt: now, UpdatedAt: now}
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
	op := box.AddOperation{Name: record.Name, SSHDestination: record.SSHDestination, WorktreeRoot: record.WorktreeRoot, UpdatedAt: now}
	if err := store.BeginAdd(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	observation := box.Observation{BoxID: record.ID, ObservedAt: now, Capabilities: box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64"}}
	if err := store.CompleteAdd(t.Context(), op, record, observation); err != nil {
		t.Fatal(err)
	}
}

func inventoryTreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			snapshot[relative] = "directory:" + info.Mode().String()
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[relative] = fmt.Sprintf("file:%s:%x", info.Mode(), sha256.Sum256(contents))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func inventoryPersistentTreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := inventoryTreeSnapshot(t, root)
	for name := range snapshot {
		if strings.HasSuffix(name, "-shm") {
			delete(snapshot, name)
			continue
		}
		if strings.HasSuffix(name, "-wal") {
			info, err := os.Stat(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() == 0 {
				delete(snapshot, name)
			}
		}
	}
	return snapshot
}

func assertInventoryPersistentTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := inventoryPersistentTreeSnapshot(t, root)
	if !maps.Equal(before, after) {
		t.Fatalf("read-only inventory changed persistent filesystem tree\nbefore: %+v\nafter:  %+v", before, after)
	}
}
