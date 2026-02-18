package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"tuiotp/internal/domain"
)

type fakeOTPParser struct {
	out domain.ParsedOTP
	err error

	called int
	lastIn domain.IncomingEmail
}

func (f *fakeOTPParser) Parse(in domain.IncomingEmail) (domain.ParsedOTP, error) {
	f.called++
	f.lastIn = in
	if f.err != nil {
		return domain.ParsedOTP{}, f.err
	}
	return f.out, nil
}

type fakeOTPDeduper struct {
	result bool
	err    error

	called  int
	lastEvt domain.OTPEvent
}

func (f *fakeOTPDeduper) IsDuplicate(_ context.Context, evt domain.OTPEvent) (bool, error) {
	f.called++
	f.lastEvt = evt
	if f.err != nil {
		return false, f.err
	}
	return f.result, nil
}

type fakeOTPRepo struct {
	out domain.OTPEvent
	err error

	called int
	lastIn domain.OTPEvent
}

func (f *fakeOTPRepo) Create(_ context.Context, in domain.OTPEvent) (domain.OTPEvent, error) {
	f.called++
	f.lastIn = in
	if f.err != nil {
		return domain.OTPEvent{}, f.err
	}
	if f.out.ID == 0 {
		f.out = in
		f.out.ID = 1
	}
	return f.out, nil
}

type fakeOTPRenderer struct {
	out string
	err error

	called int
	lastIn domain.ParsedOTP
}

func (f *fakeOTPRenderer) Render(in domain.ParsedOTP) (string, error) {
	f.called++
	f.lastIn = in
	if f.err != nil {
		return "", f.err
	}
	return f.out, nil
}

func TestNewOTPService_ValidateDependencies(t *testing.T) {
	p := &fakeOTPParser{}
	r := &fakeOTPRenderer{}
	repo := &fakeOTPRepo{}
	d := &fakeOTPDeduper{}

	if _, err := NewOTPService(nil, r, repo, d); err == nil {
		t.Fatalf("expected parser validation error")
	}
	if _, err := NewOTPService(p, nil, repo, d); err == nil {
		t.Fatalf("expected renderer validation error")
	}
	if _, err := NewOTPService(p, r, nil, d); err == nil {
		t.Fatalf("expected repository validation error")
	}
	if _, err := NewOTPService(p, r, repo, nil); err == nil {
		t.Fatalf("expected deduper validation error")
	}
}

func TestOTPService_ProcessNormalizedEmail_StoredAndRendered(t *testing.T) {
	ts := time.Date(2026, 2, 18, 10, 0, 0, 0, time.UTC)
	p := &fakeOTPParser{out: domain.ParsedOTP{
		Platform:   "SHOPEE",
		OTPCode:    "123456",
		AliasEmail: "alias@example.com",
		MessageID:  "msg-1",
		Subject:    "otp",
		FromEmail:  "sender@shopee.com",
		Snippet:    "snippet",
		ReceivedAt: ts,
	}}
	r := &fakeOTPRenderer{out: "SHOPEE | 123456"}
	repo := &fakeOTPRepo{out: domain.OTPEvent{
		ID:         42,
		AliasEmail: "alias@example.com",
		Platform:   "SHOPEE",
		OTPCode:    "123456",
		MessageID:  "msg-1",
		Subject:    "otp",
		FromEmail:  "sender@shopee.com",
		RawSnippet: "snippet",
		ReceivedAt: ts,
	}}
	d := &fakeOTPDeduper{result: false}

	svc, err := NewOTPService(p, r, repo, d)
	if err != nil {
		t.Fatalf("NewOTPService() error = %v", err)
	}

	res, err := svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{
		To:         []string{"alias@example.com"},
		From:       "sender@shopee.com",
		Subject:    "otp",
		MessageID:  "msg-1",
		Snippet:    "snippet",
		Body:       "code 123456",
		ReceivedAt: ts,
	})
	if err != nil {
		t.Fatalf("ProcessNormalizedEmail() error = %v", err)
	}

	if res.Status != OTPPipelineStatusStored {
		t.Fatalf("unexpected status: %q", res.Status)
	}
	if res.Event == nil {
		t.Fatalf("expected non-nil event")
	}
	if res.Event.PersistedID != 42 || res.Event.OTPCode != "123456" || res.Event.Rendered != "SHOPEE | 123456" {
		t.Fatalf("unexpected event payload: %+v", res.Event)
	}
	if d.called != 1 || repo.called != 1 || r.called != 1 {
		t.Fatalf("expected deduper/repo/renderer called once, got %d/%d/%d", d.called, repo.called, r.called)
	}
}

