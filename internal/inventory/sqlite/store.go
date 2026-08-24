package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
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
	"github.com/thewelshrich/schooner/internal/box"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct{ db *sql.DB }

func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create Schooner state directory: %w", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open inventory: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
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

func (s *Store) FindByName(ctx context.Context, name string) (box.Record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, selectRecord+` WHERE name = ?`, name), name)
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

func (s *Store) BeginAdd(ctx context.Context, op box.AddOperation) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO add_operations(name, ssh_destination, project_root, checkpoint, remote_identity, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET ssh_destination=excluded.ssh_destination, project_root=excluded.project_root, updated_at=excluded.updated_at
		WHERE add_operations.ssh_destination=excluded.ssh_destination AND add_operations.project_root=excluded.project_root`, op.Name, op.SSHDestination, op.ProjectRoot, op.Checkpoint, op.RemoteIdentity, formatTime(op.UpdatedAt))
	if err != nil {
		return fmt.Errorf("record add operation: %w", err)
	}
	var destination, root string
	if err = s.db.QueryRowContext(ctx, `SELECT ssh_destination, project_root FROM add_operations WHERE name=?`, op.Name).Scan(&destination, &root); err != nil {
		return err
	}
	if destination != op.SSHDestination || root != op.ProjectRoot {
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
	_, err = tx.ExecContext(ctx, `INSERT INTO boxes(id,name,acquisition,ssh_destination,remote_identity,project_root,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, record.ID, record.Name, record.Acquisition, record.SSHDestination, record.RemoteIdentity, record.ProjectRoot, formatTime(record.CreatedAt), formatTime(record.UpdatedAt))
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

func (s *Store) LastObservation(ctx context.Context, boxID string) (box.Observation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT observed_at,os_id,os_version,architecture,home,remote_identity,project_root,project_root_exists,git_available,git_version,tmux_available,tmux_version,passwordless_sudo FROM observations WHERE box_id=?`, boxID)
	var result box.Observation
	var observed string
	result.BoxID = boxID
	err := row.Scan(&observed, &result.Capabilities.OSID, &result.Capabilities.OSVersion, &result.Capabilities.Architecture, &result.Capabilities.Home, &result.Capabilities.RemoteIdentity, &result.Capabilities.ProjectRoot, &result.Capabilities.ProjectRootExists, &result.Capabilities.Git.Available, &result.Capabilities.Git.Version, &result.Capabilities.Tmux.Available, &result.Capabilities.Tmux.Version, &result.Capabilities.PasswordlessSudo)
	if errors.Is(err, sql.ErrNoRows) {
		return box.Observation{}, box.NotFound(boxID)
	}
	if err != nil {
		return box.Observation{}, err
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

const selectRecord = `SELECT id,name,acquisition,ssh_destination,remote_identity,project_root,created_at,updated_at FROM boxes`

type scanner interface{ Scan(...any) error }

func scanRecord(row scanner, key string) (box.Record, error) {
	var result box.Record
	var created, updated string
	err := row.Scan(&result.ID, &result.Name, &result.Acquisition, &result.SSHDestination, &result.RemoteIdentity, &result.ProjectRoot, &created, &updated)
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
	_, err := db.ExecContext(ctx, `INSERT INTO observations(box_id,observed_at,os_id,os_version,architecture,home,remote_identity,project_root,project_root_exists,git_available,git_version,tmux_available,tmux_version,passwordless_sudo)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(box_id) DO UPDATE SET observed_at=excluded.observed_at,os_id=excluded.os_id,os_version=excluded.os_version,architecture=excluded.architecture,home=excluded.home,remote_identity=excluded.remote_identity,project_root=excluded.project_root,project_root_exists=excluded.project_root_exists,git_available=excluded.git_available,git_version=excluded.git_version,tmux_available=excluded.tmux_available,tmux_version=excluded.tmux_version,passwordless_sudo=excluded.passwordless_sudo`, o.BoxID, formatTime(o.ObservedAt), c.OSID, c.OSVersion, c.Architecture, c.Home, c.RemoteIdentity, c.ProjectRoot, c.ProjectRootExists, c.Git.Available, c.Git.Version, c.Tmux.Available, c.Tmux.Version, c.PasswordlessSudo)
	return err
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func mapConflict(action string, err error) error {
	if strings.Contains(err.Error(), "constraint failed") || strings.Contains(err.Error(), "UNIQUE") {
		return &box.Error{Code: "conflict", Message: "box name or remote identity is already registered", Cause: err}
	}
	return fmt.Errorf("%s: %w", action, err)
}
