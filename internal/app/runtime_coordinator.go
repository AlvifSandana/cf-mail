package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

const defaultRuntimeEventBuffer = 64
const minRuntimeEventBuffer = 2
const maxRuntimeEventBuffer = 4096
const runtimeWatchFailedMessage = "runtime watch failed"

type RuntimeEventType string

const (
	RuntimeEventWatcherUpdate  RuntimeEventType = "watcher_update"
	RuntimeEventOTPProcessed   RuntimeEventType = "otp_processed"
	RuntimeEventRuntimeError   RuntimeEventType = "runtime_error"
	RuntimeEventRuntimeStopped RuntimeEventType = "runtime_stopped"
)

type RuntimeEvent struct {
	Type      RuntimeEventType
	RunID     uint64
	Timestamp time.Time
	Watch     *ports.WatchUpdate
	OTPStatus OTPPipelineStatus
	Err       string
}

type RuntimeCoordinatorConfig struct {
	EventBuffer int
}

type runtimeWatchRunner = ports.RuntimeWatchRunner

type runtimeOTPProcessor interface {
	ProcessNormalizedEmail(ctx context.Context, in domain.IncomingEmail) (OTPPipelineResult, error)
}

type RuntimeCoordinator struct {
	runner runtimeWatchRunner
	otp    runtimeOTPProcessor

	events chan RuntimeEvent
	nowFn  func() time.Time

	mu      sync.Mutex
	emitMu  sync.Mutex
	started bool
	runID   uint64
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewRuntimeCoordinator(runner runtimeWatchRunner, otp runtimeOTPProcessor, cfg RuntimeCoordinatorConfig) (*RuntimeCoordinator, error) {
	if runner == nil {
		return nil, domain.WrapValidation("runtime coordinator watcher runner is nil", nil)
	}
	if otp == nil {
		return nil, domain.WrapValidation("runtime coordinator otp processor is nil", nil)
	}

	if cfg.EventBuffer <= 0 {
		cfg.EventBuffer = defaultRuntimeEventBuffer
	}
	if cfg.EventBuffer < minRuntimeEventBuffer {
		cfg.EventBuffer = minRuntimeEventBuffer
	}
	if cfg.EventBuffer > maxRuntimeEventBuffer {
		cfg.EventBuffer = maxRuntimeEventBuffer
	}

	return &RuntimeCoordinator{
		runner: runner,
		otp:    otp,
		events: make(chan RuntimeEvent, cfg.EventBuffer),
		nowFn:  time.Now,
	}, nil
}

func (c *RuntimeCoordinator) Start(ctx context.Context) error {
	if c == nil {
		return domain.WrapValidation("runtime coordinator is nil", nil)
	}
	if c.runner == nil {
		return domain.WrapValidation("runtime coordinator watcher runner is nil", nil)
	}
	if c.nowFn == nil {
		return domain.WrapValidation("runtime coordinator nowFn is nil", nil)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return domain.WrapValidation("runtime coordinator already started", nil)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	runID := c.runID + 1
	c.runID = runID
	c.cancel = cancel
	c.done = done
	c.started = true
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			c.started = false
			c.cancel = nil
			c.done = nil
			c.mu.Unlock()

			c.emit(RuntimeEvent{Type: RuntimeEventRuntimeStopped, RunID: runID})
			close(done)
		}()

		err := c.runner.Run(runCtx, func(update ports.WatchUpdate) {
			u := update
			c.emit(RuntimeEvent{Type: RuntimeEventWatcherUpdate, RunID: runID, Watch: &u})
		})
		if err != nil && !isContextCancellation(err) {
			c.emitRuntimeError(runID, err)
		}
	}()

	return nil
}

