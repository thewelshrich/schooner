package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/thewelshrich/schooner/internal/acquisition"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/credentials"
	"github.com/thewelshrich/schooner/internal/provider"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db   *sql.DB
	path string
}

func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create Schooner state directory: %w", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open inventory: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, path: path}
	if err := store.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initialize(ctx context.Context) error {
	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL", "PRAGMA busy_timeout = 5000"} {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure inventory: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL) STRICT`); err != nil {
		return fmt.Errorf("initialize migrations: %w", err)
	}
	entries, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)
	latestKnown := 0
	if len(entries) > 0 {
		base := filepath.Base(entries[len(entries)-1])
		latestKnown, err = strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("invalid migration name %s", entries[len(entries)-1])
		}
	}
	var latestApplied sql.NullInt64
	if err = s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&latestApplied); err != nil {
		return fmt.Errorf("inspect migration history: %w", err)
	}
	if latestApplied.Valid && latestApplied.Int64 > int64(latestKnown) {
		return fmt.Errorf("inventory schema version %d is newer than this Schooner build supports", latestApplied.Int64)
	}
	for _, name := range entries {
		contents, readErr := migrations.ReadFile(name)
		if readErr != nil {
			return readErr
		}
		base := filepath.Base(name)
		version, parseErr := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if parseErr != nil {
			return fmt.Errorf("invalid migration name %s", name)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(contents))
		var recorded string
		err = s.db.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE version = ?`, version).Scan(&recorded)
		if err == nil {
			if recorded != checksum {
				return fmt.Errorf("migration %d checksum does not match applied history", version)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if version == 2 {
			if err = s.backupBeforeProviderMigration(ctx); err != nil {
				return err
			}
		}
		tx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return txErr
		}
		if _, txErr = tx.ExecContext(ctx, string(contents)); txErr == nil {
			_, txErr = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, checksum, applied_at) VALUES(?, ?, ?)`, version, checksum, time.Now().UTC().Format(time.RFC3339Nano))
		}
		if txErr != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, txErr)
		}
		if txErr = tx.Commit(); txErr != nil {
			return txErr
		}
	}
	return nil
}

func (s *Store) backupBeforeProviderMigration(ctx context.Context) error {
	backup := s.path + ".pre-v2-backup"
	if _, err := os.Stat(backup); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect inventory backup: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, backup); err != nil {
		return fmt.Errorf("back up inventory before provider migration: %w", err)
	}
	if err := os.Chmod(backup, 0o600); err != nil {
		return fmt.Errorf("protect inventory backup: %w", err)
	}
	return nil
}

func (s *Store) FindByName(ctx context.Context, name string) (box.Record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, selectRecord+` WHERE name = ?`, name), name)
}

func (s *Store) FindByID(ctx context.Context, id string) (box.Record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, selectRecord+` WHERE id = ?`, id), id)
}

func (s *Store) FindByRemoteIdentity(ctx context.Context, identity string) (box.Record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, selectRecord+` WHERE remote_identity = ?`, identity), identity)
}

