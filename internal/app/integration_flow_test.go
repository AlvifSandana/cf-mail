package app

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"tuiotp/internal/adapters/parser"
	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
	"tuiotp/internal/storage/sqlite"
)

func newIntegrationOTPService(t *testing.T, rules []parser.Rule, outputFormat string, dedupeWindow time.Duration) (*sql.DB, *sqlite.OTPRepository, *OTPService) {
	t.Helper()

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}

	repo := sqlite.NewOTPRepository(db)
	engine, err := parser.NewEngine(rules)
	if err != nil {
		t.Fatalf("new parser engine: %v", err)
	}

	renderer, err := parser.NewRenderer(outputFormat)
	if err != nil {
		t.Fatalf("new renderer: %v", err)
	}

	deduper, err := NewOTPDeduper(otpDuplicateRepositoryAdapter{repo: repo}, dedupeWindow)
	if err != nil {
		t.Fatalf("new deduper: %v", err)
	}

	svc, err := NewOTPService(
		otpParserAdapter{engine: engine},
		otpRendererAdapter{renderer: renderer},
		otpRepositoryAdapter{repo: repo},
		deduper,
	)
	if err != nil {
		t.Fatalf("new otp service: %v", err)
	}

	return db, repo, svc
}

func TestIntegration_OTPService_SQLitePipelineAndDedupe(t *testing.T) {
	_, repo, svc := newIntegrationOTPService(t, []parser.Rule{{
		Platform:     "SHOP",
		FromContains: []string{"shop@example.com"},
		SubjectRegex: `(?i)otp|code`,
		OTPRegex:     `\b(\d{6})\b`,
	}}, "{{.Platform}}|{{.OTP}}|{{.Alias}}", 2*time.Minute)

	ctx := context.Background()

	base := time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)
	first, err := svc.ProcessNormalizedEmail(ctx, domain.IncomingEmail{
		To:         []string{"alias-1@example.com"},
		From:       "shop@example.com",
		Subject:    "OTP Code",
		Body:       "kode verifikasi anda 123456",
		Snippet:    "123456",
		MessageID:  "msg-1",
		ReceivedAt: base,
	})
	if err != nil {
		t.Fatalf("process first email: %v", err)
	}
	if first.Status != OTPPipelineStatusStored || first.Event == nil {
		t.Fatalf("expected first email stored, got %+v", first)
	}

	dup, err := svc.ProcessNormalizedEmail(ctx, domain.IncomingEmail{
		To:         []string{"alias-1@example.com"},
		From:       "shop@example.com",
		Subject:    "OTP Code",
		Body:       "kode verifikasi anda 123456",
		Snippet:    "123456",
		MessageID:  "msg-2",
		ReceivedAt: base.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("process duplicate email: %v", err)
	}
	if dup.Status != OTPPipelineStatusDuplicate {
		t.Fatalf("expected duplicate status, got %+v", dup)
	}

	second, err := svc.ProcessNormalizedEmail(ctx, domain.IncomingEmail{
		To:         []string{"alias-1@example.com"},
		From:       "shop@example.com",
		Subject:    "OTP Code",
		Body:       "kode verifikasi anda 999111",
		Snippet:    "999111",
		MessageID:  "msg-3",
		ReceivedAt: base.Add(90 * time.Second),
	})
	if err != nil {
		t.Fatalf("process second unique email: %v", err)
	}
	if second.Status != OTPPipelineStatusStored || second.Event == nil {
		t.Fatalf("expected second unique email stored, got %+v", second)
	}

	rows, err := repo.List(ctx, sqlite.OTPListFilter{AliasEmail: "alias-1@example.com", Limit: 10})
	if err != nil {
		t.Fatalf("list otp rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 persisted otp rows (duplicate skipped), got %d", len(rows))
	}
	if rows[0].OTPCode != "999111" || rows[1].OTPCode != "123456" {
		t.Fatalf("unexpected otp history order/content: %+v", rows)
	}
}

