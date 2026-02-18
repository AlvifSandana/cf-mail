package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestAliasRepository_CreateAndListActive(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewAliasRepository(db)
	ctx := context.Background()

	older := time.Now().UTC().Add(-1 * time.Minute)
	newer := time.Now().UTC()

	createdOlder, err := repo.Create(ctx, Alias{
		Platform:   "TOKOPED",
		AliasEmail: "tokoped-001@example.com",
		RuleID:     "rule-1",
		RuleName:   "tuiotp:TOKOPED:tokoped-001",
		Enabled:    true,
		CreatedAt:  older,
	})
	if err != nil {
		t.Fatalf("Create(older) error = %v", err)
	}

	createdNewer, err := repo.Create(ctx, Alias{
		Platform:   "SHOPEE",
		AliasEmail: "shopee-001@example.com",
		RuleID:     "rule-2",
		RuleName:   "tuiotp:SHOPEE:shopee-001",
		Enabled:    true,
		CreatedAt:  newer,
	})
	if err != nil {
		t.Fatalf("Create(newer) error = %v", err)
	}

	if createdOlder.ID == 0 || createdNewer.ID == 0 {
		t.Fatalf("expected inserted IDs to be non-zero")
	}

	aliases, err := repo.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive() error = %v", err)
	}

	if len(aliases) != 2 {
		t.Fatalf("expected 2 aliases, got %d", len(aliases))
	}

	if aliases[0].AliasEmail != createdNewer.AliasEmail {
		t.Fatalf("expected newest alias first, got %s", aliases[0].AliasEmail)
	}
	if aliases[1].AliasEmail != createdOlder.AliasEmail {
		t.Fatalf("expected older alias second, got %s", aliases[1].AliasEmail)
	}
}

func TestAliasRepository_SoftDeleteByAliasEmail(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewAliasRepository(db)
	ctx := context.Background()

	_, err = repo.Create(ctx, Alias{
		Platform:   "TELEGRAM",
		AliasEmail: "telegram-001@example.com",
		RuleID:     "rule-3",
		RuleName:   "tuiotp:TELEGRAM:telegram-001",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := repo.SoftDeleteByAliasEmail(ctx, " TELEGRAM-001@example.com ", time.Time{}); err != nil {
		t.Fatalf("SoftDeleteByAliasEmail() error = %v", err)
	}

	aliases, err := repo.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive() error = %v", err)
	}
	if len(aliases) != 0 {
		t.Fatalf("expected 0 active aliases after delete, got %d", len(aliases))
	}

	if err := repo.SoftDeleteByAliasEmail(ctx, "telegram-001@example.com", time.Now().UTC()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows on deleting already-deleted alias, got %v", err)
	}
}

func TestAliasRepository_StoresFixedWidthTimestampLayout(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewAliasRepository(db)
	ctx := context.Background()

	in := Alias{
		Platform:   "CUSTOM",
		AliasEmail: "fixed-layout@example.com",
		RuleID:     "rule-fixed",
		RuleName:   "tuiotp:CUSTOM:fixed-layout",
		Enabled:    true,
		CreatedAt:  time.Date(2026, 2, 18, 10, 0, 0, 123456789, time.UTC),
	}

	if _, err := repo.Create(ctx, in); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	var createdAtRaw string
	if err := db.QueryRowContext(ctx, `SELECT created_at FROM aliases WHERE alias_email = ?`, in.AliasEmail).Scan(&createdAtRaw); err != nil {
		t.Fatalf("query created_at raw error = %v", err)
	}

	if len(createdAtRaw) < len("2006-01-02T15:04:05.000000000Z") {
		t.Fatalf("expected fixed-width timestamp, got %q", createdAtRaw)
	}

	if _, err := time.Parse(timestampLayout, createdAtRaw); err != nil {
		t.Fatalf("expected timestamp to parse with fixed layout: %v", err)
	}
}