func (s *Store) List(ctx context.Context) ([]box.Record, error) {
	rows, err := s.db.QueryContext(ctx, selectRecord+` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list boxes: %w", err)
	}
	defer rows.Close()
	var records []box.Record
	for rows.Next() {
		record, scanErr := scanRecord(rows, "")
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) SetDefault(ctx context.Context, name string) (box.Record, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return box.Record{}, err
	}
	defer tx.Rollback()
	record, err := scanRecord(tx.QueryRowContext(ctx, selectRecord+` WHERE name = ?`, name), name)
	if err != nil {
		return box.Record{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE boxes SET is_default = CASE WHEN name = ? THEN 1 ELSE 0 END`, name); err != nil {
		return box.Record{}, fmt.Errorf("set default box: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return box.Record{}, err
	}
	record.Default = true
	return record, nil
}

func (s *Store) BeginAdd(ctx context.Context, op box.AddOperation) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO add_operations(name, ssh_destination, workspace_root, checkpoint, remote_identity, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET ssh_destination=excluded.ssh_destination, workspace_root=excluded.workspace_root, updated_at=excluded.updated_at
		WHERE add_operations.ssh_destination=excluded.ssh_destination AND add_operations.workspace_root=excluded.workspace_root`, op.Name, op.SSHDestination, op.WorkspaceRoot, op.Checkpoint, op.RemoteIdentity, formatTime(op.UpdatedAt))
	if err != nil {
		return fmt.Errorf("record add operation: %w", err)
	}
	var destination, root string
	if err = s.db.QueryRowContext(ctx, `SELECT ssh_destination, workspace_root FROM add_operations WHERE name=?`, op.Name).Scan(&destination, &root); err != nil {
		return err
	}
	if destination != op.SSHDestination || root != op.WorkspaceRoot {
		return &box.Error{Code: "conflict", Message: fmt.Sprintf("an interrupted add for %q uses different connection details", op.Name)}
	}
	return nil
}

func (s *Store) CheckpointAdd(ctx context.Context, op box.AddOperation) error {
	_, err := s.db.ExecContext(ctx, `UPDATE add_operations SET checkpoint=?, remote_identity=?, updated_at=? WHERE name=?`, op.Checkpoint, op.RemoteIdentity, formatTime(op.UpdatedAt), op.Name)
	if err != nil {
		return fmt.Errorf("checkpoint add operation: %w", err)
	}
	return nil
}

func (s *Store) CompleteAdd(ctx context.Context, op box.AddOperation, record box.Record, observation box.Observation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var providerID, resourceID, correlationID, profile any
	region := record.ProviderRegion
	if record.Acquisition == "provisioned" {
		providerID, resourceID, correlationID, profile = record.Provider, record.ProviderResourceID, record.ProviderCorrelationID, record.CredentialProfile
	} else {
		region = ""
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO boxes(id,name,acquisition,ssh_destination,identity_file,remote_identity,runtime_path,workspace_root,provider,provider_resource_id,provider_correlation_id,credential_profile,provider_region,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, record.ID, record.Name, record.Acquisition, record.SSHDestination, record.IdentityFile, record.RemoteIdentity, record.RuntimePath, record.WorkspaceRoot, providerID, resourceID, correlationID, profile, region, formatTime(record.CreatedAt), formatTime(record.UpdatedAt))
	if err == nil {
		err = saveObservation(ctx, tx, observation)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM add_operations WHERE name=?`, op.Name)
	}
	if err != nil {
		return mapConflict("save box", err)
	}
	return tx.Commit()
}

func (s *Store) SaveObservation(ctx context.Context, observation box.Observation) error {
	return saveObservation(ctx, s.db, observation)
}

func (s *Store) UpdateRuntimePath(ctx context.Context, boxID, runtimePath string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE boxes SET runtime_path=? WHERE id=?`, runtimePath, boxID)
	if err != nil {
		return fmt.Errorf("update host runtime path: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm host runtime path update: %w", err)
	}
	if updated == 0 {
		return box.NotFound(boxID)
	}
	return nil
}

func (s *Store) LastObservation(ctx context.Context, boxID string) (box.Observation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT observed_at,os_id,os_version,architecture,home,remote_identity,workspace_root,workspace_root_exists,git_available,git_version,tmux_available,tmux_version,passwordless_sudo,host_runtime_path,host_runtime_version,host_protocol_version,host_capabilities_json FROM observations WHERE box_id=?`, boxID)
	var result box.Observation
	var observed string
	var hostCapabilities string
	result.BoxID = boxID
	err := row.Scan(&observed, &result.Capabilities.OSID, &result.Capabilities.OSVersion, &result.Capabilities.Architecture, &result.Capabilities.Home, &result.Capabilities.RemoteIdentity, &result.Capabilities.WorkspaceRoot, &result.Capabilities.WorkspaceRootExists, &result.Capabilities.Git.Available, &result.Capabilities.Git.Version, &result.Capabilities.Tmux.Available, &result.Capabilities.Tmux.Version, &result.Capabilities.PasswordlessSudo, &result.Capabilities.Host.Path, &result.Capabilities.Host.Version, &result.Capabilities.Host.ProtocolVersion, &hostCapabilities)
	if errors.Is(err, sql.ErrNoRows) {
		return box.Observation{}, box.NotFound(boxID)
	}
	if err != nil {
		return box.Observation{}, err
	}
	if err = json.Unmarshal([]byte(hostCapabilities), &result.Capabilities.Host.Capabilities); err != nil {
		return box.Observation{}, fmt.Errorf("decode cached host capabilities: %w", err)
	}
	result.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
	return result, err
}

