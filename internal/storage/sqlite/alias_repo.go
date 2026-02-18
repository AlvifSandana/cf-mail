package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

type Alias struct {
	ID         int64
	Platform   string
	AliasEmail string
	RuleID     string
	RuleName   string
	Enabled    bool
	CreatedAt  time.Time
	DeletedAt  *time.Time
}

type AliasRepository struct {
	db *sql.DB
}

func NewAliasRepository(db *sql.DB) *AliasRepository {
	return &AliasRepository{db: db}
}

func (r *AliasRepository) Create(ctx context.Context, in Alias) (Alias, error) {
	if r == nil || r.db == nil {
		return Alias{}, fmt.Errorf("alias repository db is nil")
	}

	now := time.Now().UTC()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	if in.DeletedAt != nil {
		in.Enabled = false
	}

	const q = `
INSERT INTO aliases(platform, alias_email, rule_id, rule_name, enabled, created_at, deleted_at)
VALUES(?, ?, ?, ?, ?, ?, ?)`

	var deletedAt any
	if in.DeletedAt != nil {
		deletedAt = in.DeletedAt.UTC().Format(timestampLayout)
	}

	result, err := r.db.ExecContext(
		ctx,
		q,
		in.Platform,
		in.AliasEmail,
		in.RuleID,
		in.RuleName,
		boolToInt(in.Enabled),
		in.CreatedAt.UTC().Format(timestampLayout),
		deletedAt,
	)
	if err != nil {
		return Alias{}, fmt.Errorf("insert alias: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Alias{}, fmt.Errorf("get inserted alias id: %w", err)
	}

	in.ID = id
	return in, nil
}

func (r *AliasRepository) ListActive(ctx context.Context) ([]Alias, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("alias repository db is nil")
	}

	const q = `
SELECT id, platform, alias_email, rule_id, rule_name, enabled, created_at, deleted_at
FROM aliases
WHERE deleted_at IS NULL
ORDER BY created_at DESC`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query active aliases: %w", err)
	}
	defer rows.Close()

	result := make([]Alias, 0)
	for rows.Next() {
		alias, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, alias)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active aliases: %w", err)
	}

	return result, nil
}

func (r *AliasRepository) FindActiveByAliasEmail(ctx context.Context, aliasEmail string) (Alias, error) {
	if r == nil || r.db == nil {
		return Alias{}, fmt.Errorf("alias repository db is nil")
	}

	aliasEmail = strings.ToLower(strings.TrimSpace(aliasEmail))
	if aliasEmail == "" {
		return Alias{}, fmt.Errorf("alias email is required")
	}

	const q = `
SELECT id, platform, alias_email, rule_id, rule_name, enabled, created_at, deleted_at
FROM aliases
WHERE alias_email = ? AND deleted_at IS NULL
LIMIT 1`

	row := r.db.QueryRowContext(ctx, q, aliasEmail)
	alias, err := scanAlias(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Alias{}, sql.ErrNoRows
		}
		return Alias{}, err
	}

	return alias, nil
}

func (r *AliasRepository) SoftDeleteByAliasEmail(ctx context.Context, aliasEmail string, deletedAt time.Time) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("alias repository db is nil")
	}

	aliasEmail = strings.ToLower(strings.TrimSpace(aliasEmail))
	if aliasEmail == "" {
		return fmt.Errorf("alias email is required")
	}

	if deletedAt.IsZero() {
		deletedAt = time.Now().UTC()
	}

	const q = `
UPDATE aliases
SET deleted_at = ?, enabled = 0
WHERE alias_email = ? AND deleted_at IS NULL`

	result, err := r.db.ExecContext(ctx, q, deletedAt.UTC().Format(timestampLayout), aliasEmail)
	if err != nil {
		return fmt.Errorf("soft delete alias: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("soft delete alias rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func scanAlias(scanner interface {
	Scan(dest ...any) error
}) (Alias, error) {
	var (
		row        Alias
		enabledInt int
		createdAt  string
		deletedAt  sql.NullString
	)

	if err := scanner.Scan(
		&row.ID,
		&row.Platform,
		&row.AliasEmail,
		&row.RuleID,
		&row.RuleName,
		&enabledInt,
		&createdAt,
		&deletedAt,
	); err != nil {
		return Alias{}, fmt.Errorf("scan alias row: %w", err)
	}

	t, err := time.Parse(timestampLayout, createdAt)
	if err != nil {
		return Alias{}, fmt.Errorf("parse alias created_at: %w", err)
	}
	row.CreatedAt = t
	row.Enabled = enabledInt == 1

	if deletedAt.Valid {
		td, err := time.Parse(timestampLayout, deletedAt.String)
		if err != nil {
			return Alias{}, fmt.Errorf("parse alias deleted_at: %w", err)
		}
		row.DeletedAt = &td
	}

	return row, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
