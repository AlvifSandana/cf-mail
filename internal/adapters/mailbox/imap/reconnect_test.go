package imap

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSession struct {
	runCalls   int
	closeCalls int

	runFn    func(context.Context, func(WatchUpdate)) error
	closeErr error
}

func (f *fakeSession) Run(ctx context.Context, onUpdate func(WatchUpdate)) error {
	f.runCalls++
	if f.runFn != nil {
		return f.runFn(ctx, onUpdate)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeSession) Close() error {
	f.closeCalls++
	return f.closeErr
}

func TestNewReconnector(t *testing.T) {
	t.Run("reject nil factory", func(t *testing.T) {
		_, err := NewReconnector(nil, ReconnectConfig{})
		if err == nil {
			t.Fatalf("expected nil factory error")
		}
	})

	t.Run("apply defaults", func(t *testing.T) {
		r, err := NewReconnector(func(context.Context) (Session, error) {
			return &fakeSession{}, nil
		}, ReconnectConfig{})
		if err != nil {
			t.Fatalf("NewReconnector() error = %v", err)
		}
		if r.cfg.BaseBackoff != 500*time.Millisecond {
			t.Fatalf("unexpected default base backoff: %v", r.cfg.BaseBackoff)
		}
		if r.cfg.MaxBackoff != 30*time.Second {
			t.Fatalf("unexpected default max backoff: %v", r.cfg.MaxBackoff)
		}
	})
}

func TestReconnector_BackoffDuration_BoundedAndJittered(t *testing.T) {
	r, err := NewReconnector(func(context.Context) (Session, error) {
		return &fakeSession{}, nil
	}, ReconnectConfig{BaseBackoff: time.Second, MaxBackoff: 8 * time.Second})
	if err != nil {
		t.Fatalf("NewReconnector() error = %v", err)
	}

	r.randFloatFn = func() float64 { return 0 }
	if got := r.backoffDuration(0); got != 500*time.Millisecond {
		t.Fatalf("attempt0 jitter-low expected 500ms, got %v", got)
	}

	r.randFloatFn = func() float64 { return 1 }
	if got := r.backoffDuration(0); got != 1500*time.Millisecond {
		t.Fatalf("attempt0 jitter-high expected 1.5s, got %v", got)
	}

	r.randFloatFn = func() float64 { return 1 }
	if got := r.backoffDuration(10); got != 8*time.Second {
		t.Fatalf("expected max capped backoff 8s, got %v", got)
	}
}

func TestReconnector_Run_RetryFactoryFailureThenStopOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	created := 0
	session := &fakeSession{
		runFn: func(context.Context, func(WatchUpdate)) error {
			cancel()
			return context.Canceled
		},
	}

	factory := func(context.Context) (Session, error) {
		created++
		if created < 3 {
			return nil, errors.New("dial failed")
		}
		return session, nil
	}

	r, err := NewReconnector(factory, ReconnectConfig{BaseBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewReconnector() error = %v", err)
	}

	sleeps := 0
	r.sleepFn = func(context.Context, time.Duration) error {
		sleeps++
		return nil
	}

	if err := r.Run(ctx, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if created != 3 {
		t.Fatalf("expected 3 factory calls, got %d", created)
	}
	if sleeps != 2 {
		t.Fatalf("expected 2 backoff sleeps, got %d", sleeps)
	}
	if session.runCalls != 1 || session.closeCalls != 1 {
		t.Fatalf("expected single session run+close, got run=%d close=%d", session.runCalls, session.closeCalls)
	}
}

func TestReconnector_Run_RetryOnSessionError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := &fakeSession{runFn: func(context.Context, func(WatchUpdate)) error {
		return errors.New("watch failed")
	}}
	second := &fakeSession{runFn: func(context.Context, func(WatchUpdate)) error {
		cancel()
		return context.Canceled
	}}

	created := 0
	factory := func(context.Context) (Session, error) {
		created++
		if created == 1 {
			return first, nil
		}
		return second, nil
	}

	r, err := NewReconnector(factory, ReconnectConfig{BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewReconnector() error = %v", err)
	}

	sleeps := 0
	r.sleepFn = func(context.Context, time.Duration) error {
		sleeps++
		return nil
	}

	if err := r.Run(ctx, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if created != 2 {
		t.Fatalf("expected 2 sessions created, got %d", created)
	}
	if sleeps != 1 {
		t.Fatalf("expected one backoff sleep between retries, got %d", sleeps)
	}
	if first.closeCalls != 1 || second.closeCalls != 1 {
		t.Fatalf("expected close called on both sessions, got first=%d second=%d", first.closeCalls, second.closeCalls)
	}
}

func TestReconnector_Run_CloseErrorTriggersRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := &fakeSession{
		runFn:    func(context.Context, func(WatchUpdate)) error { return nil },
		closeErr: errors.New("close failed"),
	}
	second := &fakeSession{runFn: func(context.Context, func(WatchUpdate)) error {
		cancel()
		return context.Canceled
	}}

	created := 0
	r, err := NewReconnector(func(context.Context) (Session, error) {
		created++
		if created == 1 {
			return first, nil
		}
		return second, nil
	}, ReconnectConfig{BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewReconnector() error = %v", err)
	}

	sleeps := 0
	r.sleepFn = func(context.Context, time.Duration) error {
		sleeps++
		return nil
	}

	if err := r.Run(ctx, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if created != 2 {
		t.Fatalf("expected retry after close error, created=%d", created)
	}
	if sleeps != 1 {
		t.Fatalf("expected one backoff sleep, got %d", sleeps)
	}
}
