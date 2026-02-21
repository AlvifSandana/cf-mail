package imap

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

type ReconnectConfig struct {
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

type Session interface {
	Run(ctx context.Context, onUpdate func(WatchUpdate)) error
	Close() error
}

type SessionFactory func(ctx context.Context) (Session, error)

type Reconnector struct {
	factory SessionFactory
	cfg     ReconnectConfig
	maxStep int

	randFloatFn func() float64
	sleepFn     func(context.Context, time.Duration) error
	nowFn       func() time.Time
}

func NewReconnector(factory SessionFactory, cfg ReconnectConfig) (*Reconnector, error) {
	if factory == nil {
		return nil, fmt.Errorf("imap reconnector factory is nil")
	}

	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 500 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	if cfg.MaxBackoff < cfg.BaseBackoff {
		cfg.MaxBackoff = cfg.BaseBackoff
	}

	return &Reconnector{
		factory:     factory,
		cfg:         cfg,
		maxStep:     maxBackoffStep(cfg.BaseBackoff, cfg.MaxBackoff),
		randFloatFn: defaultRandFloat,
		sleepFn:     sleepWithContext,
		nowFn:       time.Now,
	}, nil
}

func (r *Reconnector) Run(ctx context.Context, onUpdate func(WatchUpdate)) error {
	if r == nil {
		return fmt.Errorf("imap reconnector is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if onUpdate == nil {
		onUpdate = func(WatchUpdate) {}
	}

	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}

		session, err := r.factory(ctx)
		if err != nil {
			onUpdate(WatchUpdate{Mode: "reconnecting", Timestamp: r.nowFn().UTC()})
			if err := r.waitBackoff(ctx, attempt); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				return fmt.Errorf("reconnect backoff wait: %w", err)
			}
			attempt = r.nextStep(attempt)
			continue
		}

		onUpdate(WatchUpdate{Mode: "connected", Timestamp: r.nowFn().UTC()})

		runErr := session.Run(ctx, onUpdate)
		closeErr := session.Close()
		if closeErr != nil {
			if runErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close session: %w", closeErr))
			} else {
				runErr = fmt.Errorf("close session: %w", closeErr)
			}
		}

		if ctx.Err() != nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return nil
		}

		if runErr == nil {
			attempt = 0
			continue
		}

		onUpdate(WatchUpdate{Mode: "reconnecting", Timestamp: r.nowFn().UTC()})
		if err := r.waitBackoff(ctx, attempt); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("reconnect backoff wait: %w", err)
		}
		attempt = r.nextStep(attempt)
	}
}

func (r *Reconnector) waitBackoff(ctx context.Context, attempt int) error {
	return r.sleepFn(ctx, r.backoffDuration(attempt))
}

func (r *Reconnector) backoffDuration(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > r.maxStep {
		attempt = r.maxStep
	}

	multiplier := math.Pow(2, float64(attempt))
	base := time.Duration(float64(r.cfg.BaseBackoff) * multiplier)
	if base > r.cfg.MaxBackoff {
		base = r.cfg.MaxBackoff
	}

	// jitter in range [0.5, 1.5)
	jitter := 0.5 + clamp01(r.randFloatFn())
	d := time.Duration(float64(base) * jitter)
	if d > r.cfg.MaxBackoff {
		d = r.cfg.MaxBackoff
	}
	if d <= 0 {
		return r.cfg.BaseBackoff
	}

	return d
}

func (r *Reconnector) nextStep(current int) int {
	if current >= r.maxStep {
		return r.maxStep
	}
	return current + 1
}

func maxBackoffStep(base, max time.Duration) int {
	if base <= 0 || max <= 0 || max <= base {
		return 0
	}

	step := 0
	v := base
	for v < max {
		if v > max/2 {
			break
		}
		v *= 2
		step++
	}

	return step
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func defaultRandFloat() float64 {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return 0.5
	}

	n := binary.BigEndian.Uint64(b[:])
	return float64(n) / float64(^uint64(0))
}
