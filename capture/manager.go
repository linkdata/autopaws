//go:build (linux || darwin || windows) && (amd64 || arm64)

package capture

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/linkdata/autopaws/internal/monotime"
	"github.com/linkdata/autopaws/v4l2"
)

const snapshotTimeout = 5 * time.Second

var (
	ErrUnavailable    = errors.New("capture unavailable")
	ErrCaptureChanged = errors.New("capture changed")
	ErrClosed         = errors.New("capture manager closed")
	ErrOptions        = errors.New("invalid capture options")
)

// Source is one owned stream. Close must join its reads and release ownership;
// it may outlast a context while a synchronous driver call is executing.
type Source interface {
	ReadFrame(context.Context) (v4l2.Frame, error)
	Format() v4l2.Format
	Close() error
}

// Options contains selected settings and narrow source/clock seams. Nil hooks
// use the platform capture backend and host monotonic clock. The FFmpeg source
// retries failures while demand remains. Zero IdleTimeout uses ten seconds.
type Options struct {
	Device                    string
	Width, Height             uint32
	Autocrop                  bool
	ScreenWidth, ScreenHeight int // Zero disables autocrop; otherwise the target output aspect.
	OpenSource                func(context.Context, string, v4l2.Options) (Source, error)
	MonotonicNow              func() (int64, error)
	IdleTimeout               time.Duration
}

// Info reports selected/requested settings separately from negotiated Format.
// Idle is available selection, not a device-open failure. Err retains the last
// source failure until a retry or settings change succeeds.
type Info struct {
	Device        string
	Width, Height uint32
	Autocrop      bool
	Generation    uint64 // Settings or successful automatic recovery generation; unchanged by idle opens.
	Running       bool
	Format        v4l2.Format
	Err           error
}

// Manager owns at most one stream worker. Its short state lock never surrounds
// source calls or JPEG processing. Close joins the worker rather than abandoning
// an executing kernel call when a caller's application deadline expires.
type Manager struct {
	mu            sync.Mutex
	options       Options
	generation    uint64
	format        v4l2.Format
	lastErr       error
	cleanupErr    error
	recoveryDelay time.Duration
	run           *stream
	subscribers   map[*Subscription]struct{}
	pending       map[*snapshotWaiter]struct{}
	idle          *time.Timer
	changed       chan struct{}
	openResultGen uint64
	openErr       error
	closed        bool
	closeOnce     sync.Once
	closeErr      error
}

type stream struct {
	generation uint64
	settings   Options
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	opened     bool // manager.mu
	failed     bool // manager.mu; this run failed, rather than a previous attempt.
}

type snapshotWaiter struct {
	generation uint64
	arrival    int64
	result     chan snapshotResult
}

type snapshotResult struct {
	snapshot Snapshot
	err      error
}

// New validates settings without discovering or opening hardware. An empty
// selected Device permits later selection while keyboard input remains usable.
func New(options Options) (*Manager, error) {
	if options.Width == 0 || options.Height == 0 || options.ScreenWidth < 0 || options.ScreenHeight < 0 || options.IdleTimeout < 0 || numericPath(options.Device) {
		return nil, ErrOptions
	}
	var recoveryDelay time.Duration
	if options.OpenSource == nil {
		options.OpenSource = openSource
		if recoverDefaultSource {
			recoveryDelay = time.Second
		}
	}
	if options.MonotonicNow == nil {
		options.MonotonicNow = monotime.Now
	}
	if options.IdleTimeout == 0 {
		options.IdleTimeout = 10 * time.Second
	}
	return &Manager{options: options, recoveryDelay: recoveryDelay, generation: 1, subscribers: make(map[*Subscription]struct{}), pending: make(map[*snapshotWaiter]struct{}), changed: make(chan struct{})}, nil
}

func numericPath(path string) bool {
	if len(path) > 0 && (path[0] == '+' || path[0] == '-') {
		path = path[1:]
	}
	return path != "" && strings.Trim(path, "0123456789") == ""
}

