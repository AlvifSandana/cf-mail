package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

type fakeRuntimeRunner struct {
	runCalls int
	runFn    func(context.Context, func(ports.WatchUpdate)) error
}

func (f *fakeRuntimeRunner) Run(ctx context.Context, onUpdate func(ports.WatchUpdate)) error {
	f.runCalls++
	if f.runFn != nil {
		return f.runFn(ctx, onUpdate)
	}
	<-ctx.Done()
	return ctx.Err()
}

type fakeRuntimeOTPProcessor struct {
	called int
	out    OTPPipelineResult
	err    error
	lastIn domain.IncomingEmail
}

func (f *fakeRuntimeOTPProcessor) ProcessNormalizedEmail(_ context.Context, in domain.IncomingEmail) (OTPPipelineResult, error) {
	f.called++
	f.lastIn = in
	if f.err != nil {
		return OTPPipelineResult{}, f.err
	}
	return f.out, nil
}

func TestNewRuntimeCoordinator_ValidateAndDefaults(t *testing.T) {
	runner := &fakeRuntimeRunner{}
	otp := &fakeRuntimeOTPProcessor{}

	if _, err := NewRuntimeCoordinator(nil, otp, RuntimeCoordinatorConfig{}); err == nil {
		t.Fatalf("expected nil runner error")
	}
	if _, err := NewRuntimeCoordinator(runner, nil, RuntimeCoordinatorConfig{}); err == nil {
		t.Fatalf("expected nil otp processor error")
	}

	c, err := NewRuntimeCoordinator(runner, otp, RuntimeCoordinatorConfig{})
	if err != nil {
		t.Fatalf("NewRuntimeCoordinator() error = %v", err)
	}
	if c.events == nil {
		t.Fatalf("expected events channel initialized")
	}
}

func TestRuntimeCoordinator_StartStop_EmitsWatcherAndStoppedEvents(t *testing.T) {
	runner := &fakeRuntimeRunner{runFn: func(ctx context.Context, onUpdate func(ports.WatchUpdate)) error {
		onUpdate(ports.WatchUpdate{Mode: "poll", Timestamp: time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)})
		<-ctx.Done()
		return ctx.Err()
	}}
	otp := &fakeRuntimeOTPProcessor{}

	c, err := NewRuntimeCoordinator(runner, otp, RuntimeCoordinatorConfig{EventBuffer: 8})
	if err != nil {
		t.Fatalf("NewRuntimeCoordinator() error = %v", err)
	}

	baseNow := time.Date(2026, 2, 18, 12, 30, 0, 0, time.UTC)
	c.nowFn = func() time.Time { return baseNow }

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	watchEvt := waitRuntimeEvent(t, c.Events(), func(evt RuntimeEvent) bool {
		return evt.Type == RuntimeEventWatcherUpdate
	})
	if watchEvt.Watch == nil || watchEvt.Watch.Mode != "poll" {
		t.Fatalf("unexpected watch event payload: %+v", watchEvt)
	}
	if !isEmptyIncomingEmail(watchEvt.Watch.IncomingEmail) {
		t.Fatalf("expected watcher event payload to redact incoming email content")
	}

	c.Stop()

	stoppedEvt := waitRuntimeEvent(t, c.Events(), func(evt RuntimeEvent) bool {
		return evt.Type == RuntimeEventRuntimeStopped
	})
	if stoppedEvt.Type != RuntimeEventRuntimeStopped {
		t.Fatalf("unexpected stopped event: %+v", stoppedEvt)
	}
}

func TestSanitizeWatchUpdateForEvent_RedactsIncomingEmail(t *testing.T) {
	in := ports.WatchUpdate{
		Mode:      "idle",
		Timestamp: time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC),
		IncomingEmail: domain.IncomingEmail{
			To:        []string{"alias@example.com"},
			From:      "sender@example.com",
			Subject:   "subject",
			MessageID: "msg-1",
			Snippet:   "snippet",
			Body:      "body",
		},
	}

	out := sanitizeWatchUpdateForEvent(in)
	if out.Mode != in.Mode || !out.Timestamp.Equal(in.Timestamp) {
		t.Fatalf("expected non-sensitive watch update fields to be preserved")
	}
	if !isEmptyIncomingEmail(out.IncomingEmail) {
		t.Fatalf("expected incoming email payload redacted for runtime event")
	}
}

func TestRuntimeCoordinator_Start_RejectsDoubleStart(t *testing.T) {
	runner := &fakeRuntimeRunner{}
	otp := &fakeRuntimeOTPProcessor{}

	c, err := NewRuntimeCoordinator(runner, otp, RuntimeCoordinatorConfig{})
	if err != nil {
		t.Fatalf("NewRuntimeCoordinator() error = %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start() first call error = %v", err)
	}
	defer c.Stop()

	if err := c.Start(context.Background()); err == nil {
		t.Fatalf("expected double start error")
	}
}

