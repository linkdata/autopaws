package hid

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

// Transport prepares/resets the target before an action and transmits its frames.
// A changed generation resets keyboard/relative input; Controller also releases
// remembered absolute holds before admitting fresh input.
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

	// The action gate protects transport generation and held mouse buttons.
	prepared                         bool
	generation                       uint64
	buttons                          byte
	absoluteButtons, relativeButtons byte
	absolutePending, relativePending byte
	absolutePosition                 Point
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

type releaseMask byte

const (
	releaseKeyboard releaseMask = 1 << iota
	releaseMouse
	releaseBoth = releaseKeyboard | releaseMouse
)

func (controller *Controller) isClosing() bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.closing
}

func (controller *Controller) acquire(ctx context.Context) error {
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
	if err := ctx.Err(); err != nil {
		<-controller.gate
		return err
	}
	if controller.isClosing() {
		<-controller.gate
		return ErrClosed
	}
	return nil
}

func (controller *Controller) run(ctx context.Context, release releaseMask, execute func(*action) error) error {
	if err := controller.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-controller.gate }()
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
		if controller.isClosing() {
			release = releaseBoth
		}
		cleanupErr := controller.cleanup(attempt, release)
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
		// Transport preparation resets keyboard and relative reports. An absolute
		// hold survives that reset and must be released on its own report ID.
		controller.relativeButtons, controller.relativePending = 0, 0
		if controller.absoluteButtons|controller.absolutePending != 0 {
			point := controller.absolutePosition
			if err := controller.writeReport(ctx, ch9329.MouseAbsolute(0, point.X, point.Y, 0)); err != nil {
				return false, fmt.Errorf("prepare absolute mouse release: %w", err)
			}
		}
		controller.buttons = 0
	}
	controller.prepared, controller.generation = true, generation
	return changed, nil
}

func (current *action) write(frame []byte) error {
	if err := current.ctx.Err(); err != nil {
		return err
	}
	current.dirty = true // A failed send may already have reached the target.
	return current.controller.writeFrame(current.ctx, frame)
}

// Failed writes may already have reached the chip. Retain both the previous
// and attempted holds until that report ID is successfully updated or reset.
func (controller *Controller) writeFrame(ctx context.Context, frame []byte) error {
	if len(frame) >= 12 && frame[3] == 0x04 && binary.LittleEndian.Uint16(frame[7:9]) == 0 && binary.LittleEndian.Uint16(frame[9:11]) == 0 {
		// Some targets ignore all-zero absolute motion. Preposition without
		// pressing new buttons, then send the exact requested coordinates.
		held := controller.absoluteButtons &^ controller.absolutePending
		if err := controller.writeReport(ctx, ch9329.MouseAbsolute(held, 0, 1, 0)); err != nil {
			return err
		}
	}
	return controller.writeReport(ctx, frame)
}

func (controller *Controller) writeReport(ctx context.Context, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var held, pending *byte
	if len(frame) >= 12 && frame[3] == 0x04 {
		held, pending = &controller.absoluteButtons, &controller.absolutePending
		controller.absolutePosition = Point{int(binary.LittleEndian.Uint16(frame[7:9])), int(binary.LittleEndian.Uint16(frame[9:11]))}
	} else if len(frame) >= 11 && frame[3] == 0x05 {
		held, pending = &controller.relativeButtons, &controller.relativePending
	}
	if held != nil {
		*pending |= *held | frame[6]
	}
	err := controller.transport.WriteFrame(ctx, frame)
	if err == nil && held != nil {
		*held = frame[6]
		*pending = 0
		controller.buttons = controller.absoluteButtons | controller.relativeButtons
	}
	return err
}

type cleanupAttempt struct {
	ctx           context.Context
	cancel        context.CancelFunc
	prepared      bool
	failed        bool
	keyboard      bool
	mouseAbsolute bool
	mouseRelative bool
	err           error
}

func (controller *Controller) beginCleanup(ctx context.Context) *cleanupAttempt {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	return controller.registerCleanup(cleanupCtx, cancel)
}

func (controller *Controller) registerCleanup(ctx context.Context, cancel context.CancelFunc) *cleanupAttempt {
	attempt := &cleanupAttempt{ctx: ctx, cancel: cancel}
	controller.mu.Lock()
	controller.active = attempt
	if controller.closing {
		controller.shutdown = attempt
	}
	controller.mu.Unlock()
	return attempt
}

// releaseHeld is itself cleanup, so it uses the existing attempt/handoff rather
// than running a normal action that might start another cleanup on failure.
// Its deadline includes admission and is shared by an overlapping Close.
func (controller *Controller) releaseHeld(ctx context.Context, release releaseMask) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	deadline := time.Now().Add(time.Second)
	if earlier, ok := ctx.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	cleanupCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	if err := controller.acquire(cleanupCtx); err != nil {
		cancel()
		return false, err
	}
	defer func() { <-controller.gate }()
	attempt := controller.registerCleanup(cleanupCtx, cancel)
	err := controller.cleanup(attempt, release)
	controller.endCleanup(attempt)
	return true, err
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
// per release report are shared if shutdown overlaps an ordinary input cleanup.
func (controller *Controller) cleanup(attempt *cleanupAttempt, release releaseMask) error {
	if !attempt.prepared {
		attempt.prepared = true
		changed, err := controller.prepare(attempt.ctx)
		if err != nil {
			attempt.failed = true
			attempt.err = fmt.Errorf("prepare release: %w", err)
		} else if changed {
			attempt.keyboard, attempt.mouseAbsolute, attempt.mouseRelative = true, true, true
		}
	}
	if attempt.failed {
		return attempt.err
	}
	if release&releaseKeyboard != 0 && !attempt.keyboard {
		attempt.keyboard = true
		err := controller.transport.WriteFrame(attempt.ctx, ch9329.Keyboard(0))
		if err != nil {
			attempt.err = errors.Join(attempt.err, fmt.Errorf("release keyboard: %w", err))
		}
	}
	if release&releaseMouse != 0 && !attempt.mouseAbsolute {
		attempt.mouseAbsolute = true
		if controller.absoluteButtons|controller.absolutePending != 0 {
			point := controller.absolutePosition
			// Release-only cleanup must never preposition with a held mask.
			if err := controller.writeReport(attempt.ctx, ch9329.MouseAbsolute(0, point.X, point.Y, 0)); err != nil {
				attempt.err = errors.Join(attempt.err, fmt.Errorf("release absolute mouse: %w", err))
			}
		}
	}
	if release&releaseMouse != 0 && !attempt.mouseRelative {
		attempt.mouseRelative = true
		err := controller.writeFrame(attempt.ctx, ch9329.MouseRelative(0, 0, 0, 0))
		if err != nil {
			attempt.err = errors.Join(attempt.err, fmt.Errorf("release mouse: %w", err))
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
			controller.closeErr = controller.cleanup(attempt, releaseBoth)
			attempt.cancel()
		}
		controller.closeErr = errors.Join(controller.closeErr, controller.transport.Close())
	})
	return controller.closeErr
}
