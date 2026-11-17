package hid

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

// Transport prepares/resets the target before an action and transmits its frames.
// A changed generation means Prepare released all keyboard and mouse input.
type Transport interface {
	Prepare(context.Context) (uint64, error)
	WriteFrame(context.Context, []byte) error
	Close() error
}

// ErrClosed means the controller no longer admits input actions.
var ErrClosed = errors.New("HID controller closed")

// Controller serializes complete keyboard and mouse actions on one transport.
// It supports one operator; it does not merge independently held input.
type Controller struct {
	transport Transport
	gate      chan struct{}
	lifetime  context.Context
	stop      context.CancelFunc
	once      sync.Once
	closeErr  error

	mu       sync.Mutex // admission and the active-cleanup handoff to Close
	closing  bool
	active   *cleanupAttempt
	shutdown *cleanupAttempt

	// The action gate protects transport generation and held input state.
	prepared   bool
	generation uint64
	modifier   byte
	keys       []byte
	buttons    byte
}

// NewController creates a controller without opening hardware. A nil transport
// is a programming error and panics. Close owns the transport's final close.
func NewController(transport Transport) *Controller {
	if transport == nil {
		panic("hid: nil transport")
	}
	lifetime, stop := context.WithCancel(context.Background())
	return &Controller{transport: transport, gate: make(chan struct{}, 1), lifetime: lifetime, stop: stop}
}

type action struct {
	controller *Controller
	ctx        context.Context
	dirty      bool
}

func (controller *Controller) isClosing() bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.closing
}

func (controller *Controller) run(ctx context.Context, execute func(*action) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-controller.lifetime.Done():
		return ErrClosed
	case controller.gate <- struct{}{}:
	}
	defer func() { <-controller.gate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if controller.isClosing() {
		return ErrClosed
	}
	activeCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(controller.lifetime, cancel)
	defer stop()
	defer cancel()
	if _, err := controller.prepare(activeCtx); err != nil {
		return err
	}
	current := &action{controller: controller, ctx: activeCtx}
	err := execute(current)
	if err == nil {
		err = activeCtx.Err()
	}
	if err != nil && current.dirty {
		attempt := controller.beginCleanup(ctx)
		cleanupErr := controller.cleanup(attempt, controller.isClosing())
		controller.endCleanup(attempt)
		err = errors.Join(err, cleanupErr)
	}
	return err
}

func (controller *Controller) prepare(ctx context.Context) (bool, error) {
	generation, err := controller.transport.Prepare(ctx)
	if err != nil {
		return false, err
	}
	changed := !controller.prepared || controller.generation != generation
	if changed {
		controller.modifier, controller.keys, controller.buttons = 0, nil, 0
	}
	controller.prepared, controller.generation = true, generation
	return changed, nil
}

func (current *action) write(frame []byte) error {
	if err := current.ctx.Err(); err != nil {
		return err
	}
	current.dirty = true // A failed send may already have reached the target.
	return current.controller.transport.WriteFrame(current.ctx, frame)
}

type cleanupAttempt struct {
	ctx      context.Context
	cancel   context.CancelFunc
	prepared bool
	failed   bool
	keyboard bool
	mouse    bool
	err      error
}

func (controller *Controller) beginCleanup(ctx context.Context) *cleanupAttempt {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	attempt := &cleanupAttempt{ctx: cleanupCtx, cancel: cancel}
	controller.mu.Lock()
	controller.active = attempt
	if controller.closing {
		controller.shutdown = attempt
	}
	controller.mu.Unlock()
	return attempt
}

func (controller *Controller) endCleanup(attempt *cleanupAttempt) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.active = nil
	if controller.closing {
		controller.shutdown = attempt
	} else {
		attempt.cancel()
	}
}

// cleanup runs only with the gate held. One preparation and at most one attempt
// per release report are shared if shutdown overlaps ordinary keyboard cleanup.
func (controller *Controller) cleanup(attempt *cleanupAttempt, all bool) error {
	if !attempt.prepared {
		attempt.prepared = true
		changed, err := controller.prepare(attempt.ctx)
		if err != nil {
			attempt.failed = true
			attempt.err = fmt.Errorf("prepare release: %w", err)
		} else if changed {
			attempt.keyboard, attempt.mouse = true, true
		}
	}
	if attempt.failed {
		return attempt.err
	}
	if !attempt.keyboard {
		attempt.keyboard = true
		err := controller.transport.WriteFrame(attempt.ctx, ch9329.Keyboard(0))
		if err != nil {
			attempt.err = errors.Join(attempt.err, fmt.Errorf("release keyboard: %w", err))
		} else {
			controller.modifier, controller.keys = 0, nil
		}
	}
	if all && !attempt.mouse {
		attempt.mouse = true
		err := controller.transport.WriteFrame(attempt.ctx, ch9329.MouseRelative(0, 0, 0, 0))
		if err != nil {
			attempt.err = errors.Join(attempt.err, fmt.Errorf("release mouse: %w", err))
		} else {
			controller.buttons = 0
		}
	}
	return attempt.err
}

// Close rejects new actions, cancels active application waits, attempts release
// before permanently closing the transport, and shares its result with other
// Close callers. Its cleanup budget cannot interrupt an executing kernel call.
func (controller *Controller) Close() error {
	controller.once.Do(func() {
		controller.mu.Lock()
		controller.closing = true
		controller.stop()
		controller.shutdown = controller.active
		controller.mu.Unlock()
		controller.gate <- struct{}{}
		defer func() { <-controller.gate }()
		controller.mu.Lock()
		attempt := controller.shutdown
		controller.mu.Unlock()
		if controller.prepared {
			if attempt == nil {
				attempt = controller.beginCleanup(context.Background())
			}
			controller.closeErr = controller.cleanup(attempt, true)
			attempt.cancel()
		}
		controller.closeErr = errors.Join(controller.closeErr, controller.transport.Close())
	})
	return controller.closeErr
}
