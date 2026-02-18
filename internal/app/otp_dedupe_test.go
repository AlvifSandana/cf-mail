package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

type fakeOTPDuplicateRepo struct {
	result bool
	err    error

	called int
	lastIn ports.OTPDuplicateCheck
}

func (r *fakeOTPDuplicateRepo) ExistsDuplicateWithinWindow(_ context.Context, in ports.OTPDuplicateCheck) (bool, error) {
	r.called++
	r.lastIn = in
	if r.err != nil {
		return false, r.err
	}
	return r.result, nil
}

func TestNewOTPDeduper_Validate(t *testing.T) {
	if _, err := NewOTPDeduper(nil, time.Minute); err == nil {
		t.Fatalf("expected nil repo error")
	}

	_, err := NewOTPDeduper(&fakeOTPDuplicateRepo{}, 0)
	if err == nil {
		t.Fatalf("expected invalid window error")
	}
}

func TestOTPDeduper_IsDuplicate_UsesMessageIDAndWindow(t *testing.T) {
	repo := &fakeOTPDuplicateRepo{result: true}
	d, err := NewOTPDeduper(repo, 2*time.Minute)
	if err != nil {
		t.Fatalf("NewOTPDeduper() error = %v", err)
	}

	now := time.Date(2026, 2, 18, 10, 0, 0, 0, time.UTC)
	d.nowFn = func() time.Time { return now }

	base := time.Date(2026, 2, 18, 9, 0, 0, 0, time.UTC)
	isDup, err := d.IsDuplicate(context.Background(), domain.OTPEvent{
		AliasEmail: "alias@example.com",
		OTPCode:    "123456",
		MessageID:  "msg-1",
		ReceivedAt: base,
	})
	if err != nil {
		t.Fatalf("IsDuplicate() error = %v", err)
	}
	if !isDup {
		t.Fatalf("expected duplicate=true")
	}
	if repo.called != 1 {
		t.Fatalf("expected one repo call, got %d", repo.called)
	}
	if repo.lastIn.Since != now.Add(-2*time.Minute) {
		t.Fatalf("unexpected since value: %v", repo.lastIn.Since)
	}
	if repo.lastIn.MessageID != "msg-1" {
		t.Fatalf("unexpected message_id: %q", repo.lastIn.MessageID)
	}
}

func TestOTPDeduper_IsDuplicate_ZeroReceivedAtUsesNow(t *testing.T) {
	repo := &fakeOTPDuplicateRepo{result: false}
	d, err := NewOTPDeduper(repo, 30*time.Second)
	if err != nil {
		t.Fatalf("NewOTPDeduper() error = %v", err)
	}

	now := time.Date(2026, 2, 18, 12, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	d.nowFn = func() time.Time { return now }

	_, err = d.IsDuplicate(context.Background(), domain.OTPEvent{
		AliasEmail: "alias@example.com",
		OTPCode:    "999999",
	})
	if err != nil {
		t.Fatalf("IsDuplicate() error = %v", err)
	}

	if repo.lastIn.Since != now.UTC().Add(-30*time.Second) {
		t.Fatalf("unexpected since derived from now: %v", repo.lastIn.Since)
	}
}

func TestOTPDeduper_IsDuplicate_PropagatesRepoError(t *testing.T) {
	repoErr := errors.New("query failed")
	repo := &fakeOTPDuplicateRepo{err: repoErr}
	d, err := NewOTPDeduper(repo, time.Minute)
	if err != nil {
		t.Fatalf("NewOTPDeduper() error = %v", err)
	}

	_, err = d.IsDuplicate(context.Background(), domain.OTPEvent{
		AliasEmail: "alias@example.com",
		OTPCode:    "123123",
		ReceivedAt: time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, repoErr) {
		t.Fatalf("expected wrapped repo error, got %v", err)
	}
}

func TestOTPDeduper_IsDuplicate_RequiresAliasAndOTPOrMessageID(t *testing.T) {
	repo := &fakeOTPDuplicateRepo{}
	d, err := NewOTPDeduper(repo, time.Minute)
	if err != nil {
		t.Fatalf("NewOTPDeduper() error = %v", err)
	}

	_, err = d.IsDuplicate(context.Background(), domain.OTPEvent{OTPCode: "123456"})
	if err == nil {
		t.Fatalf("expected alias validation error")
	}

	_, err = d.IsDuplicate(context.Background(), domain.OTPEvent{AliasEmail: "alias@example.com"})
	if err == nil {
		t.Fatalf("expected otp/message_id validation error")
	}
}

func TestOTPDeduper_IsDuplicate_UsesProcessingTimeNotEventReceivedAt(t *testing.T) {
	repo := &fakeOTPDuplicateRepo{}
	d, err := NewOTPDeduper(repo, 2*time.Minute)
	if err != nil {
		t.Fatalf("NewOTPDeduper() error = %v", err)
	}

	now := time.Date(2026, 2, 18, 10, 0, 0, 0, time.UTC)
	d.nowFn = func() time.Time { return now }

	_, err = d.IsDuplicate(context.Background(), domain.OTPEvent{
		AliasEmail: "alias@example.com",
		OTPCode:    "111111",
		ReceivedAt: now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("IsDuplicate() error = %v", err)
	}

	if !repo.lastIn.Since.Equal(now.Add(-2 * time.Minute)) {
		t.Fatalf("expected since from processing-time anchor, got %v", repo.lastIn.Since)
	}
}

func TestOTPDeduper_IsDuplicate_NilInternalsGuarded(t *testing.T) {
	var d *OTPDeduper
	_, err := d.IsDuplicate(context.Background(), domain.OTPEvent{})
	if err == nil {
		t.Fatalf("expected nil deduper error")
	}

	d = &OTPDeduper{window: time.Minute}
	_, err = d.IsDuplicate(context.Background(), domain.OTPEvent{AliasEmail: "a@example.com", OTPCode: "1"})
	if err == nil {
		t.Fatalf("expected nil repo/nowFn guard error")
	}
}
