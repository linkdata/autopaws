//go:build linux && (amd64 || arm64)

package alsa

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var generations atomic.Uint64

// Capture owns one PCM and one fixed blocking-read worker. Call Close even if
// subscriptions report a terminal error. The worker also closes the descriptor
// on terminal failure. Cancelling a subscriber never stops other subscribers.
type Capture struct {
	path string
	fd   int
	ops  kernel

	lifetime context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
	closeErr error

	// PREPARE/STATUS/final close and DROP share this mutex; READI never holds
	// it, so DROP can wake an already blocked read. Only the worker closes fd.
	kernelMu     sync.Mutex
	kernelClosed bool

	mu       sync.Mutex
	config   Config
	stats    Stats
	sequence uint64
	subs     map[*Subscription]struct{}
	closing  bool
	ended    bool
	err      error
}

// Open exclusively opens path with O_NONBLOCK (so busy opens fail), then clears
// that flag for the fixed worker's reads. It prefers stereo, then mono, and
// 48000 Hz when exactly supported. ctx controls startup, not stream lifetime.
// No PCM descriptor is registered with Go's runtime poller.
func Open(ctx context.Context, path string) (*Capture, error) {
	return open(ctx, path, systemKernel())
}

func open(ctx context.Context, path string, ops kernel) (*Capture, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := ops.open(path, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC)
	if err != nil {
		return nil, kernelError("open", path, err)
	}
	if err = ctx.Err(); err == nil {
		err = ops.setNonblock(fd, false)
	}
	var config Config
	if err == nil {
		config, err = negotiate(ctx, ops, fd)
	}
	if err != nil {
		return nil, kernelError("setup", path, errors.Join(err, ops.close(fd)))
	}
	config.Generation = generations.Add(1)
	lifetime, cancel := context.WithCancel(context.Background())
	capture := &Capture{
		path: path, fd: fd, ops: ops, lifetime: lifetime, cancel: cancel,
		done: make(chan struct{}), config: config, subs: make(map[*Subscription]struct{}),
	}
	go capture.run()
	return capture, nil
}

// Config returns the current generation's negotiated configuration.
func (capture *Capture) Config() Config {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.config
}

// Stats returns recovery event counters.
func (capture *Capture) Stats() Stats {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.stats
}

func (capture *Capture) run() {
	err := capture.readLoop()
	capture.mu.Lock()
	capture.err = kernelError("capture", capture.path, err)
	capture.ended = true
	capture.finishSubscriptions(capture.terminalError())
	capture.mu.Unlock()
	// Report a known terminal failure before a possibly retained driver close.
	// done still joins close, and Close retains its error and fd ownership.
	capture.kernelMu.Lock()
	closeErr := capture.ops.close(capture.fd)
	capture.kernelClosed = true
	capture.kernelMu.Unlock()
	capture.mu.Lock()
	capture.err = kernelError("capture", capture.path, errors.Join(err, closeErr))
	capture.finishSubscriptions(capture.terminalError())
	capture.mu.Unlock()
	close(capture.done)
}

func (capture *Capture) readLoop() error {
	config := capture.Config()
	storage := make([]byte, int(config.PeriodFrames)*int(config.Channels)*2)
	recovering := false
	for capture.lifetime.Err() == nil {
		frames, timestamp, err := capture.read(storage, config)
		if capture.lifetime.Err() != nil {
			return nil
		}
		switch {
		case err == nil:
			recovering = false
			capture.publish(storage[:int(frames)*int(config.Channels)*2], frames, timestamp)
		case errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ESTRPIPE):
			// A detached driver can keep accepting PREPARE while reads fail
			// immediately. Require valid PCM before attempting recovery again.
			if recovering {
				return fmt.Errorf("PCM read failed after PREPARE without sample progress: %w", err)
			}
			recovering = true
			if err := capture.recover(err); err != nil {
				return err
			}
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EAGAIN):
			// O_NONBLOCK is cleared, but a driver can still report EAGAIN.
			// Avoid a CPU spin while retaining a cancellable single worker.
			timer := time.NewTimer(time.Millisecond)
			select {
			case <-capture.lifetime.Done():
			case <-timer.C:
			}
			timer.Stop()
		default:
			return err
		}
	}
	return nil
}