func TestOTPService_ProcessNormalizedEmail_IgnoresWhenNoRuleMatched(t *testing.T) {
	p := &fakeOTPParser{err: domain.ErrNoRuleMatched}
	r := &fakeOTPRenderer{}
	repo := &fakeOTPRepo{}
	d := &fakeOTPDeduper{}

	svc, err := NewOTPService(p, r, repo, d)
	if err != nil {
		t.Fatalf("NewOTPService() error = %v", err)
	}

	res, err := svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{})
	if err != nil {
		t.Fatalf("ProcessNormalizedEmail() error = %v", err)
	}
	if res.Status != OTPPipelineStatusIgnoredNoRule {
		t.Fatalf("expected ignored_no_rule, got %q", res.Status)
	}
	if res.Event != nil {
		t.Fatalf("expected nil event for ignored result")
	}
	if d.called != 0 || repo.called != 0 || r.called != 0 {
		t.Fatalf("expected no downstream calls, got %d/%d/%d", d.called, repo.called, r.called)
	}
}

func TestOTPService_ProcessNormalizedEmail_IgnoresWhenRuleMatchedButNoOTP(t *testing.T) {
	p := &fakeOTPParser{err: domain.ErrNoOTPFound}
	r := &fakeOTPRenderer{}
	repo := &fakeOTPRepo{}
	d := &fakeOTPDeduper{}

	svc, err := NewOTPService(p, r, repo, d)
	if err != nil {
		t.Fatalf("NewOTPService() error = %v", err)
	}

	res, err := svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{})
	if err != nil {
		t.Fatalf("ProcessNormalizedEmail() error = %v", err)
	}
	if res.Status != OTPPipelineStatusIgnoredNoOTP {
		t.Fatalf("expected ignored_no_otp, got %q", res.Status)
	}
}

func TestOTPService_ProcessNormalizedEmail_ParserAliasRequired_TreatedAsIgnored(t *testing.T) {
	p := &fakeOTPParser{err: domain.ErrAliasRequired}
	r := &fakeOTPRenderer{}
	repo := &fakeOTPRepo{}
	d := &fakeOTPDeduper{}

	svc, err := NewOTPService(p, r, repo, d)
	if err != nil {
		t.Fatalf("NewOTPService() error = %v", err)
	}

	res, err := svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{})
	if err != nil {
		t.Fatalf("ProcessNormalizedEmail() error = %v", err)
	}
	if res.Status != OTPPipelineStatusIgnoredNoRule {
		t.Fatalf("expected ignored_no_rule for alias required, got %q", res.Status)
	}
}

func TestOTPService_ProcessNormalizedEmail_DuplicateSkipsPersistAndRender(t *testing.T) {
	p := &fakeOTPParser{out: domain.ParsedOTP{AliasEmail: "alias@example.com", Platform: "CUSTOM", OTPCode: "654321"}}
	r := &fakeOTPRenderer{}
	repo := &fakeOTPRepo{}
	d := &fakeOTPDeduper{result: true}

	svc, err := NewOTPService(p, r, repo, d)
	if err != nil {
		t.Fatalf("NewOTPService() error = %v", err)
	}

	res, err := svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{})
	if err != nil {
		t.Fatalf("ProcessNormalizedEmail() error = %v", err)
	}
	if res.Status != OTPPipelineStatusDuplicate {
		t.Fatalf("expected duplicate status, got %q", res.Status)
	}
	if repo.called != 0 || r.called != 0 {
		t.Fatalf("expected repo/render skipped for duplicate, got %d/%d", repo.called, r.called)
	}
}

