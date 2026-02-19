package imap

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeWatchClient struct {
	idleCalls int
	pollCalls int

	idleErr error
	pollErr error

	idleOnUpdateCalls int
}

func (f *fakeWatchClient) Idle(ctx context.Context, onUpdate func() error) error {
	f.idleCalls++
	if onUpdate != nil {
		f.idleOnUpdateCalls++
		_ = onUpdate()
	}
	if f.idleErr != nil {
		return f.idleErr
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeWatchClient) Poll(context.Context) error {
	f.pollCalls++
	if f.pollErr != nil {
		return f.pollErr
	}
	return nil
}

func TestNewWatcher_DefaultPollInterval(t *testing.T) {
	w, err := NewWatcher(&fakeWatchClient{}, WatcherConfig{})
	if err != nil {
		t.Fatalf("NewWatcher() error = %v", err)
	}
	if w.cfg.PollInterval != 5*time.Second {
		t.Fatalf("expected default poll interval 5s, got %v", w.cfg.PollInterval)
	}
}

func TestWatcher_Run_IdleEnabled_UsesIdleThenStopsOnCancel(t *testing.T) {
	fc := &fakeWatchClient{}
	w, err := NewWatcher(fc, WatcherConfig{EnableIdle: true, PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewWatcher() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	updates := make([]WatchUpdate, 0)
	mu := sync.Mutex{}

	done := make(chan error, 1)
	go func() {
		done <- w.Run(ctx, func(u WatchUpdate) {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, u)
			cancel()
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() timeout")
	}

	if fc.idleCalls != 1 {
		t.Fatalf("expected one idle call, got %d", fc.idleCalls)
	}
	if fc.pollCalls != 0 {
		t.Fatalf("expected no poll calls while idle active, got %d", fc.pollCalls)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(updates) == 0 || updates[0].Mode != "idle" {
		t.Fatalf("expected idle update, got %+v", updates)
	}
}

func TestWatcher_Run_IdleUnsupported_FallbackToPolling(t *testing.T) {
	fc := &fakeWatchClient{idleErr: ErrIdleUnsupported}
	w, err := NewWatcher(fc, WatcherConfig{EnableIdle: true, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("NewWatcher() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	updates := make([]WatchUpdate, 0)
	mu := sync.Mutex{}

	w.sleepFn = func(context.Context, time.Duration) error { return nil }

	done := make(chan error, 1)
	go func() {
		done <- w.Run(ctx, func(u WatchUpdate) {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, u)
			if u.Mode == "poll" {
				cancel()
			}
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() timeout")
	}

	if fc.idleCalls != 1 {
		t.Fatalf("expected one idle attempt, got %d", fc.idleCalls)
	}
	if fc.pollCalls == 0 {
		t.Fatalf("expected poll calls after idle fallback")
	}

	mu.Lock()
	defer mu.Unlock()
	foundPoll := false
	for _, u := range updates {
		if u.Mode == "poll" {
			foundPoll = true
			break
		}
	}
	if !foundPoll {
		t.Fatalf("expected at least one poll update, got %+v", updates)
	}
}

func TestWatcher_Run_IdleError_PropagatesError(t *testing.T) {
	idleErr := errors.New("idle failed")
	fc := &fakeWatchClient{idleErr: idleErr}
	w, err := NewWatcher(fc, WatcherConfig{EnableIdle: true, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("NewWatcher() error = %v", err)
	}

	err = w.Run(context.Background(), nil)
	if !errors.Is(err, idleErr) {
		t.Fatalf("expected idle error to propagate, got %v", err)
	}

	if fc.idleCalls != 1 {
		t.Fatalf("expected one idle attempt, got %d", fc.idleCalls)
	}
	if fc.pollCalls != 0 {
		t.Fatalf("expected no polling fallback for generic idle error")
	}
}

func TestWatcher_Run_PollOnly_PropagatesPollError(t *testing.T) {
	fc := &fakeWatchClient{pollErr: errors.New("poll failed")}
	w, err := NewWatcher(fc, WatcherConfig{EnableIdle: false, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("NewWatcher() error = %v", err)
	}

	err = w.Run(context.Background(), nil)
	if err == nil || err.Error() == "" {
		t.Fatalf("expected non-empty poll error")
	}
	if fc.idleCalls != 0 {
		t.Fatalf("expected no idle call in poll-only mode")
	}
	if fc.pollCalls != 1 {
		t.Fatalf("expected one poll call before error, got %d", fc.pollCalls)
	}
}

func TestWatcher_Run_PollOnly_CancelStopsGracefully(t *testing.T) {
	fc := &fakeWatchClient{}
	w, err := NewWatcher(fc, WatcherConfig{EnableIdle: false, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("NewWatcher() error = %v", err)
	}
	w.sleepFn = func(ctx context.Context, _ time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- w.Run(ctx, nil)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected graceful stop on cancel, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() timeout")
	}
	if fc.pollCalls == 0 {
		t.Fatalf("expected at least one poll call before cancel")
	}
}

func TestWatcher_WatchUpdate_ExposesIncomingEmailPayloadContract(t *testing.T) {
	updateType := reflect.TypeOf(WatchUpdate{})
	field, ok := updateType.FieldByName("IncomingEmail")
	if !ok {
		t.Fatalf("watch update must expose IncomingEmail payload field for runtime ingestion")
	}

	if field.Type != reflect.TypeOf(IncomingEmail{}) {
		t.Fatalf("IncomingEmail payload field must be type imap.IncomingEmail, got %v", field.Type)
	}
}
