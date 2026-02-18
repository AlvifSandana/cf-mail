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