func TestOTPService_ProcessNormalizedEmail_MapsFieldsCorrectlyToDeduperAndRepository(t *testing.T) {
	ts := time.Date(2026, 2, 18, 12, 30, 0, 0, time.UTC)
	p := &fakeOTPParser{out: domain.ParsedOTP{
		AliasEmail: "alias-1@example.com",
		Platform:   "TOKOPED",
		OTPCode:    "111222",
		MessageID:  "msg-alias-1",
		Subject:    "otp",
		FromEmail:  "sender@tokopedia.com",
		Snippet:    "otp snippet",
		ReceivedAt: ts,
	}}
	r := &fakeOTPRenderer{out: "ok"}
	repo := &fakeOTPRepo{}
	d := &fakeOTPDeduper{}

	svc, err := NewOTPService(p, r, repo, d)
	if err != nil {
		t.Fatalf("NewOTPService() error = %v", err)
	}

	_, err = svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{})
	if err != nil {
		t.Fatalf("ProcessNormalizedEmail() error = %v", err)
	}

	if d.lastEvt.AliasEmail != "alias-1@example.com" || d.lastEvt.MessageID != "msg-alias-1" {
		t.Fatalf("unexpected deduper mapping: %+v", d.lastEvt)
	}
	if repo.lastIn.Platform != "TOKOPED" || repo.lastIn.OTPCode != "111222" || repo.lastIn.RawSnippet != "otp snippet" {
		t.Fatalf("unexpected repository mapping: %+v", repo.lastIn)
	}
}

func TestOTPService_ProcessNormalizedEmail_PersistError_ReturnsError(t *testing.T) {
	p := &fakeOTPParser{out: domain.ParsedOTP{AliasEmail: "alias@example.com", Platform: "CUSTOM", OTPCode: "654321"}}
	repoErr := errors.New("insert failed")
	r := &fakeOTPRenderer{}
	repo := &fakeOTPRepo{err: repoErr}
	d := &fakeOTPDeduper{}

	svc, err := NewOTPService(p, r, repo, d)
	if err != nil {
		t.Fatalf("NewOTPService() error = %v", err)
	}

	_, err = svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{})
	if !errors.Is(err, repoErr) {
		t.Fatalf("expected wrapped repository error, got %v", err)
	}
	if r.called != 0 {
		t.Fatalf("renderer should not be called when persist fails")
	}
}

func TestOTPService_ProcessNormalizedEmail_RenderError_ReturnsErrorAfterPersist(t *testing.T) {
	p := &fakeOTPParser{out: domain.ParsedOTP{AliasEmail: "alias@example.com", Platform: "CUSTOM", OTPCode: "654321"}}
	renderErr := errors.New("render failed")
	r := &fakeOTPRenderer{err: renderErr}
	ts := time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)
	repo := &fakeOTPRepo{out: domain.OTPEvent{ID: 77, AliasEmail: "alias@example.com", Platform: "CUSTOM", OTPCode: "654321", ReceivedAt: ts}}
	d := &fakeOTPDeduper{}

	svc, err := NewOTPService(p, r, repo, d)
	if err != nil {
		t.Fatalf("NewOTPService() error = %v", err)
	}

	res, err := svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{})
	if err != nil {
		t.Fatalf("expected non-fatal render fallback, got %v", err)
	}
	if res.Status != OTPPipelineStatusStored || res.Event == nil {
		t.Fatalf("expected stored status with event, got %+v", res)
	}
	if !strings.Contains(res.Event.Rendered, "CUSTOM | 654321") {
		t.Fatalf("expected fallback rendered output, got %q", res.Event.Rendered)
	}
	if !strings.Contains(res.Event.Rendered, "alias@example.com") {
		t.Fatalf("expected fallback to include alias, got %q", res.Event.Rendered)
	}
	if repo.called != 1 {
		t.Fatalf("expected persist called before render failure")
	}
	if r.called != 1 {
		t.Fatalf("expected renderer attempted once")
	}
}

