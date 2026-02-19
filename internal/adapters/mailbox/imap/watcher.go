package imap

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrIdleUnsupported = errors.New("imap idle unsupported")

type WatcherConfig struct {
	EnableIdle   bool
	PollInterval time.Duration
}

type WatchUpdate struct {
	Mode          string
	Timestamp     time.Time
	IncomingEmail IncomingEmail
}

type WatchClient interface {
	Idle(ctx context.Context, onUpdate func() error) error
	Poll(ctx context.Context) ([]IncomingEmail, error)
}

type Watcher struct {
	client WatchClient
	cfg    WatcherConfig

	nowFn   func() time.Time
	sleepFn func(context.Context, time.Duration) error
}

func NewWatcher(client WatchClient, cfg WatcherConfig) (*Watcher, error) {
	if client == nil {
		return nil, fmt.Errorf("imap watcher client is nil")
	}

	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}

	return &Watcher{
		client:  client,
		cfg:     cfg,
		nowFn:   time.Now,
		sleepFn: sleepWithContext,
	}, nil
}

func (w *Watcher) Run(ctx context.Context, onUpdate func(WatchUpdate)) error {
	if w == nil {
		return fmt.Errorf("imap watcher is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if onUpdate == nil {
		onUpdate = func(WatchUpdate) {}
	}

	if w.cfg.EnableIdle {
		err := w.client.Idle(ctx, func() error {
			onUpdate(WatchUpdate{Mode: "idle", Timestamp: w.nowFn().UTC()})
			return nil
		})
		if err == nil {
			if ctx.Err() != nil {
				return nil
			}
			return w.runPolling(ctx, onUpdate)
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return nil
		}

		// Fallback to polling only when IDLE is unavailable.
		if errors.Is(err, ErrIdleUnsupported) {
			return w.runPolling(ctx, onUpdate)
		}

		return fmt.Errorf("idle watch mailbox: %w", err)
	}

	return w.runPolling(ctx, onUpdate)
}

func (w *Watcher) runPolling(ctx context.Context, onUpdate func(WatchUpdate)) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		incoming, err := w.client.Poll(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("poll mailbox: %w", err)
		}

		if len(incoming) == 0 {
			onUpdate(WatchUpdate{Mode: "poll", Timestamp: w.nowFn().UTC()})
		} else {
			for _, msg := range incoming {
				onUpdate(WatchUpdate{Mode: "poll", Timestamp: w.nowFn().UTC(), IncomingEmail: msg})
			}
		}

		if err := w.sleepFn(ctx, w.cfg.PollInterval); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("poll interval wait: %w", err)
		}
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
