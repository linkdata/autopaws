//go:build (linux || darwin || windows) && (amd64 || arm64)

// Package serial provides CH340/CH341 discovery and a raw CH9329 transport.
// Writes wait for the host output queue and a conservative baud-derived delay.
// This is pacing, not proof of UART completion, a CH9329 acknowledgement, or
// target processing. Kernel open, configuration and close calls can outlast a context.
package serial

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

const (
	ioBudget  = time.Second
	queuePoll = time.Millisecond
)

var (
	// ErrBaud indicates a rate outside the supported standard serial rates.
	ErrBaud = errors.New("serial: unsupported baud rate")
	// ErrBusy means another cooperating process has locked the port.
	ErrBusy = errors.New("serial: port busy")
	// ErrNotPrepared means Prepare must succeed before another frame is sent.
	ErrNotPrepared = errors.New("serial: port not prepared")
	// ErrSettings indicates that the driver did not apply the requested modes.
	ErrSettings = errors.New("serial: raw serial settings not applied")
)

// Options configures a Port. An empty Path auto-detects one CH340/CH341 each
// time recovery requires opening a port. Baud defaults to 9600. SysRoot selects
// Linux sysfs (default /sys) and is ignored on macOS/Windows. Logger, when non-nil, receives successful frames at debug level.
type Options struct {
	Path    string
	Baud    int
	SysRoot string
	Logger  *slog.Logger
}

type device interface {
	Write([]byte) (int, error)
	SetWriteDeadline(time.Time) error
	queued(context.Context) (int, error)
	Close() error
}

// Port owns a raw serial port. Construct it with New. Methods are safe for
// concurrent use, but the HID layer must serialize complete input actions.
// Prepare resets hardware before exposing a new recovery generation. WriteFrame
// never opens/reopens, so a precomputed report cannot undo a recovery reset.
//
// After a write failure or cancellation, release-only cleanup must explicitly
// Prepare with a bounded context derived from context.WithoutCancel while the
// caller still holds its HID action lock. A new generation means release-all
// succeeded: clear held software state and never replay the interrupted input.
// Finish that cleanup attempt before permanently closing the Port.
type Port struct {
	opts       Options
	gate       chan struct{}
	lifetime   context.Context
	cancel     context.CancelFunc
	closeOnce  sync.Once
	closeErr   error
	dev        device // gate protects dev and generation
	generation uint64
	open       func(context.Context, string, uint32) (device, error)
}

// New validates options without opening hardware.
func New(opts Options) (*Port, error) {
	if opts.Baud == 0 {
		opts.Baud = 9600
	}
	if _, ok := baudRates[opts.Baud]; !ok {
		return nil, fmt.Errorf("%w: %d", ErrBaud, opts.Baud)
	}
	if opts.SysRoot == "" {
		opts.SysRoot = "/sys"
	}
	lifetime, cancel := context.WithCancel(context.Background())
	p := &Port{
		opts: opts, gate: make(chan struct{}, 1), lifetime: lifetime,
		cancel: cancel, open: openDevice,
	}
	p.gate <- struct{}{}
	return p, nil
}

// Prepare opens/reopens and sends release-all if necessary. Its generation
// changes only after both reset reports succeed. Callers must clear held HID
// state when it changes, before constructing the next action's reports.
// Cancellation is checked around kernel calls, which cannot be forcibly stopped.
func (p *Port) Prepare(ctx context.Context) (uint64, error) {
	ctx, cancel := p.operationContext(ctx)
	defer cancel()
	if err := p.acquire(ctx); err != nil {
		return 0, err
	}
	defer p.release()
	if p.dev != nil {
		return p.generation, nil
	}
	path := p.opts.Path
	if path == "" {
		var err error
		if path, err = detect(ctx, p.opts.SysRoot); err != nil {
			return 0, err
		}
	}
	if err := p.check(ctx); err != nil {
		return 0, err
	}
	d, err := p.open(ctx, path, baudRates[p.opts.Baud])
	if err != nil {
		return 0, fmt.Errorf("serial prepare %s: %w", path, errors.Join(p.check(ctx), err))
	}
	p.dev = d
	for _, frame := range ch9329.ReleaseAll() {
		if err := p.send(ctx, frame); err != nil {
			return 0, fmt.Errorf("serial reset %s: %w", path, errors.Join(err, p.invalidate()))
		}
	}
	if err := p.check(ctx); err != nil {
		return 0, errors.Join(err, p.invalidate())
	}
	p.generation++
	return p.generation, nil
}

