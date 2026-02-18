package sqlite

import (
	"context"
	"errors"
	"testing"
)

func TestKVRepository_SetGetUpdateDelete(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewKVRepository(db)
	ctx := context.Background()

	if err := repo.Set(ctx, " destination_verified ", "false"); err != nil {
		t.Fatalf("Set(initial) error = %v", err)
	}

	v, err := repo.Get(ctx, "destination_verified")
	if err != nil {
		t.Fatalf("Get(initial) error = %v", err)
	}
	if v != "false" {
		t.Fatalf("expected value false, got %q", v)
	}

	if err := repo.Set(ctx, "destination_verified", "true"); err != nil {
		t.Fatalf("Set(update) error = %v", err)
	}

	v, err = repo.Get(ctx, "destination_verified")
	if err != nil {
		t.Fatalf("Get(updated) error = %v", err)
	}
	if v != "true" {
		t.Fatalf("expected updated value true, got %q", v)
	}

	if err := repo.Delete(ctx, " destination_verified "); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, err = repo.Get(ctx, "destination_verified")
	if !errors.Is(err, ErrKVNotFound) {
		t.Fatalf("expected ErrKVNotFound after delete, got %v", err)
	}
}

func TestKVRepository_NotFoundAndValidation(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewKVRepository(db)
	ctx := context.Background()

	if _, err := repo.Get(ctx, "missing"); !errors.Is(err, ErrKVNotFound) {
		t.Fatalf("expected ErrKVNotFound for missing key, got %v", err)
	}

	if err := repo.Delete(ctx, "missing"); !errors.Is(err, ErrKVNotFound) {
		t.Fatalf("expected ErrKVNotFound for delete missing key, got %v", err)
	}

	if err := repo.Set(ctx, " ", "value"); err == nil {
		t.Fatalf("expected validation error for empty key in Set")
	}

	if _, err := repo.Get(ctx, " "); err == nil {
		t.Fatalf("expected validation error for empty key in Get")
	}

	if err := repo.Delete(ctx, " "); err == nil {
		t.Fatalf("expected validation error for empty key in Delete")
	}
}
