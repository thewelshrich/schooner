package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/thewelshrich/schooner/internal/acquisition"
)

func TestProvisionWarningMigrationPreservesLegacyCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	op := acquisition.ProvisionOperation{Name: "work", CorrelationID: "existing", ResourceID: "42", Checkpoint: "remote_preparation"}
	if _, err = store.BeginProvision(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the preceding schema with its already-persisted checkpoint.
	if _, err = store.db.ExecContext(t.Context(), `ALTER TABLE provision_operations DROP COLUMN warning`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(t.Context(), `DELETE FROM schema_migrations WHERE version=9`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.FindProvision(t.Context(), op.Name)
	if err != nil || got.CorrelationID != op.CorrelationID || got.ResourceID != op.ResourceID || got.Checkpoint != op.Checkpoint || got.Warning != "" {
		t.Fatalf("migrated checkpoint = %+v, %v", got, err)
	}
}