// WriteFrame sends one frame on a prepared port. Admission, nonblocking writes,
// host-queue polling and baud pacing share a one-second application budget or
// the earlier caller deadline. Synchronous kernel calls and subsequent cleanup
// can exceed that budget. On failure the current port is closed and unavailable
// until a later Prepare; the failed frame is never replayed automatically.
func (p *Port) WriteFrame(ctx context.Context, frame []byte) error {
	ctx, cancel := p.operationContext(ctx)
	defer cancel()
	ctx, stop := context.WithTimeout(ctx, ioBudget)
	defer stop()
	if err := p.acquire(ctx); err != nil {
		return err
	}
	defer p.release()
	if p.dev == nil {
		return ErrNotPrepared
	}
	if err := p.send(ctx, frame); err != nil {
		return fmt.Errorf("serial write: %w", errors.Join(err, p.invalidate()))
	}
	return nil
}

// Close permanently rejects new operations and cancels application waits before
// closing the owned device. It waits for any executing kernel operation/close to
// finish normally; the OS does not provide a context deadline for those calls.
// The caller must finish any uncancelled release-only preparation before Close,
// because a permanently closed Port cannot reopen to release held input.
func (p *Port) Close() error {
	p.closeOnce.Do(func() {
		p.cancel()
		<-p.gate
		defer p.release()
		p.closeErr = p.invalidate()
	})
	return p.closeErr
}

func (p *Port) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(p.lifetime, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (p *Port) acquire(ctx context.Context) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return p.check(ctx)
	case <-p.lifetime.Done():
		return os.ErrClosed
	case <-p.gate:
	}
	if err := p.check(ctx); err != nil {
		p.release()
		return err
	}
	return nil
}

func (p *Port) release() { p.gate <- struct{}{} }

func (p *Port) check(ctx context.Context) error {
	if p.lifetime.Err() != nil {
		return os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (p *Port) invalidate() error {
	d := p.dev
	p.dev = nil
	if d != nil {
		return d.Close()
	}
	return nil
}

func (p *Port) send(parent context.Context, frame []byte) error {
	ctx, cancel := context.WithTimeout(parent, ioBudget)
	defer cancel()
	if err := p.check(ctx); err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	if err := p.dev.SetWriteDeadline(deadline); err != nil {
		return errors.Join(p.check(ctx), err)
	}
	// Join the deadline callback before releasing ownership of this device.
	// It must not set a deadline on a later operation or a reused descriptor.
	d := p.dev
	woke := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = d.SetWriteDeadline(time.Unix(1, 0))
		close(woke)
	})
	defer func() {
		if !stop() {
			<-woke
		}
	}()
	if err := p.check(ctx); err != nil {
		return err
	}
	n, err := d.Write(frame)
	if n != len(frame) && err == nil {
		err = io.ErrShortWrite
	}
	if err = errors.Join(p.check(ctx), err); err != nil {
		return err
	}
	for {
		queued, err := d.queued(ctx)
		if err = errors.Join(p.check(ctx), err); err != nil {
			return err
		}
		if queued == 0 {
			break
		}
		if err := wait(ctx, queuePoll); err != nil {
			return p.check(ctx)
		}
	}
	if err := wait(ctx, guard(len(frame), p.opts.Baud)); err != nil {
		return p.check(ctx)
	}
	if err := p.check(ctx); err != nil {
		return err
	}
	if p.opts.Logger != nil {
		p.opts.Logger.DebugContext(ctx, "serial frame", "hex", hex.EncodeToString(frame))
	}
	return nil
}

func guard(length, baud int) time.Duration {
	// A longer frame cannot finish within the application budget. Saturation
	// also avoids overflowing duration arithmetic for an arbitrary input slice.
	if length >= baud/10 {
		return ioBudget
	}
	numerator := time.Duration(length+1) * 10 * time.Second
	return (numerator + time.Duration(baud) - 1) / time.Duration(baud)
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
