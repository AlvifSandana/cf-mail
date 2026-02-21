package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type OTPEvent struct {
	ID         int64
	AliasEmail string
	Platform   string
	OTPCode    string
	ReceivedAt time.Time
	FromEmail  string
	Subject    string
	MessageID  string
	RawSnippet string
}

type OTPRepository struct {
	db *sql.DB
}

func NewOTPRepository(db *sql.DB) *OTPRepository {
	return &OTPRepository{db: db}
}

func escapedLikeQuery(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	escape := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + escape.Replace(v) + "%"
}

func (r *OTPRepository) Create(ctx context.Context, in OTPEvent) (OTPEvent, error) {
	if r == nil || r.db == nil {
		return OTPEvent{}, fmt.Errorf("otp repository db is nil")
	}

	in.AliasEmail = strings.ToLower(strings.TrimSpace(in.AliasEmail))
	in.Platform = strings.TrimSpace(in.Platform)
	in.OTPCode = strings.TrimSpace(in.OTPCode)

	if in.AliasEmail == "" {
		return OTPEvent{}, fmt.Errorf("alias_email is required")
	}
	if in.Platform == "" {
		return OTPEvent{}, fmt.Errorf("platform is required")
	}
	if in.OTPCode == "" {
		return OTPEvent{}, fmt.Errorf("otp_code is required")
	}

	if in.ReceivedAt.IsZero() {
		in.ReceivedAt = time.Now().UTC()
	}

	const q = `
INSERT INTO otp_events(alias_email, platform, otp_code, received_at, from_email, subject, message_id, raw_snippet)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`

	result, err := r.db.ExecContext(
		ctx,
		q,
		in.AliasEmail,
		in.Platform,
		in.OTPCode,
		in.ReceivedAt.UTC().Format(timestampLayout),
		in.FromEmail,
		in.Subject,
		in.MessageID,
		in.RawSnippet,
	)
	if err != nil {
		return OTPEvent{}, fmt.Errorf("insert otp_event: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return OTPEvent{}, fmt.Errorf("get inserted otp_event id: %w", err)
	}

	in.ID = id
	return in, nil
}

func (r *OTPRepository) List(ctx context.Context, filter OTPListFilter) ([]OTPEvent, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("otp repository db is nil")
	}

	if filter.Limit <= 0 {
		filter.Limit = 100
	}

	base := `
SELECT id, alias_email, platform, otp_code, received_at, from_email, subject, message_id, raw_snippet
FROM otp_events`

	clauses := make([]string, 0, 3)
	args := make([]any, 0, 4)

	if v := strings.TrimSpace(strings.ToLower(filter.AliasEmail)); v != "" {
		clauses = append(clauses, "LOWER(alias_email) = ?")
		args = append(args, v)
	}

	if v := strings.TrimSpace(filter.Platform); v != "" {
		clauses = append(clauses, "platform = ?")
		args = append(args, v)
	}

	if v := strings.TrimSpace(strings.ToLower(filter.Query)); v != "" {
		clauses = append(clauses, "(LOWER(alias_email) LIKE ? ESCAPE '\\' OR LOWER(platform) LIKE ? ESCAPE '\\' OR LOWER(subject) LIKE ? ESCAPE '\\' OR otp_code LIKE ? ESCAPE '\\')")
		like := escapedLikeQuery(v)
		args = append(args, like, like, like, like)
	}

	q := base
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY received_at DESC LIMIT ?"
	args = append(args, filter.Limit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query otp_events: %w", err)
	}
	defer rows.Close()

	out := make([]OTPEvent, 0)
	for rows.Next() {
		evt, err := scanOTPEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, evt)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate otp_events rows: %w", err)
	}

	return out, nil
}

type OTPListFilter struct {
	AliasEmail string
	Platform   string
	Query      string
	Limit      int
}

type OTPDeleteFilter struct {
	AliasEmail     string
	Platform       string
	Query          string
	AllowDeleteAll bool
}

func (r *OTPRepository) DeleteByID(ctx context.Context, id int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("otp repository db is nil")
	}
	if id <= 0 {
		return 0, fmt.Errorf("id must be greater than zero")
	}

	const q = `DELETE FROM otp_events WHERE id = ?`
	result, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return 0, fmt.Errorf("delete otp_event by id: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete otp_event by id rows_affected: %w", err)
	}

	return rows, nil
}