// Snapshot returns the first successfully decoded frame whose monotonic
// timestamp is after this request's arrival. On macOS this is an estimated pipe
// read-start time; FFmpeg/OS buffering prevents a device freshness guarantee. Its five-second acquisition budget
// includes cold startup. Processing remains owned even if this waiter times out.
func (manager *Manager) Snapshot(ctx context.Context) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	arrival, err := manager.options.MonotonicNow()
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: read monotonic clock: %w", ErrUnavailable, err)
	}
	manager.mu.Lock()
	if manager.closed || manager.options.Device == "" {
		err := managerUnavailable(manager.closed)
		manager.mu.Unlock()
		return Snapshot{}, err
	}
	if manager.run != nil && manager.run.failed && manager.recoveryDelay == 0 {
		err := manager.lastErr
		manager.mu.Unlock()
		return Snapshot{}, err
	}
	waiter := &snapshotWaiter{generation: manager.generation, arrival: arrival, result: make(chan snapshotResult, 1)}
	manager.pending[waiter] = struct{}{}
	manager.startLocked()
	manager.mu.Unlock()
	defer func() {
		manager.mu.Lock()
		delete(manager.pending, waiter)
		manager.idleLocked()
		manager.mu.Unlock()
	}()
	var result snapshotResult
	select {
	case result = <-waiter.result:
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if manager.generation != waiter.generation {
		return Snapshot{}, ErrCaptureChanged
	}
	return result.snapshot, result.err
}

func managerUnavailable(closed bool) error {
	if closed {
		return ErrClosed
	}
	return ErrUnavailable
}

// Subscription has one latest-frame slot. Close is idempotent. A source failure
// or settings change preserves the subscription so a later repair can resume it.
type Subscription struct {
	manager *Manager
	frames  chan Event
}

// Event is one latest-frame slot result. Failure events preserve a subscription
// so a later settings repair can resume it. A closed channel means ErrClosed.
type Event struct {
	Frame *Frame
	Err   error
}

// Events exposes the existing one-slot delivery channel for a combined media
// select loop. Use either Events or Next as the subscription's sole consumer.
func (subscription *Subscription) Events() <-chan Event { return subscription.frames }

func (manager *Manager) Subscribe() (*Subscription, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil, ErrClosed
	}
	subscription := &Subscription{manager: manager, frames: make(chan Event, 1)}
	manager.subscribers[subscription] = struct{}{}
	if manager.options.Device == "" {
		subscription.frames <- Event{Err: ErrUnavailable}
	} else if manager.run != nil && manager.run.failed {
		subscription.frames <- Event{Err: manager.lastErr}
	}
	manager.startLocked()
	return subscription, nil
}