func (capture *Capture) read(storage []byte, config Config) (uint32, int64, error) {
	capture.kernelMu.Lock()
	stopped := capture.lifetime.Err() != nil
	capture.kernelMu.Unlock()
	if stopped {
		return 0, 0, ErrClosed
	}
	transfer := transfer{Buffer: unsafe.Pointer(&storage[0]), Frames: uint64(config.PeriodFrames)}
	err := capture.ops.ioctl(capture.fd, ioctlRead, unsafe.Pointer(&transfer))
	runtime.KeepAlive(storage)
	if err != nil {
		return 0, 0, err
	}
	if transfer.Result <= 0 || uint64(transfer.Result) > uint64(config.PeriodFrames) {
		return 0, 0, ErrSamples
	}
	capture.kernelMu.Lock()
	defer capture.kernelMu.Unlock()
	var snapshot status
	if err := call(capture.lifetime, capture.ops, capture.fd, ioctlStatus, unsafe.Pointer(&snapshot)); err != nil {
		return 0, 0, err
	}
	switch snapshot.State {
	case stateRunning:
		// STATUS snapshots state before updating the hardware pointer; that
		// update can itself discover an overrun at our stop threshold.
		if snapshot.Available >= uint64(config.BufferFrames) {
			return 0, 0, syscall.EPIPE
		}
	case stateXRun:
		return 0, 0, syscall.EPIPE
	case stateSuspended:
		return 0, 0, syscall.ESTRPIPE
	case stateDisconnected:
		return 0, 0, syscall.ENODEV
	default:
		return 0, 0, ErrSettings
	}
	frames := uint32(transfer.Result)
	timestamp, err := firstSampleNS(snapshot, frames, config)
	return frames, timestamp, err
}

func (capture *Capture) recover(cause error) error {
	capture.mu.Lock()
	if errors.Is(cause, syscall.EPIPE) {
		capture.stats.Overruns++
	} else {
		capture.stats.Suspends++
	}
	capture.config.Generation = generations.Add(1)
	capture.sequence = 0
	for subscription := range capture.subs {
		subscription.queue, subscription.frames = nil, 0
		subscription.signal()
	}
	capture.mu.Unlock()
	capture.kernelMu.Lock()
	defer capture.kernelMu.Unlock()
	if capture.lifetime.Err() != nil {
		return nil
	}
	return kernelError("recover PREPARE", capture.path, call(capture.lifetime, capture.ops, capture.fd, ioctlPrepare, nil))
}

func (capture *Capture) publish(data []byte, frames uint32, timestamp int64) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.closing || capture.ended {
		return
	}
	block := Block{Sequence: capture.sequence, TimestampNS: timestamp, Frames: frames, Config: capture.config}
	capture.sequence++
	for subscription := range capture.subs {
		maximum := uint64(block.Config.Rate) * uint64(ServerQueueBudget) / uint64(time.Second)
		if uint64(frames) > maximum {
			continue
		}
		for len(subscription.queue) != 0 && subscription.frames+uint64(frames) > maximum {
			subscription.pop()
		}
		block.Data = bytes.Clone(data)
		subscription.queue = append(subscription.queue, block)
		subscription.frames += uint64(frames)
		subscription.signal()
	}
}

func (capture *Capture) terminalError() error {
	if capture.err != nil {
		return capture.err
	}
	return ErrClosed
}

func (capture *Capture) finishSubscriptions(err error) {
	for subscription := range capture.subs {
		subscription.queue, subscription.frames = nil, 0
		subscription.err = err
		subscription.signal()
	}
}