func (s *Store) Remove(ctx context.Context, name string) (box.Record, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return box.Record{}, err
	}
	defer tx.Rollback()
	record, err := scanRecord(tx.QueryRowContext(ctx, selectRecord+` WHERE name=?`, name), name)
	if err != nil {
		return box.Record{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM add_operations WHERE name=?`, name); err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM boxes WHERE name=?`, name)
	}
	if err != nil {
		return box.Record{}, err
	}
	if err = tx.Commit(); err != nil {
		return box.Record{}, err
	}
	return record, nil
}

const selectRecord = `SELECT id,name,acquisition,ssh_destination,identity_file,remote_identity,runtime_path,workspace_root,COALESCE(provider,''),COALESCE(provider_resource_id,''),COALESCE(provider_correlation_id,''),COALESCE(credential_profile,''),COALESCE(provider_region,''),is_default,created_at,updated_at FROM boxes`

type scanner interface{ Scan(...any) error }

func scanRecord(row scanner, key string) (box.Record, error) {
	var result box.Record
	var created, updated string
	err := row.Scan(&result.ID, &result.Name, &result.Acquisition, &result.SSHDestination, &result.IdentityFile, &result.RemoteIdentity, &result.RuntimePath, &result.WorkspaceRoot, &result.Provider, &result.ProviderResourceID, &result.ProviderCorrelationID, &result.CredentialProfile, &result.ProviderRegion, &result.Default, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return box.Record{}, box.NotFound(key)
	}
	if err != nil {
		return box.Record{}, err
	}
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err == nil {
		result.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	}
	return result, err
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func saveObservation(ctx context.Context, db executor, o box.Observation) error {
	c := o.Capabilities
	hostCapabilities := append([]string(nil), c.Host.Capabilities...)
	sort.Strings(hostCapabilities)
	encodedCapabilities, err := json.Marshal(hostCapabilities)
	if err != nil {
		return fmt.Errorf("encode host capabilities: %w", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO observations(box_id,observed_at,os_id,os_version,architecture,home,remote_identity,workspace_root,workspace_root_exists,git_available,git_version,tmux_available,tmux_version,passwordless_sudo,host_runtime_path,host_runtime_version,host_protocol_version,host_capabilities_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(box_id) DO UPDATE SET observed_at=excluded.observed_at,os_id=excluded.os_id,os_version=excluded.os_version,architecture=excluded.architecture,home=excluded.home,remote_identity=excluded.remote_identity,workspace_root=excluded.workspace_root,workspace_root_exists=excluded.workspace_root_exists,git_available=excluded.git_available,git_version=excluded.git_version,tmux_available=excluded.tmux_available,tmux_version=excluded.tmux_version,passwordless_sudo=excluded.passwordless_sudo,host_runtime_path=excluded.host_runtime_path,host_runtime_version=excluded.host_runtime_version,host_protocol_version=excluded.host_protocol_version,host_capabilities_json=excluded.host_capabilities_json`, o.BoxID, formatTime(o.ObservedAt), c.OSID, c.OSVersion, c.Architecture, c.Home, c.RemoteIdentity, c.WorkspaceRoot, c.WorkspaceRootExists, c.Git.Available, c.Git.Version, c.Tmux.Available, c.Tmux.Version, c.PasswordlessSudo, c.Host.Path, c.Host.Version, c.Host.ProtocolVersion, string(encodedCapabilities))
	return err
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func mapConflict(action string, err error) error {
	if strings.Contains(err.Error(), "constraint failed") || strings.Contains(err.Error(), "UNIQUE") {
		return &box.Error{Code: "conflict", Message: "box name or remote identity is already registered", Cause: err}
	}
	return fmt.Errorf("%s: %w", action, err)
}

func (s *Store) ListCredentialProfiles(ctx context.Context) ([]credentials.Profile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ref,provider,name,external_account_id,account_name,account_email,credential_key,is_default,created_at,updated_at FROM credential_profiles ORDER BY ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []credentials.Profile
	for rows.Next() {
		profile, scanErr := scanCredentialProfile(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, profile)
	}
	return result, rows.Err()
}

func (s *Store) FindCredentialProfile(ctx context.Context, ref provider.CredentialProfileRef) (credentials.Profile, error) {
	profile, err := scanCredentialProfile(s.db.QueryRowContext(ctx, `SELECT ref,provider,name,external_account_id,account_name,account_email,credential_key,is_default,created_at,updated_at FROM credential_profiles WHERE ref=?`, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return credentials.Profile{}, box.NotFound(string(ref))
	}
	return profile, err
}

func scanCredentialProfile(row scanner) (credentials.Profile, error) {
	var result credentials.Profile
	var providerID, created, updated string
	err := row.Scan(&result.Ref, &providerID, &result.Name, &result.ExternalID, &result.AccountName, &result.AccountEmail, &result.CredentialKey, &result.Default, &created, &updated)
	if err != nil {
		return credentials.Profile{}, err
	}
	result.Provider = provider.ID(providerID)
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err == nil {
		result.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	}
	return result, err
}

func (s *Store) SaveCredentialProfile(ctx context.Context, profile credentials.Profile) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if profile.Default {
		if _, err = tx.ExecContext(ctx, `UPDATE credential_profiles SET is_default=0 WHERE provider=? AND ref<>?`, profile.Provider, profile.Ref); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credential_profiles(ref,provider,name,external_account_id,account_name,account_email,credential_key,is_default,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(ref) DO UPDATE SET external_account_id=excluded.external_account_id,account_name=excluded.account_name,account_email=excluded.account_email,credential_key=excluded.credential_key,is_default=excluded.is_default,updated_at=excluded.updated_at`, profile.Ref, profile.Provider, profile.Name, profile.ExternalID, profile.AccountName, profile.AccountEmail, profile.CredentialKey, profile.Default, formatTime(profile.CreatedAt), formatTime(profile.UpdatedAt))
	if err != nil {
		return mapConflict("save credential profile", err)
	}
	return tx.Commit()
}

func (s *Store) BeginProvision(ctx context.Context, requested acquisition.ProvisionOperation) (acquisition.ProvisionOperation, error) {
	existing, err := s.FindProvision(ctx, requested.Name)
	if err == nil {
		if conflict := acquisition.ConflictForOperation(existing, requested); conflict != nil {
			return acquisition.ProvisionOperation{}, conflict
		}
		return existing, nil
	}
	if !box.IsNotFound(err) {
		return acquisition.ProvisionOperation{}, err
	}
	keys, err := json.Marshal(requested.AccessKeyIDs)
	if err != nil {
		return acquisition.ProvisionOperation{}, err
	}
	localKeys, err := json.Marshal(requested.LocalPublicKeys)
	if err != nil {
		return acquisition.ProvisionOperation{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO provision_operations(name,correlation_id,credential_profile,region,size,image,network_id,access_key_ids_json,local_public_keys_json,automatic_backups,ipv6,workspace_root,resource_id,ssh_destination,identity_file,checkpoint,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, requested.Name, requested.CorrelationID, requested.Profile, requested.Region, requested.Size, requested.Image, requested.NetworkID, string(keys), string(localKeys), requested.AutomaticBackups, requested.IPv6, requested.WorkspaceRoot, requested.ResourceID, requested.SSHDestination, requested.IdentityFile, requested.Checkpoint, formatTime(requested.UpdatedAt))
	if err != nil {
		return acquisition.ProvisionOperation{}, mapConflict("begin provider provisioning", err)
	}
	return requested, nil
}

func (s *Store) FindProvision(ctx context.Context, name string) (acquisition.ProvisionOperation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT name,correlation_id,credential_profile,region,size,image,network_id,access_key_ids_json,local_public_keys_json,automatic_backups,ipv6,workspace_root,resource_id,ssh_destination,identity_file,checkpoint,updated_at FROM provision_operations WHERE name=?`, name)
	var result acquisition.ProvisionOperation
	var keys, localKeys, updated string
	err := row.Scan(&result.Name, &result.CorrelationID, &result.Profile, &result.Region, &result.Size, &result.Image, &result.NetworkID, &keys, &localKeys, &result.AutomaticBackups, &result.IPv6, &result.WorkspaceRoot, &result.ResourceID, &result.SSHDestination, &result.IdentityFile, &result.Checkpoint, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return acquisition.ProvisionOperation{}, box.NotFound(name)
	}
	if err != nil {
		return acquisition.ProvisionOperation{}, err
	}
	if err = json.Unmarshal([]byte(keys), &result.AccessKeyIDs); err != nil {
		return acquisition.ProvisionOperation{}, err
	}
	if err = json.Unmarshal([]byte(localKeys), &result.LocalPublicKeys); err != nil {
		return acquisition.ProvisionOperation{}, err
	}
	result.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return result, err
}

func (s *Store) CheckpointProvision(ctx context.Context, operation acquisition.ProvisionOperation) error {
	result, err := s.db.ExecContext(ctx, `UPDATE provision_operations SET resource_id=?,ssh_destination=?,identity_file=?,checkpoint=?,updated_at=? WHERE name=?`, operation.ResourceID, operation.SSHDestination, operation.IdentityFile, operation.Checkpoint, formatTime(operation.UpdatedAt), operation.Name)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return box.NotFound(operation.Name)
	}
	return nil
}

func (s *Store) CompleteProvision(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM provision_operations WHERE name=?`, name)
	return err
}

func (s *Store) BeginDestroy(ctx context.Context, operation acquisition.DestroyOperation) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO destroy_operations(box_id,name,provider,resource_id,correlation_id,credential_profile,checkpoint,updated_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(box_id) DO UPDATE SET checkpoint=destroy_operations.checkpoint,updated_at=excluded.updated_at
		WHERE destroy_operations.provider=excluded.provider AND destroy_operations.resource_id=excluded.resource_id AND destroy_operations.correlation_id=excluded.correlation_id AND destroy_operations.credential_profile=excluded.credential_profile`, operation.BoxID, operation.Name, operation.Resource.Provider, operation.Resource.ResourceID, operation.Resource.CorrelationID, operation.Resource.Profile, operation.Checkpoint, formatTime(operation.UpdatedAt))
	if err != nil {
		return mapConflict("begin provider destruction", err)
	}
	return nil
}

func (s *Store) CheckpointDestroy(ctx context.Context, operation acquisition.DestroyOperation) error {
	_, err := s.db.ExecContext(ctx, `UPDATE destroy_operations SET checkpoint=?,updated_at=? WHERE box_id=?`, operation.Checkpoint, formatTime(operation.UpdatedAt), operation.BoxID)
	return err
}

func (s *Store) CompleteDestroy(ctx context.Context, boxID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM destroy_operations WHERE box_id=?`, boxID)
	return err
}