func TestIntegration_RuntimeCoordinator_RouteIncomingEmailEmitsEvent(t *testing.T) {
	ctx := context.Background()
	_, _, svc := newIntegrationOTPService(t, []parser.Rule{{
		Platform:     "TEL",
		FromContains: []string{"telegram.org"},
		SubjectRegex: `(?i)telegram`,
		OTPRegex:     `\b(\d{5})\b`,
	}}, "{{.Platform}} {{.OTP}}", time.Minute)

	runner := &fakeRuntimeRunner{}
	coordinator, err := NewRuntimeCoordinator(runner, svc, RuntimeCoordinatorConfig{EventBuffer: 8})
	if err != nil {
		t.Fatalf("new runtime coordinator: %v", err)
	}

	result, err := coordinator.RouteIncomingEmail(ctx, domain.IncomingEmail{
		To:         []string{"alias-2@example.com"},
		From:       "Telegram <login@telegram.org>",
		Subject:    "Telegram code",
		Body:       "Your code is 54321",
		Snippet:    "54321",
		MessageID:  "msg-telegram-1",
		ReceivedAt: time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("route incoming email: %v", err)
	}
	if result.Status != OTPPipelineStatusStored || result.Event == nil {
		t.Fatalf("expected stored result, got %+v", result)
	}

	evt := waitRuntimeEvent(t, coordinator.Events(), func(evt RuntimeEvent) bool {
		return evt.Type == RuntimeEventOTPProcessed
	})
	if evt.OTPStatus != OTPPipelineStatusStored {
		t.Fatalf("expected otp processed runtime event payload, got %+v", evt)
	}
}

type fakeIngestionRunner struct {
	update ports.WatchUpdate
}

func (f *fakeIngestionRunner) Run(ctx context.Context, onUpdate func(ports.WatchUpdate)) error {
	if onUpdate != nil {
		onUpdate(f.update)
	}
	<-ctx.Done()
	return ctx.Err()
}

func watchUpdateWithIncomingEmail(t *testing.T, in domain.IncomingEmail) ports.WatchUpdate {
	t.Helper()

	update := ports.WatchUpdate{Mode: "idle", Timestamp: time.Date(2026, 2, 18, 13, 0, 0, 0, time.UTC)}
	v := reflect.ValueOf(&update).Elem()
	field := v.FieldByName("IncomingEmail")
	if !field.IsValid() {
		t.Fatalf("ports.WatchUpdate must expose IncomingEmail for runtime ingestion")
	}
	if !field.CanSet() {
		t.Fatalf("ports.WatchUpdate.IncomingEmail field must be settable")
	}

	field.Set(reflect.ValueOf(in))
	return update
}

func TestIntegration_RuntimeCoordinator_Start_IngestsWatcherIncomingEmailToStore(t *testing.T) {
	ctx := context.Background()
	_, repo, svc := newIntegrationOTPService(t, []parser.Rule{{
		Platform:     "BANK",
		FromContains: []string{"bank.example.com"},
		SubjectRegex: `(?i)otp|code`,
		OTPRegex:     `\b(\d{6})\b`,
	}}, "{{.Platform}} {{.OTP}}", time.Minute)

	runner := &fakeIngestionRunner{update: watchUpdateWithIncomingEmail(t, domain.IncomingEmail{
		To:         []string{"alias-runtime@example.com"},
		From:       "noreply@bank.example.com",
		Subject:    "OTP CODE",
		Body:       "Use 654321 to continue",
		Snippet:    "654321",
		MessageID:  "msg-runtime-1",
		ReceivedAt: time.Date(2026, 2, 18, 13, 0, 0, 0, time.UTC),
	})}

	coordinator, err := NewRuntimeCoordinator(runner, svc, RuntimeCoordinatorConfig{EventBuffer: 8})
	if err != nil {
		t.Fatalf("new runtime coordinator: %v", err)
	}

	if err := coordinator.Start(ctx); err != nil {
		t.Fatalf("start runtime coordinator: %v", err)
	}
	defer coordinator.Stop()

	otpEvt := waitRuntimeEvent(t, coordinator.Events(), func(evt RuntimeEvent) bool {
		return evt.Type == RuntimeEventOTPProcessed
	})
	if otpEvt.OTPStatus != OTPPipelineStatusStored {
		t.Fatalf("expected runtime ingestion to persist otp, got %+v", otpEvt)
	}

	rows, err := repo.List(ctx, sqlite.OTPListFilter{AliasEmail: "alias-runtime@example.com", Limit: 10})
	if err != nil {
		t.Fatalf("list otp rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one ingested otp row, got %d", len(rows))
	}
}
