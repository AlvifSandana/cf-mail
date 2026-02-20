package sqlite

import (
	"context"
	"testing"
	"time"
)

func TestOTPRepository_CreateAndListWithFilter(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	ctx := context.Background()

	olderTime := time.Now().UTC().Add(-1 * time.Minute)
	newerTime := time.Now().UTC()

	_, err = repo.Create(ctx, OTPEvent{
		AliasEmail: "shopee-001@example.com",
		Platform:   "SHOPEE",
		OTPCode:    "123456",
		ReceivedAt: olderTime,
		Subject:    "Kode verifikasi Shopee",
		MessageID:  "msg-old",
	})
	if err != nil {
		t.Fatalf("Create(old) error = %v", err)
	}

	createdNew, err := repo.Create(ctx, OTPEvent{
		AliasEmail: "tokoped-001@example.com",
		Platform:   "TOKOPED",
		OTPCode:    "654321",
		ReceivedAt: newerTime,
		Subject:    "OTP Tokoped terbaru",
		MessageID:  "msg-new",
	})
	if err != nil {
		t.Fatalf("Create(new) error = %v", err)
	}

	all, err := repo.List(ctx, OTPListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List(all) error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(all))
	}
	if all[0].ID != createdNew.ID {
		t.Fatalf("expected newest event first")
	}

	filtered, err := repo.List(ctx, OTPListFilter{Platform: "SHOPEE", Limit: 10})
	if err != nil {
		t.Fatalf("List(filter platform) error = %v", err)
	}
	if len(filtered) != 1 || filtered[0].Platform != "SHOPEE" {
		t.Fatalf("expected one SHOPEE event")
	}

	queryRows, err := repo.List(ctx, OTPListFilter{Query: "tokoped", Limit: 10})
	if err != nil {
		t.Fatalf("List(query) error = %v", err)
	}
	if len(queryRows) != 1 || queryRows[0].MessageID != "msg-new" {
		t.Fatalf("expected query to match newest tokoped row")
	}
}