func TestRuntimeCoordinator_Start_EmitsRuntimeErrorOnRunnerFailure(t *testing.T) {
	runErr := errors.New("watcher failed")
	runner := &fakeRuntimeRunner{runFn: func(context.Context, func(ports.WatchUpdate)) error {
		return runErr
	}}
	otp := &fakeRuntimeOTPProcessor{}

	c, err := NewRuntimeCoordinator(runner, otp, RuntimeCoordinatorConfig{EventBuffer: 8})
	if err != nil {
		t.Fatalf("NewRuntimeCoordinator() error = %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	errEvt := waitRuntimeEvent(t, c.Events(), func(evt RuntimeEvent) bool {
		return evt.Type == RuntimeEventRuntimeError
	})
	assertSanitizedRuntimeErrorEvent(t, errEvt, "watcher failed")

	stopped := waitRuntimeEvent(t, c.Events(), func(evt RuntimeEvent) bool {
		return evt.Type == RuntimeEventRuntimeStopped
	})
	if stopped.Type != RuntimeEventRuntimeStopped {
		t.Fatalf("expected runtime stopped event")
	}
}

func TestRuntimeCoordinator_RouteIncomingEmail_EmitsOTPProcessedEvent(t *testing.T) {
	runner := &fakeRuntimeRunner{}
	otp := &fakeRuntimeOTPProcessor{out: OTPPipelineResult{
		Status: OTPPipelineStatusStored,
		Event:  &OTPUIEvent{PersistedID: 99, OTPCode: "123456"},
	}}

	c, err := NewRuntimeCoordinator(runner, otp, RuntimeCoordinatorConfig{EventBuffer: 8})
	if err != nil {
		t.Fatalf("NewRuntimeCoordinator() error = %v", err)
	}

	res, err := c.RouteIncomingEmail(context.Background(), domain.IncomingEmail{To: []string{"alias@example.com"}, MessageID: "msg-1"})
	if err != nil {
		t.Fatalf("RouteIncomingEmail() error = %v", err)
	}
	if res.Status != OTPPipelineStatusStored || res.Event == nil || res.Event.PersistedID != 99 {
		t.Fatalf("unexpected route result: %+v", res)
	}

	evt := waitRuntimeEvent(t, c.Events(), func(evt RuntimeEvent) bool {
		return evt.Type == RuntimeEventOTPProcessed
	})
	if evt.OTPStatus != OTPPipelineStatusStored {
		t.Fatalf("unexpected otp processed event payload: %+v", evt)
	}
}

func TestRuntimeCoordinator_RouteIncomingEmail_PropagatesOTPError(t *testing.T) {
	runner := &fakeRuntimeRunner{}
	otpErr := errors.New("process failed")
	otp := &fakeRuntimeOTPProcessor{err: otpErr}

	c, err := NewRuntimeCoordinator(runner, otp, RuntimeCoordinatorConfig{})
	if err != nil {
		t.Fatalf("NewRuntimeCoordinator() error = %v", err)
	}

	_, err = c.RouteIncomingEmail(context.Background(), domain.IncomingEmail{To: []string{"alias@example.com"}})
	if !errors.Is(err, otpErr) {
		t.Fatalf("expected otp processor error, got %v", err)
	}
}

func TestRuntimeCoordinator_RouteIncomingEmail_NormalizeValidation(t *testing.T) {
	runner := &fakeRuntimeRunner{}
	otp := &fakeRuntimeOTPProcessor{}

	c, err := NewRuntimeCoordinator(runner, otp, RuntimeCoordinatorConfig{})
	if err != nil {
		t.Fatalf("NewRuntimeCoordinator() error = %v", err)
	}

	_, err = c.RouteIncomingEmail(context.Background(), domain.IncomingEmail{})
	if err == nil {
		t.Fatalf("expected normalization error for missing recipient")
	}
	if otp.called != 0 {
		t.Fatalf("otp processor must not be called when normalization fails")
	}
}