func TestOTPService_ProcessNormalizedEmail_NilServiceGuarded(t *testing.T) {
	var svc *OTPService
	_, err := svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{})
	if err == nil {
		t.Fatalf("expected nil service error")
	}
}

func TestOTPService_ProcessNormalizedEmail_ClampFutureReceivedAtToProcessingTime(t *testing.T) {
	now := time.Date(2026, 2, 18, 11, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)

	p := &fakeOTPParser{out: domain.ParsedOTP{
		AliasEmail: "alias@example.com",
		Platform:   "CUSTOM",
		OTPCode:    "777888",
		ReceivedAt: future,
	}}
	r := &fakeOTPRenderer{out: "ok"}
	repo := &fakeOTPRepo{}
	d := &fakeOTPDeduper{}

	svc, err := NewOTPService(p, r, repo, d)
	if err != nil {
		t.Fatalf("NewOTPService() error = %v", err)
	}
	svc.nowFn = func() time.Time { return now }

	_, err = svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{})
	if err != nil {
		t.Fatalf("ProcessNormalizedEmail() error = %v", err)
	}

	if !d.lastEvt.ReceivedAt.Equal(now) {
		t.Fatalf("expected dedupe event received_at clamped to now, got %v", d.lastEvt.ReceivedAt)
	}
	if !repo.lastIn.ReceivedAt.Equal(now) {
		t.Fatalf("expected persisted received_at clamped to now, got %v", repo.lastIn.ReceivedAt)
	}
}

func TestOTPService_ProcessNormalizedEmail_UsesProcessingTimeWhenParsedReceivedAtZero(t *testing.T) {
	now := time.Date(2026, 2, 18, 11, 5, 0, 0, time.UTC)

	p := &fakeOTPParser{out: domain.ParsedOTP{
		AliasEmail: "alias@example.com",
		Platform:   "CUSTOM",
		OTPCode:    "123123",
	}}
	r := &fakeOTPRenderer{out: "ok"}
	repo := &fakeOTPRepo{}
	d := &fakeOTPDeduper{}

	svc, err := NewOTPService(p, r, repo, d)
	if err != nil {
		t.Fatalf("NewOTPService() error = %v", err)
	}
	svc.nowFn = func() time.Time { return now }

	_, err = svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{})
	if err != nil {
		t.Fatalf("ProcessNormalizedEmail() error = %v", err)
	}

	if !repo.lastIn.ReceivedAt.Equal(now) {
		t.Fatalf("expected zero parsed received_at replaced with now, got %v", repo.lastIn.ReceivedAt)
	}
}

func TestOTPService_ProcessNormalizedEmail_OverridesBackdatedReceivedAtWithProcessingTime(t *testing.T) {
	now := time.Date(2026, 2, 18, 11, 10, 0, 0, time.UTC)
	oldTS := now.Add(-48 * time.Hour)

	p := &fakeOTPParser{out: domain.ParsedOTP{
		AliasEmail: "alias@example.com",
		Platform:   "CUSTOM",
		OTPCode:    "444555",
		ReceivedAt: oldTS,
	}}
	r := &fakeOTPRenderer{out: "ok"}
	repo := &fakeOTPRepo{}
	d := &fakeOTPDeduper{}

	svc, err := NewOTPService(p, r, repo, d)
	if err != nil {
		t.Fatalf("NewOTPService() error = %v", err)
	}
	svc.nowFn = func() time.Time { return now }

	_, err = svc.ProcessNormalizedEmail(context.Background(), domain.IncomingEmail{})
	if err != nil {
		t.Fatalf("ProcessNormalizedEmail() error = %v", err)
	}

	if !d.lastEvt.ReceivedAt.Equal(now) {
		t.Fatalf("expected dedupe event received_at anchored to processing time, got %v", d.lastEvt.ReceivedAt)
	}
	if !repo.lastIn.ReceivedAt.Equal(now) {
		t.Fatalf("expected persisted received_at anchored to processing time, got %v", repo.lastIn.ReceivedAt)
	}
}