func TestOTPRepository_ExistsDuplicateWithinWindow(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	_, err = repo.Create(ctx, OTPEvent{
		AliasEmail: "custom-001@example.com",
		Platform:   "CUSTOM",
		OTPCode:    "999999",
		ReceivedAt: now.Add(-30 * time.Second),
		MessageID:  "msg-dup-1",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	dupByMsgID, err := repo.ExistsDuplicateWithinWindow(ctx, OTPDuplicateCheck{
		AliasEmail: "custom-001@example.com",
		MessageID:  "msg-dup-1",
		Since:      now.Add(-2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ExistsDuplicateWithinWindow(by message_id) error = %v", err)
	}
	if !dupByMsgID {
		t.Fatalf("expected duplicate by message_id")
	}

	notDupOldMsgID, err := repo.ExistsDuplicateWithinWindow(ctx, OTPDuplicateCheck{
		AliasEmail: "custom-001@example.com",
		MessageID:  "msg-dup-1",
		Since:      now.Add(-10 * time.Second),
	})
	if err != nil {
		t.Fatalf("ExistsDuplicateWithinWindow(old message_id) error = %v", err)
	}
	if notDupOldMsgID {
		t.Fatalf("expected non-duplicate when message_id exists but outside window")
	}

	dupByWindow, err := repo.ExistsDuplicateWithinWindow(ctx, OTPDuplicateCheck{
		AliasEmail: "custom-001@example.com",
		OTPCode:    "999999",
		Since:      now.Add(-2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ExistsDuplicateWithinWindow(by window) error = %v", err)
	}
	if !dupByWindow {
		t.Fatalf("expected duplicate by alias+otp+window")
	}

	notDup, err := repo.ExistsDuplicateWithinWindow(ctx, OTPDuplicateCheck{
		AliasEmail: "custom-001@example.com",
		OTPCode:    "888888",
		Since:      now.Add(-2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ExistsDuplicateWithinWindow(not dup) error = %v", err)
	}
	if notDup {
		t.Fatalf("expected non-duplicate result")
	}
}

func TestOTPRepository_ExistsDuplicateWithinWindow_Validation(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	ctx := context.Background()

	if _, err := repo.ExistsDuplicateWithinWindow(ctx, OTPDuplicateCheck{
		AliasEmail: "custom-001@example.com",
		OTPCode:    "123456",
	}); err == nil {
		t.Fatalf("expected validation error when since is missing")
	}

	if _, err := repo.ExistsDuplicateWithinWindow(ctx, OTPDuplicateCheck{
		AliasEmail: "custom-001@example.com",
		MessageID:  "msg-1",
		Since:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("did not expect validation error when alias_email + message_id provided: %v", err)
	}

	if _, err := repo.ExistsDuplicateWithinWindow(ctx, OTPDuplicateCheck{
		MessageID: "msg-1",
		Since:     time.Now().UTC(),
	}); err == nil {
		t.Fatalf("expected validation error when message_id is set without alias_email")
	}

	if _, err := repo.ExistsDuplicateWithinWindow(ctx, OTPDuplicateCheck{
		Since: time.Now().UTC(),
	}); err == nil {
		t.Fatalf("expected validation error when message_id and alias/otp are missing")
	}
}

func TestOTPRepository_CreateValidation(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	ctx := context.Background()

	_, err = repo.Create(ctx, OTPEvent{Platform: "CUSTOM", OTPCode: "1234"})
	if err == nil {
		t.Fatalf("expected validation error when alias_email missing")
	}
}

func TestOTPRepository_DeleteByID(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	ctx := context.Background()

	created, err := repo.Create(ctx, OTPEvent{
		AliasEmail: "delete-id@example.com",
		Platform:   "SHOP",
		OTPCode:    "111111",
		ReceivedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	rows, err := repo.DeleteByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("DeleteByID() error = %v", err)
	}
	if rows != 1 {
		t.Fatalf("expected 1 affected row, got %d", rows)
	}

	left, err := repo.List(ctx, OTPListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("expected no rows after delete by id, got %d", len(left))
	}
}

func TestOTPRepository_DeleteByFilter(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	seed := []OTPEvent{
		{AliasEmail: "a@example.com", Platform: "SHOP", OTPCode: "111111", Subject: "tokoped code", ReceivedAt: now},
		{AliasEmail: "b@example.com", Platform: "BANK", OTPCode: "222222", Subject: "bank code", ReceivedAt: now},
		{AliasEmail: "c@example.com", Platform: "SHOP", OTPCode: "333333", Subject: "tokoped login", ReceivedAt: now},
	}
	for _, row := range seed {
		if _, err := repo.Create(ctx, row); err != nil {
			t.Fatalf("Create(seed) error = %v", err)
		}
	}

	rows, err := repo.DeleteByFilter(ctx, OTPDeleteFilter{Query: "tokoped"})
	if err != nil {
		t.Fatalf("DeleteByFilter(query) error = %v", err)
	}
	if rows != 2 {
		t.Fatalf("expected 2 affected rows, got %d", rows)
	}

	left, err := repo.List(ctx, OTPListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(left) != 1 || left[0].Platform != "BANK" {
		t.Fatalf("expected only BANK row left, got %+v", left)
	}

	rows, err = repo.DeleteByFilter(ctx, OTPDeleteFilter{AllowDeleteAll: true})
	if err != nil {
		t.Fatalf("DeleteByFilter(all) error = %v", err)
	}
	if rows != 1 {
		t.Fatalf("expected 1 affected row on clear all, got %d", rows)
	}
}

func TestOTPRepository_DeleteByFilter_AllWithoutAllowFlagRejected(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	if _, err := repo.DeleteByFilter(context.Background(), OTPDeleteFilter{}); err == nil {
		t.Fatalf("expected error for clear-all without explicit allow flag")
	}
}

func TestOTPRepository_DeleteByID_NotFound(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	rows, err := repo.DeleteByID(context.Background(), 99999)
	if err != nil {
		t.Fatalf("DeleteByID(not found) error = %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected 0 affected rows for unknown id, got %d", rows)
	}
}

func TestOTPRepository_DeleteByFilter_AliasAndPlatform(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	seed := []OTPEvent{
		{AliasEmail: "same@example.com", Platform: "SHOP", OTPCode: "111111", ReceivedAt: now},
		{AliasEmail: "same@example.com", Platform: "BANK", OTPCode: "222222", ReceivedAt: now},
		{AliasEmail: "other@example.com", Platform: "SHOP", OTPCode: "333333", ReceivedAt: now},
	}
	for _, row := range seed {
		if _, err := repo.Create(ctx, row); err != nil {
			t.Fatalf("Create(seed) error = %v", err)
		}
	}

	rows, err := repo.DeleteByFilter(ctx, OTPDeleteFilter{AliasEmail: "same@example.com", Platform: "SHOP"})
	if err != nil {
		t.Fatalf("DeleteByFilter(alias+platform) error = %v", err)
	}
	if rows != 1 {
		t.Fatalf("expected 1 affected row, got %d", rows)
	}

	left, err := repo.List(ctx, OTPListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(left) != 2 {
		t.Fatalf("expected 2 rows left, got %d", len(left))
	}
}

func TestOTPRepository_DeleteByFilter_ScopedQueryAllowed(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	if _, err := repo.DeleteByFilter(context.Background(), OTPDeleteFilter{Query: "tokoped"}); err != nil {
		t.Fatalf("expected delete filter without allow-all to proceed for scoped query, err=%v", err)
	}
}

func TestOTPRepository_DeleteByFilter_QueryWildcardEscaped(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	seed := []OTPEvent{
		{AliasEmail: "a@example.com", Platform: "SHOP", OTPCode: "111111", Subject: "tokoped code", ReceivedAt: now},
		{AliasEmail: "b@example.com", Platform: "BANK", OTPCode: "222222", Subject: "bank code", ReceivedAt: now},
	}
	for _, row := range seed {
		if _, err := repo.Create(ctx, row); err != nil {
			t.Fatalf("Create(seed) error = %v", err)
		}
	}

	rows, err := repo.DeleteByFilter(ctx, OTPDeleteFilter{Query: "%"})
	if err != nil {
		t.Fatalf("DeleteByFilter(wildcard-literal) error = %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected 0 affected rows for escaped wildcard query, got %d", rows)
	}

	left, err := repo.List(ctx, OTPListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(left) != 2 {
		t.Fatalf("expected 2 rows left, got %d", len(left))
	}
}

func TestOTPRepository_List_QueryWildcardEscaped(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	seed := []OTPEvent{
		{AliasEmail: "a@example.com", Platform: "SHOP", OTPCode: "111111", Subject: "tokoped code", ReceivedAt: now},
		{AliasEmail: "b@example.com", Platform: "BANK", OTPCode: "222222", Subject: "bank code", ReceivedAt: now},
	}
	for _, row := range seed {
		if _, err := repo.Create(ctx, row); err != nil {
			t.Fatalf("Create(seed) error = %v", err)
		}
	}

	for _, tc := range []string{"%", "_", "\\"} {
		rows, err := repo.List(ctx, OTPListFilter{Query: tc, Limit: 10})
		if err != nil {
			t.Fatalf("List(query=%q) error = %v", tc, err)
		}
		if len(rows) != 0 {
			t.Fatalf("expected 0 rows for escaped wildcard query %q, got %d", tc, len(rows))
		}
	}
}

func TestOTPRepository_DeleteByID_InvalidID(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	repo := NewOTPRepository(db)
	if _, err := repo.DeleteByID(context.Background(), 0); err == nil {
		t.Fatalf("expected validation error for id <= 0")
	}
}