func (r *OTPRepository) DeleteByFilter(ctx context.Context, filter OTPDeleteFilter) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("otp repository db is nil")
	}
	base := `DELETE FROM otp_events`
	clauses := make([]string, 0, 3)
	args := make([]any, 0, 4)

	if v := strings.TrimSpace(strings.ToLower(filter.AliasEmail)); v != "" {
		clauses = append(clauses, "LOWER(alias_email) = ?")
		args = append(args, v)
	}

	if v := strings.TrimSpace(filter.Platform); v != "" {
		clauses = append(clauses, "platform = ?")
		args = append(args, v)
	}

	if v := strings.TrimSpace(strings.ToLower(filter.Query)); v != "" {
		like := escapedLikeQuery(v)
		clauses = append(clauses, "(LOWER(alias_email) LIKE ? ESCAPE '\\' OR LOWER(platform) LIKE ? ESCAPE '\\' OR LOWER(subject) LIKE ? ESCAPE '\\' OR otp_code LIKE ? ESCAPE '\\')")
		args = append(args, like, like, like, like)
	}

	q := base
	if len(clauses) == 0 && !filter.AllowDeleteAll {
		return 0, fmt.Errorf("refusing to delete all otp events without explicit allow flag")
	}
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}

	result, err := r.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("delete otp_events by filter: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete otp_events by filter rows_affected: %w", err)
	}

	return rows, nil
}

func (r *OTPRepository) ExistsDuplicateWithinWindow(ctx context.Context, in OTPDuplicateCheck) (bool, error) {
	if r == nil || r.db == nil {
		return false, fmt.Errorf("otp repository db is nil")
	}

	in.AliasEmail = strings.ToLower(strings.TrimSpace(in.AliasEmail))
	in.OTPCode = strings.TrimSpace(in.OTPCode)
	in.MessageID = strings.TrimSpace(in.MessageID)

	if in.Since.IsZero() {
		return false, fmt.Errorf("since is required")
	}
	if in.AliasEmail == "" {
		return false, fmt.Errorf("alias_email is required")
	}
	if in.MessageID == "" {
		if in.OTPCode == "" {
			return false, fmt.Errorf("otp_code is required when message_id is empty")
		}
	}

	if in.MessageID != "" {
		const byMessageID = `
SELECT 1
FROM otp_events
WHERE message_id = ?
  AND alias_email = ?
  AND received_at >= ?
LIMIT 1`

		var one int
		err := r.db.QueryRowContext(ctx, byMessageID, in.MessageID, in.AliasEmail, in.Since.UTC().Format(timestampLayout)).Scan(&one)
		if err == nil {
			return true, nil
		}
		if err != nil && err != sql.ErrNoRows {
			return false, fmt.Errorf("query duplicate by message_id: %w", err)
		}
	}

	if in.OTPCode == "" {
		return false, nil
	}

	const byWindow = `
SELECT 1
FROM otp_events
WHERE alias_email = ?
  AND otp_code = ?
  AND received_at >= ?
LIMIT 1`

	var one int
	err := r.db.QueryRowContext(ctx, byWindow, in.AliasEmail, in.OTPCode, in.Since.UTC().Format(timestampLayout)).Scan(&one)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}

	return false, fmt.Errorf("query duplicate within window: %w", err)
}

type OTPDuplicateCheck struct {
	AliasEmail string
	OTPCode    string
	MessageID  string
	Since      time.Time
}

func scanOTPEvent(scanner interface {
	Scan(dest ...any) error
}) (OTPEvent, error) {
	var (
		row        OTPEvent
		receivedAt string
		fromEmail  sql.NullString
		subject    sql.NullString
		messageID  sql.NullString
		rawSnippet sql.NullString
	)

	if err := scanner.Scan(
		&row.ID,
		&row.AliasEmail,
		&row.Platform,
		&row.OTPCode,
		&receivedAt,
		&fromEmail,
		&subject,
		&messageID,
		&rawSnippet,
	); err != nil {
		return OTPEvent{}, fmt.Errorf("scan otp_event row: %w", err)
	}

	t, err := time.Parse(timestampLayout, receivedAt)
	if err != nil {
		return OTPEvent{}, fmt.Errorf("parse otp_event received_at: %w", err)
	}
	row.ReceivedAt = t

	if fromEmail.Valid {
		row.FromEmail = fromEmail.String
	}
	if subject.Valid {
		row.Subject = subject.String
	}
	if messageID.Valid {
		row.MessageID = messageID.String
	}
	if rawSnippet.Valid {
		row.RawSnippet = rawSnippet.String
	}

	return row, nil
}