func (subscription *Subscription) Next(ctx context.Context) (*Frame, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case result, ok := <-subscription.frames:
		if !ok {
			return nil, ErrClosed
		}
		return result.Frame, result.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (subscription *Subscription) Close() error {
	manager := subscription.manager
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, exists := manager.subscribers[subscription]; exists {
		delete(manager.subscribers, subscription)
		closeSubscription(subscription)
		manager.idleLocked()
	}
	return nil
}

func closeSubscription(subscription *Subscription) {
	select {
	case <-subscription.frames:
	default:
	}
	close(subscription.frames)
}

// SetDevice accepts a path even when no device is open. Accepted changes always
// begin a new generation. With subscribers it waits for that generation's Open.
func (manager *Manager) SetDevice(ctx context.Context, path string) error {
	if path == "" || numericPath(path) {
		return ErrOptions
	}
	return manager.change(ctx, func(options *Options) { options.Device = path }, false)
}

func (manager *Manager) SetResolution(ctx context.Context, width, height uint32) error {
	if width == 0 || height == 0 {
		return ErrOptions
	}
	return manager.change(ctx, func(options *Options) { options.Width, options.Height = width, height }, true)
}

func (manager *Manager) change(ctx context.Context, update func(*Options), needsDevice bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	manager.mu.Lock()
	if manager.closed || needsDevice && manager.options.Device == "" {
		err := managerUnavailable(manager.closed)
		manager.mu.Unlock()
		return err
	}
	update(&manager.options)
	manager.generation++
	generation := manager.generation
	manager.format, manager.lastErr = v4l2.Format{}, nil
	manager.failPendingLocked(ErrCaptureChanged)
	for subscription := range manager.subscribers {
		select {
		case <-subscription.frames:
		default:
		}
	}
	if manager.run != nil {
		manager.run.cancel()
	}
	manager.notifyLocked()
	manager.startLocked()
	wait := len(manager.subscribers) != 0
	manager.mu.Unlock()
	if !wait {
		return nil
	}
	return manager.waitOpen(ctx, generation)
}

func (manager *Manager) waitOpen(ctx context.Context, generation uint64) error {
	for {
		manager.mu.Lock()
		switch {
		case manager.closed:
			manager.mu.Unlock()
			return ErrClosed
		case manager.generation != generation:
			manager.mu.Unlock()
			return ErrCaptureChanged
		case manager.openResultGen == generation:
			err := manager.openErr
			manager.mu.Unlock()
			return err
		}
		changed := manager.changed
		manager.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (manager *Manager) Info() Info {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.infoLocked()
}

// Watch returns a coherent settings/status snapshot and its change notification.
// Read a fresh snapshot after the returned channel closes; never poll Info.
func (manager *Manager) Watch() (Info, <-chan struct{}) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.infoLocked(), manager.changed
}

func (manager *Manager) infoLocked() Info {
	err := manager.lastErr
	if manager.closed || manager.options.Device == "" {
		err = managerUnavailable(manager.closed)
	}
	return Info{
		Device: manager.options.Device, Width: manager.options.Width, Height: manager.options.Height,
		Autocrop: manager.options.Autocrop, Generation: manager.generation,
		Running: manager.run != nil && manager.run.opened && manager.run.ctx.Err() == nil,
		Format:  manager.format, Err: err,
	}
}

func (manager *Manager) notifyLocked() {
	close(manager.changed)
	manager.changed = make(chan struct{})
}

func (manager *Manager) interestsLocked() bool {
	return len(manager.subscribers) != 0 || len(manager.pending) != 0
}

func (manager *Manager) startLocked() {
	if manager.idle != nil {
		manager.idle.Stop()
		manager.idle = nil
	}
	if manager.closed || manager.run != nil || manager.options.Device == "" || !manager.interestsLocked() {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &stream{generation: manager.generation, settings: manager.options, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	manager.run = run
	go manager.produce(run)
}

func (manager *Manager) idleLocked() {
	if manager.closed || manager.run == nil || manager.interestsLocked() {
		return
	}
	if manager.run.failed && manager.recoveryDelay > 0 {
		manager.run.cancel()
		return
	}
	if manager.idle != nil {
		return
	}
	run := manager.run
	var timer *time.Timer
	timer = time.AfterFunc(manager.options.IdleTimeout, func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		if manager.idle == timer {
			manager.idle = nil
			if manager.run == run && !manager.interestsLocked() {
				run.cancel()
			}
		}
	})
	manager.idle = timer
}

func (manager *Manager) failPendingLocked(err error) {
	for waiter := range manager.pending {
		waiter.result <- snapshotResult{err: err}
		delete(manager.pending, waiter)
	}
}

func (manager *Manager) produce(run *stream) {
	recovered := false
attempts:
	for {
		source, err := run.settings.OpenSource(run.ctx, run.settings.Device, v4l2.Options{Width: run.settings.Width, Height: run.settings.Height})
		var format v4l2.Format
		if err == nil {
			format = source.Format()
		}
		manager.mu.Lock()
		if manager.generation == run.generation && run.ctx.Err() == nil {
			if err == nil && recovered {
				manager.generation++
				run.generation = manager.generation
				for waiter := range manager.pending {
					waiter.generation = manager.generation
				}
				for subscription := range manager.subscribers {
					select {
					case <-subscription.frames:
					default:
					}
				}
			}
			manager.openResultGen = run.generation
			manager.openErr = unavailableError(err)
			manager.lastErr = manager.openErr
			if err == nil {
				run.opened, run.failed = true, false
				manager.format = format
			}
			manager.notifyLocked()
		}
		manager.mu.Unlock()
		if err == nil {
			err = manager.read(run, source)
		}
		manager.mu.Lock()
		manager.reportFailureLocked(run, err)
		manager.mu.Unlock()
		var closeErr error
		if source != nil {
			closeErr = source.Close()
		}
		manager.mu.Lock()
		manager.cleanupErr = errors.Join(manager.cleanupErr, closeErr)
		if manager.generation == run.generation && !manager.closed && closeErr != nil {
			manager.lastErr = unavailableError(closeErr)
			manager.failPendingLocked(manager.lastErr)
			manager.publishLocked(Event{Err: manager.lastErr})
		}
		retry := manager.recoveryDelay > 0 && err != nil && run.ctx.Err() == nil &&
			manager.generation == run.generation && !manager.closed && manager.interestsLocked()
		manager.mu.Unlock()
		if !retry {
			break
		}
		recovered = true
		timer := time.NewTimer(manager.recoveryDelay)
		select {
		case <-run.ctx.Done():
			timer.Stop()
			break attempts
		case <-timer.C:
		}
	}
	restart := run.ctx.Err() != nil
	run.cancel()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.run = nil
	manager.notifyLocked()
	close(run.done)
	// Demand may have arrived while a failed native source was retiring.
	if manager.generation != run.generation || restart || manager.recoveryDelay > 0 {
		manager.startLocked()
	}
}

func (manager *Manager) reportFailureLocked(run *stream, err error) {
	if err != nil && run.ctx.Err() == nil && manager.generation == run.generation && !manager.closed {
		run.opened = false
		run.failed = true
		manager.lastErr = unavailableError(err)
		manager.failPendingLocked(manager.lastErr)
		manager.publishLocked(Event{Err: manager.lastErr})
		manager.notifyLocked()
	}
}

func (manager *Manager) publishLocked(result Event) {
	for subscription := range manager.subscribers {
		select {
		case <-subscription.frames:
		default:
		}
		subscription.frames <- result
	}
}

func unavailableError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

func (manager *Manager) read(run *stream, source Source) error {
	processor := newProcessor(run.settings.Autocrop, run.settings.ScreenWidth, run.settings.ScreenHeight)
	format := source.Format()
	for {
		frame, err := source.ReadFrame(run.ctx)
		if err != nil {
			return err
		}
		timestamp, err := frame.Timestamp.Nanoseconds()
		if err != nil {
			return err
		}
		manager.mu.Lock()
		if run.ctx.Err() != nil || manager.generation != run.generation {
			manager.mu.Unlock()
			return context.Canceled
		}
		monotonic := frame.Flags&v4l2.TimestampMask == v4l2.TimestampMonotonic
		if !monotonic {
			manager.failPendingLocked(fmt.Errorf("%w: source timestamp is not CLOCK_MONOTONIC", ErrUnavailable))
		}
		needSnapshot := false
		for waiter := range manager.pending {
			needSnapshot = needSnapshot || timestamp > waiter.arrival
		}
		manager.mu.Unlock()
		processed, err := processor.process(frame.Data, Metadata{Width: int(format.Width), Height: int(format.Height), Generation: run.generation, TimestampNS: timestamp, TimestampFlags: frame.Flags, Sequence: frame.Sequence}, needSnapshot)
		if err != nil {
			continue // Corrupt warmup/candidates do not terminate capture.
		}
		var snapshot Snapshot
		if needSnapshot {
			snapshot, err = processed.Snapshot()
			if err != nil {
				continue
			}
		}
		manager.mu.Lock()
		if run.ctx.Err() != nil || manager.generation != run.generation {
			manager.mu.Unlock()
			return context.Canceled
		}
		manager.publishLocked(Event{Frame: processed})
		if needSnapshot {
			for waiter := range manager.pending {
				if timestamp > waiter.arrival {
					waiter.result <- snapshotResult{snapshot: snapshot}
					delete(manager.pending, waiter)
				}
			}
		}
		manager.idleLocked()
		manager.mu.Unlock()
	}
}

// Close permanently stops admission and joins the tracked source owner. Kernel
// calls may outlast application deadlines; ownership is never abandoned.
func (manager *Manager) Close() error {
	manager.closeOnce.Do(func() {
		manager.mu.Lock()
		manager.closed = true
		if manager.idle != nil {
			manager.idle.Stop()
			manager.idle = nil
		}
		manager.failPendingLocked(ErrClosed)
		for subscription := range manager.subscribers {
			delete(manager.subscribers, subscription)
			closeSubscription(subscription)
		}
		run := manager.run
		if run != nil {
			run.cancel()
		}
		manager.notifyLocked()
		manager.mu.Unlock()
		if run != nil {
			<-run.done
		}
		manager.mu.Lock()
		manager.closeErr = manager.cleanupErr
		manager.mu.Unlock()
	})
	return manager.closeErr
}
