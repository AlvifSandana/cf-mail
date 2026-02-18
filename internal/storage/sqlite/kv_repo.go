package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrKVNotFound = errors.New("kv not found")

type KVRepository struct {
	db *sql.DB
}

func NewKVRepository(db *sql.DB) *KVRepository {
	return &KVRepository{db: db}
}

func (r *KVRepository) Get(ctx context.Context, key string) (string, error) {
	if r == nil || r.db == nil {
		return "", fmt.Errorf("kv repository db is nil")
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("key is required")
	}

	const q = `SELECT value FROM kv WHERE key = ? LIMIT 1`

	var value string
	err := r.db.QueryRowContext(ctx, q, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", ErrKVNotFound
	}
	if err != nil {
		return "", fmt.Errorf("query kv by key: %w", err)
	}

	return value, nil
}

func (r *KVRepository) Set(ctx context.Context, key, value string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("kv repository db is nil")
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("key is required")
	}

	const q = `
INSERT INTO kv(key, value, updated_at)
VALUES(?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
  value = excluded.value,
  updated_at = excluded.updated_at`

	if _, err := r.db.ExecContext(ctx, q, key, value, time.Now().UTC().Format(timestampLayout)); err != nil {
		return fmt.Errorf("upsert kv: %w", err)
	}

	return nil
}

func (r *KVRepository) Delete(ctx context.Context, key string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("kv repository db is nil")
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("key is required")
	}

	const q = `DELETE FROM kv WHERE key = ?`

	result, err := r.db.ExecContext(ctx, q, key)
	if err != nil {
		return fmt.Errorf("delete kv by key: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete kv rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return ErrKVNotFound
	}

	return nil
}