func TestRuntimeCoordinator_Start_ContextCancelDoesNotEmitRuntimeError(t *testing.T) {
	runner := &fakeRuntimeRunner{runFn: func(ctx context.Context, _ func(ports.WatchUpdate)) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	otp := &fakeRuntimeOTPProcessor{}

	c, err := NewRuntimeCoordinator(runner, otp, RuntimeCoordinatorConfig{EventBuffer: 8})
	if err != nil {
		t.Fatalf("NewRuntimeCoordinator() error = %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	c.Stop()

	stopped := waitRuntimeEvent(t, c.Events(), func(evt RuntimeEvent) bool {
		return evt.Type == RuntimeEventRuntimeStopped
	})
	if stopped.Type != RuntimeEventRuntimeStopped {
		t.Fatalf("expected runtime stopped event")
	}

	select {
	case evt := <-c.Events():
		if evt.Type == RuntimeEventRuntimeError {
			t.Fatalf("did not expect runtime error event on cancellation")
		}
	default:
	}
}

func TestRuntimeCoordinator_RuntimeErrorAndStoppedNotLostOnSmallBuffer(t *testing.T) {
	runErr := errors.New("watcher failed")
	runner := &fakeRuntimeRunner{runFn: func(context.Context, func(ports.WatchUpdate)) error {
		return runErr
	}}
	otp := &fakeRuntimeOTPProcessor{}

	c, err := NewRuntimeCoordinator(runner, otp, RuntimeCoordinatorConfig{EventBuffer: 1})
	if err != nil {
		t.Fatalf("NewRuntimeCoordinator() error = %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	errEvt := waitRuntimeEvent(t, c.Events(), func(evt RuntimeEvent) bool {
		return evt.Type == RuntimeEventRuntimeError
	})
	assertSanitizedRuntimeErrorEvent(t, errEvt, "watcher failed")

	stopped := waitRuntimeEvent(t, c.Events(), func(evt RuntimeEvent) bool {
		return evt.Type == RuntimeEventRuntimeStopped
	})
	if stopped.Type != RuntimeEventRuntimeStopped {
		t.Fatalf("expected runtime stopped event")
	}
}

func TestRuntimeCoordinator_CriticalEventDoesNotEvictCriticalWhenQueueFull(t *testing.T) {
	runner := &fakeRuntimeRunner{}
	otp := &fakeRuntimeOTPProcessor{}

	c, err := NewRuntimeCoordinator(runner, otp, RuntimeCoordinatorConfig{EventBuffer: 2})
	if err != nil {
		t.Fatalf("NewRuntimeCoordinator() error = %v", err)
	}

	c.emit(RuntimeEvent{Type: RuntimeEventRuntimeError, Err: "first"})
	c.emit(RuntimeEvent{Type: RuntimeEventRuntimeStopped})

	// Queue is full with critical events; this new critical event should be dropped,
	// and existing critical events must remain.
	c.emit(RuntimeEvent{Type: RuntimeEventRuntimeError, Err: "second"})

	first := waitRuntimeEvent(t, c.Events(), func(evt RuntimeEvent) bool { return true })
	second := waitRuntimeEvent(t, c.Events(), func(evt RuntimeEvent) bool { return true })

	if first.Type != RuntimeEventRuntimeError {
		t.Fatalf("expected first critical event preserved, got %+v", first)
	}
	if second.Type != RuntimeEventRuntimeStopped {
		t.Fatalf("expected runtime stopped preserved, got %+v", second)
	}

	select {
	case evt := <-c.Events():
		t.Fatalf("expected no third event, got %+v", evt)
	default:
	}
}

func TestRuntimeCoordinator_EmitRuntimeError_AlwaysSanitized(t *testing.T) {
	runner := &fakeRuntimeRunner{}
	otp := &fakeRuntimeOTPProcessor{}

	c, err := NewRuntimeCoordinator(runner, otp, RuntimeCoordinatorConfig{EventBuffer: 8})
	if err != nil {
		t.Fatalf("NewRuntimeCoordinator() error = %v", err)
	}

	c.emit(RuntimeEvent{Type: RuntimeEventRuntimeError, Err: "raw secret detail"})

	errEvt := waitRuntimeEvent(t, c.Events(), func(evt RuntimeEvent) bool {
		return evt.Type == RuntimeEventRuntimeError
	})
	assertSanitizedRuntimeErrorEvent(t, errEvt, "raw secret detail")
}

func TestRuntimeWatchRunnerAdapter_ProductionPathAcceptsGenericRuntimeRunner(t *testing.T) {
	adapterType := reflect.TypeOf(runtimeWatchRunnerAdapter{})
	field, ok := adapterType.FieldByName("runner")
	if !ok {
		t.Fatalf("runtime watch adapter should depend on generic runtime runner field to support reconnector path")
	}

	runnerIface := reflect.TypeOf((*ports.RuntimeWatchRunner)(nil)).Elem()
	if !field.Type.Implements(runnerIface) {
		t.Fatalf("runtime adapter runner field must implement ports.RuntimeWatchRunner, got %v", field.Type)
	}
}

func waitRuntimeEvent(t *testing.T, ch <-chan RuntimeEvent, predicate func(RuntimeEvent) bool) RuntimeEvent {
	t.Helper()

	timeout := time.After(2 * time.Second)
	for {
		select {
		case evt := <-ch:
			if predicate(evt) {
				return evt
			}
		case <-timeout:
			t.Fatal("timeout waiting runtime event")
		}
	}
}

func assertSanitizedRuntimeErrorEvent(t *testing.T, evt RuntimeEvent, rawErrPart string) {
	t.Helper()

	if evt.Err != runtimeWatchFailedMessage {
		t.Fatalf("unexpected sanitized runtime error message: %q", evt.Err)
	}
	if rawErrPart != "" && strings.Contains(strings.ToLower(evt.Err), strings.ToLower(rawErrPart)) {
		t.Fatalf("runtime error event leaked raw error detail: %q", evt.Err)
	}
}