func (c *RuntimeCoordinator) Stop() {
	if c == nil {
		return
	}

	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (c *RuntimeCoordinator) Events() <-chan RuntimeEvent {
	if c == nil {
		return nil
	}
	return c.events
}

func (c *RuntimeCoordinator) RouteIncomingEmail(ctx context.Context, in domain.IncomingEmail) (OTPPipelineResult, error) {
	if c == nil {
		return OTPPipelineResult{}, domain.WrapValidation("runtime coordinator is nil", nil)
	}
	if c.otp == nil {
		return OTPPipelineResult{}, domain.WrapValidation("runtime coordinator otp processor is nil", nil)
	}
	if c.nowFn == nil {
		return OTPPipelineResult{}, domain.WrapValidation("runtime coordinator nowFn is nil", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	normalized, err := normalizeIncomingEmail(in)
	if err != nil {
		return OTPPipelineResult{}, domain.WrapValidation("normalize incoming email", err)
	}

	result, err := c.otp.ProcessNormalizedEmail(ctx, normalized)
	if err != nil {
		return OTPPipelineResult{}, err
	}

	c.emit(RuntimeEvent{Type: RuntimeEventOTPProcessed, RunID: c.currentRunID(), OTPStatus: result.Status})

	return result, nil
}

func (a *App) NewRuntimeCoordinator(runner runtimeWatchRunner, cfg RuntimeCoordinatorConfig) (*RuntimeCoordinator, error) {
	if a == nil {
		return nil, domain.WrapValidation("app is nil", nil)
	}
	if a.otpService == nil {
		return nil, domain.WrapValidation("app otp service is nil", nil)
	}

	return NewRuntimeCoordinator(runner, a.otpService, cfg)
}

func (c *RuntimeCoordinator) emit(evt RuntimeEvent) {
	if c == nil || c.events == nil || c.nowFn == nil {
		return
	}
	if evt.Type == RuntimeEventRuntimeError {
		evt.Err = runtimeWatchFailedMessage
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = c.nowFn().UTC()
	}

	c.emitMu.Lock()
	defer c.emitMu.Unlock()

	if !isCriticalRuntimeEvent(evt.Type) {
		select {
		case c.events <- evt:
		default:
		}
		return
	}

	select {
	case c.events <- evt:
	default:
		c.enqueueCriticalLocked(evt)
	}
}

func (c *RuntimeCoordinator) emitRuntimeError(runID uint64, _ error) {
	c.emit(RuntimeEvent{Type: RuntimeEventRuntimeError, RunID: runID, Err: runtimeWatchFailedMessage})
}

func (c *RuntimeCoordinator) enqueueCriticalLocked(evt RuntimeEvent) {
	if c == nil || c.events == nil {
		return
	}

	capacity := cap(c.events)
	if capacity <= 0 {
		return
	}

	buffered := make([]RuntimeEvent, 0, capacity)
	for i := 0; i < capacity; i++ {
		select {
		case e := <-c.events:
			buffered = append(buffered, e)
		default:
			i = capacity
		}
	}

	filtered := make([]RuntimeEvent, 0, len(buffered)+1)
	for _, e := range buffered {
		if !isCriticalRuntimeEvent(e.Type) {
			continue
		}
		if e.RunID != evt.RunID {
			continue
		}
		filtered = append(filtered, e)
	}

	if len(filtered) < capacity {
		filtered = append(filtered, evt)
	} else {
		replaced := false
		for i := range filtered {
			if filtered[i].Type == evt.Type && filtered[i].RunID == evt.RunID {
				filtered[i] = evt
				replaced = true
				break
			}
		}
		if !replaced {
			// keep existing critical set if no safe slot.
		}
	}

	if len(filtered) > capacity {
		filtered = filtered[len(filtered)-capacity:]
	}

	for _, e := range filtered {
		select {
		case c.events <- e:
		default:
			return
		}
	}
}

func isCriticalRuntimeEvent(t RuntimeEventType) bool {
	return t == RuntimeEventRuntimeError || t == RuntimeEventRuntimeStopped
}

func (c *RuntimeCoordinator) currentRunID() uint64 {
	if c == nil {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runID
}

func isContextCancellation(err error) bool {
	if err == nil {
		return false
	}

	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
