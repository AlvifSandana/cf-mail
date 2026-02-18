package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

func TestMigrate_CreatesSchemaAndIsIdempotent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() first run error = %v", err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() second run should be idempotent, got error = %v", err)
	}

	for _, table := range []string{"schema_migrations", "aliases", "otp_events", "kv"} {
		if !tableExists(t, db, table) {
			t.Fatalf("expected table %q to exist", table)
		}
	}

	count := countRows(t, db, "SELECT COUNT(1) FROM schema_migrations")
	if count != 1 {
		t.Fatalf("expected exactly 1 applied migration, got %d", count)
	}
}

func TestMigrate_NilDB(t *testing.T) {
	if err := Migrate(context.Background(), nil); err == nil {
		t.Fatalf("expected error when db is nil")
	}
}

func TestApplyMigration_MissingFile(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := ensureMigrationsTable(ctx, db); err != nil {
		t.Fatalf("ensureMigrationsTable() error = %v", err)
	}

	if err := applyMigration(ctx, db, "999_missing.sql"); err == nil {
		t.Fatalf("expected applyMigration() missing file to fail")
	}

	count := countRows(t, db, "SELECT COUNT(1) FROM schema_migrations")
	if count != 0 {
		t.Fatalf("expected 0 applied migration rows, got %d", count)
	}
}

func tableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()

	const q = `SELECT 1 FROM sqlite_master WHERE type='table' AND name=? LIMIT 1`
	var one int
	err := db.QueryRow(q, table).Scan(&one)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query sqlite_master for %q: %v", table, err)
	}

	return true
}

func countRows(t *testing.T, db *sql.DB, query string) int {
	t.Helper()

	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("count rows query failed: %v", err)
	}

	return n
}