// Close permanently stops subscriptions, issues DROP without waiting behind a
// blocked READI, and joins the worker that closes fd. It shares one result with
// concurrent callers. DROP/read/close can remain inside a Linux driver beyond a
// context deadline; this method retains ownership instead of abandoning them.
func (capture *Capture) Close() error {
	capture.once.Do(func() {
		capture.cancel()
		capture.mu.Lock()
		capture.closing = true
		capture.finishSubscriptions(ErrClosed)
		capture.mu.Unlock()
		capture.kernelMu.Lock()
		var dropErr error
		if !capture.kernelClosed {
			dropErr = call(context.Background(), capture.ops, capture.fd, ioctlDrop, nil)
		}
		capture.kernelMu.Unlock()
		<-capture.done
		capture.mu.Lock()
		capture.closeErr = kernelError("close", capture.path, errors.Join(dropErr, capture.err))
		capture.mu.Unlock()
	})
	return capture.closeErr
}

// Subscription is one duration-bounded server queue. It does not own the PCM.
type Subscription struct {
	capture *Capture
	notify  chan struct{}
	queue   []Block // capture.mu protects every field below
	frames  uint64
	closed  bool
	err     error
}

// Subscribe starts an empty queue; it never replays earlier samples.
func (capture *Capture) Subscribe() *Subscription {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	subscription := &Subscription{capture: capture, notify: make(chan struct{})}
	if capture.closing || capture.ended {
		subscription.err = capture.terminalError()
	} else {
		capture.subs[subscription] = struct{}{}
	}
	return subscription
}

func (subscription *Subscription) signal() {
	close(subscription.notify)
	subscription.notify = make(chan struct{})
}

func (subscription *Subscription) pop() Block {
	block := subscription.queue[0]
	subscription.queue[0] = Block{}
	subscription.queue = subscription.queue[1:]
	subscription.frames -= uint64(block.Frames)
	return block
}

// Ready returns a change notification for an external media select loop. Read
// it BEFORE TryNext, then wait only if no block/error is ready, to avoid missing
// a concurrent publication. The signal carries no PCM and adds no sample queue.
func (subscription *Subscription) Ready() <-chan struct{} {
	subscription.capture.mu.Lock()
	defer subscription.capture.mu.Unlock()
	return subscription.notify
}

// TryNext never waits for publication. It expires blocks100ms after their last
// captured sample, separate from their acquisition duration. The queue itself
// still holds at most100ms of sample frames.
func (subscription *Subscription) TryNext() (Block, bool, error) {
	now, err := subscription.capture.ops.now()
	if err != nil {
		return Block{}, false, fmt.Errorf("alsa queue monotonic clock: %w", err)
	}
	subscription.capture.mu.Lock()
	defer subscription.capture.mu.Unlock()
	for len(subscription.queue) != 0 {
		first := subscription.queue[0]
		duration := int64(uint64(first.Frames) * uint64(time.Second) / uint64(first.Config.Rate))
		if now >= first.TimestampNS && now-first.TimestampNS-duration > int64(ServerQueueBudget) {
			subscription.pop()
			continue
		}
		return subscription.pop(), true, nil
	}
	return Block{}, false, subscription.err
}

// Next discards blocks whose end-of-capture time is older than the server's
// 100 ms share before returning samples. Acquisition duration is separate from
// queue residence. Call immediately before writing; a caller that
// stalls after Next must recheck freshness rather than replay an old block.
func (subscription *Subscription) Next(ctx context.Context) (Block, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Block{}, err
		}
		notify := subscription.Ready()
		block, ok, err := subscription.TryNext()
		if err != nil || ok {
			return block, err
		}
		select {
		case <-ctx.Done():
			return Block{}, ctx.Err()
		case <-notify:
		}
	}
}

// Close drops only this subscriber's queue and wakes its blocked Next callers.
func (subscription *Subscription) Close() {
	subscription.capture.mu.Lock()
	defer subscription.capture.mu.Unlock()
	if !subscription.closed {
		subscription.closed = true
		subscription.err = ErrClosed
		subscription.queue, subscription.frames = nil, 0
		delete(subscription.capture.subs, subscription)
		subscription.signal()
	}
}
