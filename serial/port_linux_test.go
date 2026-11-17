//go:build linux && (amd64 || arm64)

package serial

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

type fakeDevice struct {
	mu        sync.Mutex
	frames    [][]byte
	deadlines []time.Time
	closes    int
	write     func([]byte) (int, error)
	queue     func(context.Context) (int, error)
	deadline  func(time.Time) error
	close     func() error
}

func (d *fakeDevice) Write(b []byte) (int, error) {
	d.mu.Lock()
	d.frames = append(d.frames, bytes.Clone(b))
	d.mu.Unlock()
	if d.write != nil {
		return d.write(b)
	}
	return len(b), nil
}

func (d *fakeDevice) queued(ctx context.Context) (int, error) {
	if d.queue != nil {
		return d.queue(ctx)
	}
	return 0, nil
}

func (d *fakeDevice) SetWriteDeadline(deadline time.Time) error {
	d.mu.Lock()
	d.deadlines = append(d.deadlines, deadline)
	d.mu.Unlock()
	if d.deadline != nil {
		return d.deadline(deadline)
	}
	return nil
}

func (d *fakeDevice) Close() error {
	d.mu.Lock()
	d.closes++
	d.mu.Unlock()
	if d.close != nil {
		return d.close()
	}
	return nil
}

func (d *fakeDevice) snapshot() ([][]byte, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.frames), d.closes
}

func fakePort(t *testing.T, opts Options, devices ...*fakeDevice) (*Port, *[]string) {
	t.Helper()
	if opts.Path == "" && opts.SysRoot == "" {
		opts.Path = "/fake/serial"
	}
	if opts.Baud == 0 {
		opts.Baud = 115200
	}
	p, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	paths := new([]string)
	p.open = func(_ context.Context, path string, _ uint32) (device, error) {
		*paths = append(*paths, path)
		if len(devices) == 0 {
			return nil, syscall.ENODEV
		}
		d := devices[0]
		devices = devices[1:]
		return d, nil
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return p, paths
}

func preparedPort(t *testing.T, d *fakeDevice) *Port {
	t.Helper()
	p, _ := fakePort(t, Options{}, d)
	if generation, err := p.Prepare(context.Background()); err != nil || generation != 1 {
		t.Fatalf("Prepare = %d, %v", generation, err)
	}
	return p
}

func requireFrames(t *testing.T, d *fakeDevice, want ...[]byte) {
	t.Helper()
	got, _ := d.snapshot()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frames = % x; want % x", got, want)
	}
}

func TestOptions(t *testing.T) {
	p, err := New(Options{})
	if err != nil || p.opts.Baud != 9600 || p.opts.SysRoot != "/sys" {
		t.Fatalf("defaults = %+v, %v", p, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	for _, baud := range []int{1200, 1800, 2400, 4800, 9600, 19200, 38400, 57600, 115200} {
		p, err := New(Options{Baud: baud})
		if err != nil {
			t.Fatalf("baud %d: %v", baud, err)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, baud := range []int{-1, 1, 7200, 230400} {
		if _, err := New(Options{Baud: baud}); !errors.Is(err, ErrBaud) {
			t.Fatalf("baud %d: %v", baud, err)
		}
	}
}

func TestPrepareAndWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		d := &fakeDevice{}
		p, paths := fakePort(t, Options{Logger: logger}, d)
		if err := p.WriteFrame(context.Background(), []byte{1}); !errors.Is(err, ErrNotPrepared) || len(*paths) != 0 {
			t.Fatalf("unprepared write = %v, opens %v", err, *paths)
		}
		for range 2 {
			if generation, err := p.Prepare(context.Background()); generation != 1 || err != nil {
				t.Fatalf("Prepare = %d, %v", generation, err)
			}
		}
		frame := ch9329.Keyboard(2, 4)
		if err := p.WriteFrame(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
		reset := ch9329.ReleaseAll()
		requireFrames(t, d, reset[0], reset[1], frame)
		if len(*paths) != 1 || !strings.Contains(logs.String(), "hex=57ab") || strings.Count(logs.String(), "serial frame") != 3 {
			t.Fatalf("opens %v, log %s", *paths, logs.String())
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		_, closes := d.snapshot()
		if closes != 1 {
			t.Fatalf("closed %d times", closes)
		}
		if _, err := p.Prepare(context.Background()); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("Prepare after Close: %v", err)
		}
		if err := p.WriteFrame(context.Background(), frame); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("WriteFrame after Close: %v", err)
		}
	})
}

func TestPrepareFailureDoesNotPublishGeneration(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				bad := &fakeDevice{}
				calls := 0
				bad.write = func(b []byte) (int, error) {
					calls++
					if calls == failAt {
						return 0, syscall.EIO
					}
					return len(b), nil
				}
				good := &fakeDevice{}
				p, paths := fakePort(t, Options{}, bad, good)
				if generation, err := p.Prepare(context.Background()); generation != 0 || !errors.Is(err, syscall.EIO) {
					t.Fatalf("failed reset = %d, %v", generation, err)
				}
				if err := p.WriteFrame(context.Background(), []byte{1}); !errors.Is(err, ErrNotPrepared) || len(*paths) != 1 {
					t.Fatalf("write after failed reset = %v, opens %v", err, *paths)
				}
				if generation, err := p.Prepare(context.Background()); generation != 1 || err != nil {
					t.Fatalf("retry = %d, %v", generation, err)
				}
				reset := ch9329.ReleaseAll()
				requireFrames(t, good, reset[0], reset[1])
				_, closes := bad.snapshot()
				if closes != 1 {
					t.Fatalf("failed reset closed %d times", closes)
				}
			})
		})
	}
}

func TestRecoveryDoesNotReplayFailedFrame(t *testing.T) {
	for name, fault := range map[string]func(*fakeDevice){
		"write EIO":     func(d *fakeDevice) { d.write = func([]byte) (int, error) { return 0, syscall.EIO } },
		"unplug":        func(d *fakeDevice) { d.write = func([]byte) (int, error) { return 0, syscall.ENODEV } },
		"short write":   func(d *fakeDevice) { d.write = func(b []byte) (int, error) { return len(b) - 1, nil } },
		"partial error": func(d *fakeDevice) { d.write = func([]byte) (int, error) { return 1, syscall.EIO } },
		"queue":         func(d *fakeDevice) { d.queue = func(context.Context) (int, error) { return 0, syscall.EIO } },
		"deadline":      func(d *fakeDevice) { d.deadline = func(time.Time) error { return syscall.EIO } },
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				bad, good := &fakeDevice{}, &fakeDevice{}
				p, paths := fakePort(t, Options{}, bad, good)
				if _, err := p.Prepare(context.Background()); err != nil {
					t.Fatal(err)
				}
				fault(bad)
				failed := ch9329.Keyboard(0, 4)
				if err := p.WriteFrame(context.Background(), failed); err == nil {
					t.Fatal("fault succeeded")
				}
				_, closes := bad.snapshot()
				if closes != 1 {
					t.Fatalf("invalidated device closed %d times", closes)
				}
				if err := p.WriteFrame(context.Background(), failed); !errors.Is(err, ErrNotPrepared) || len(*paths) != 1 {
					t.Fatalf("write reopened mid-action: %v, opens %v", err, *paths)
				}
				if generation, err := p.Prepare(context.Background()); generation != 2 || err != nil {
					t.Fatalf("recovery = %d, %v", generation, err)
				}
				reset := ch9329.ReleaseAll()
				requireFrames(t, good, reset[0], reset[1])
				next := ch9329.MouseAbsolute(0, 100, 200, 0)
				if err := p.WriteFrame(context.Background(), next); err != nil {
					t.Fatal(err)
				}
				requireFrames(t, good, reset[0], reset[1], next)
			})
		})
	}
}

func TestPrepareAutoDetectsMovedPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSysfsFixture(t)
		usb := f.usb("devices/usb1/1-1", "1a86", "7523")
		f.tty("ttyUSB0", usb)
		bad, good := &fakeDevice{}, &fakeDevice{}
		p, paths := fakePort(t, Options{SysRoot: f.root}, bad, good)
		if _, err := p.Prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		bad.write = func([]byte) (int, error) { return 0, syscall.ENODEV }
		if err := p.WriteFrame(context.Background(), []byte{1}); !errors.Is(err, syscall.ENODEV) {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(f.root, "class/tty/ttyUSB0")); err != nil {
			t.Fatal(err)
		}
		f.tty("ttyUSB7", usb)
		if generation, err := p.Prepare(context.Background()); generation != 2 || err != nil {
			t.Fatalf("recovery = %d, %v", generation, err)
		}
		if !slices.Equal(*paths, []string{"/dev/ttyUSB0", "/dev/ttyUSB7"}) {
			t.Fatalf("open paths = %v", *paths)
		}
	})
}

func TestPrepareSelectionAndOpenErrors(t *testing.T) {
	f := newSysfsFixture(t)
	p, paths := fakePort(t, Options{SysRoot: f.root})
	if _, err := p.Prepare(context.Background()); !errors.Is(err, ErrNotFound) || len(*paths) != 0 {
		t.Fatalf("no device = %v, opens %v", err, *paths)
	}
	q, paths := fakePort(t, Options{})
	if _, err := q.Prepare(context.Background()); !errors.Is(err, syscall.ENODEV) || len(*paths) != 1 {
		t.Fatalf("open failure = %v, opens %v", err, *paths)
	}
}

func TestCancellationWhileQueuedDoesNotInvalidate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &fakeDevice{}
		p := preparedPort(t, d)
		<-p.gate
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- p.WriteFrame(ctx, []byte{1}) }()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter: %v", err)
		}
		p.release()
		if generation, err := p.Prepare(context.Background()); generation != 1 || err != nil {
			t.Fatalf("waiter changed preparation: %d, %v", generation, err)
		}
		reset := ch9329.ReleaseAll()
		requireFrames(t, d, reset[0], reset[1])
	})
}

func TestApplicationBudgets(t *testing.T) {
	for _, phase := range []string{"admission", "queue", "guard"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := &fakeDevice{}
				p := preparedPort(t, d)
				frame := []byte{1}
				switch phase {
				case "admission":
					<-p.gate
				case "queue":
					d.queue = func(context.Context) (int, error) { return 1, nil }
				case "guard":
					frame = make([]byte, p.opts.Baud)
				}
				start := time.Now()
				if err := p.WriteFrame(context.Background(), frame); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("budget error: %v", err)
				}
				if elapsed := time.Since(start); elapsed != ioBudget {
					t.Fatalf("elapsed = %v; want %v", elapsed, ioBudget)
				}
				if phase == "admission" {
					p.release()
				} else {
					if err := p.WriteFrame(context.Background(), frame); !errors.Is(err, ErrNotPrepared) {
						t.Fatalf("failed write left ready: %v", err)
					}
				}
			})
		})
	}
}

func TestCancelAndCloseAtEachIOPhase(t *testing.T) {
	for _, phase := range []string{"write", "queue", "guard"} {
		for _, closing := range []bool{false, true} {
			name := phase + "/cancel"
			if closing {
				name = phase + "/close"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					d := &fakeDevice{}
					p := preparedPort(t, d)
					started := make(chan struct{})
					var startedOnce sync.Once
					markStarted := func() { startedOnce.Do(func() { close(started) }) }
					switch phase {
					case "write":
						wake := make(chan struct{})
						var wakeOnce sync.Once
						d.deadline = func(deadline time.Time) error {
							if deadline.Before(time.Now()) {
								wakeOnce.Do(func() { close(wake) })
							}
							return nil
						}
						d.write = func([]byte) (int, error) {
							markStarted()
							<-wake
							return 0, os.ErrDeadlineExceeded
						}
					case "queue":
						d.queue = func(context.Context) (int, error) { markStarted(); return 1, nil }
					case "guard":
						d.queue = func(context.Context) (int, error) { markStarted(); return 0, nil }
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					result := make(chan error, 1)
					go func() { result <- p.WriteFrame(ctx, ch9329.Keyboard(0, 4)) }()
					<-started
					synctest.Wait()
					want := error(context.Canceled)
					if closing {
						want = os.ErrClosed
						if err := p.Close(); err != nil {
							t.Fatal(err)
						}
					} else {
						cancel()
					}
					if err := <-result; !errors.Is(err, want) {
						t.Fatalf("write error = %v; want %v", err, want)
					}
					_, closes := d.snapshot()
					if closes != 1 {
						t.Fatalf("closed %d times", closes)
					}
				})
			})
		}
	}
}

func TestCloseWaitsForOwnedKernelOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &fakeDevice{}
		p := preparedPort(t, d)
		entered, finish := make(chan struct{}), make(chan struct{})
		d.queue = func(context.Context) (int, error) {
			close(entered)
			<-finish // model a synchronous kernel operation returning later
			return 0, nil
		}
		writeDone := make(chan error, 1)
		go func() { writeDone <- p.WriteFrame(context.Background(), []byte{1}) }()
		<-entered
		closeDone := make(chan error, 1)
		go func() { closeDone <- p.Close() }()
		synctest.Wait()
		_, closes := d.snapshot()
		if closes != 0 {
			t.Fatal("device closed before its owned operation returned")
		}
		if _, err := p.Prepare(context.Background()); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("closing transport admitted preparation: %v", err)
		}
		select {
		case <-closeDone:
			t.Fatal("Close returned before kernel operation")
		default:
		}
		close(finish)
		if err := <-writeDone; !errors.Is(err, os.ErrClosed) {
			t.Fatal(err)
		}
		if err := <-closeDone; err != nil {
			t.Fatal(err)
		}
		_, closes = d.snapshot()
		if closes != 1 {
			t.Fatalf("closed %d times", closes)
		}
	})
}

func TestCloseWaitsForKernelClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &fakeDevice{}
		p := preparedPort(t, d)
		entered, finish := make(chan struct{}), make(chan struct{})
		d.close = func() error { close(entered); <-finish; return nil }
		closed := make(chan error, 1)
		go func() { closed <- p.Close() }()
		<-entered
		select {
		case <-closed:
			t.Fatal("Close concealed an executing kernel close")
		default:
		}
		if err := p.WriteFrame(context.Background(), []byte{1}); !errors.Is(err, os.ErrClosed) {
			t.Fatal(err)
		}
		close(finish)
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
	})
}

func TestPreserveOriginalAndCleanupErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &fakeDevice{}
		p := preparedPort(t, d)
		d.write = func([]byte) (int, error) { return 0, syscall.EIO }
		d.close = func() error { return io.ErrUnexpectedEOF }
		err := p.WriteFrame(context.Background(), []byte{1})
		if !errors.Is(err, syscall.EIO) || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("lost original or cleanup error: %v", err)
		}
	})
}

func TestCancelledPrepareAndSlowOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, paths := fakePort(t, Options{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := p.Prepare(ctx); !errors.Is(err, context.Canceled) || len(*paths) != 0 {
			t.Fatalf("cancelled preparation = %v, opens %v", err, *paths)
		}
		d := &fakeDevice{}
		entered, finish := make(chan struct{}), make(chan struct{})
		p.open = func(context.Context, string, uint32) (device, error) {
			close(entered)
			<-finish
			return d, nil
		}
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() { _, err := p.Prepare(ctx); result <- err }()
		<-entered
		cancel()
		synctest.Wait()
		select {
		case <-result:
			t.Fatal("Prepare abandoned its executing open")
		default:
		}
		close(finish)
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		frames, closes := d.snapshot()
		if len(frames) != 0 || closes != 1 {
			t.Fatalf("cancelled open leaked or reset: % x, closes %d", frames, closes)
		}
	})
}

func TestCancelledPressCanPrepareReleaseOnlyCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pressed, cleanup := &fakeDevice{}, &fakeDevice{}
		p, paths := fakePort(t, Options{}, pressed, cleanup)
		if generation, err := p.Prepare(context.Background()); generation != 1 || err != nil {
			t.Fatalf("initial Prepare = %d, %v", generation, err)
		}

		// Cancel after the complete press report was written and its host queue
		// emptied, while the baud guard is still pending. Input may be held even
		// though WriteFrame reports cancellation and invalidates this device.
		queued := make(chan struct{})
		pressed.queue = func(context.Context) (int, error) { close(queued); return 0, nil }
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		press := ch9329.Keyboard(0, 4)
		writeDone := make(chan error, 1)
		go func() { writeDone <- p.WriteFrame(ctx, press) }()
		<-queued
		synctest.Wait()
		cancel()
		if err := <-writeDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled press: %v", err)
		}
		reset := ch9329.ReleaseAll()
		requireFrames(t, pressed, reset[0], reset[1], press)
		if _, closes := pressed.snapshot(); closes != 1 {
			t.Fatalf("cancelled device closed %d times", closes)
		}

		// The caller retains its HID action lock and starts bounded cleanup
		// before permanent Close. A bare release write cannot recover the port.
		cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer stop()
		if err := p.WriteFrame(cleanupCtx, reset[0]); !errors.Is(err, ErrNotPrepared) {
			t.Fatalf("bare release unexpectedly recovered the port: %v", err)
		}
		resetStarted, finishReset := make(chan struct{}), make(chan struct{})
		queueCalls := 0
		cleanup.queue = func(context.Context) (int, error) {
			queueCalls++
			if queueCalls == 1 {
				close(resetStarted)
				<-finishReset
			}
			return 0, nil
		}
		type preparation struct {
			generation uint64
			err        error
		}
		prepared := make(chan preparation, 1)
		go func() {
			generation, err := p.Prepare(cleanupCtx)
			prepared <- preparation{generation, err}
		}()
		<-resetStarted
		synctest.Wait()
		if p.lifetime.Err() != nil {
			t.Fatal("port was permanently closed before release cleanup finished")
		}
		if _, closes := cleanup.snapshot(); closes != 0 {
			t.Fatal("cleanup device closed before its reset finished")
		}
		close(finishReset)
		result := <-prepared
		if result.generation != 2 || result.err != nil {
			t.Fatalf("release-only Prepare = %d, %v", result.generation, result.err)
		}
		// A new generation tells HID to clear held state. Recovery emitted only
		// the two release reports, with no original press or later normal action.
		requireFrames(t, cleanup, reset[0], reset[1])
		if len(*paths) != 2 {
			t.Fatalf("open paths = %v", *paths)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		if _, closes := cleanup.snapshot(); closes != 1 {
			t.Fatalf("cleanup device closed %d times", closes)
		}
		if _, err := p.Prepare(cleanupCtx); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("Prepare after permanent Close: %v", err)
		}
	})
}

func TestGuardDuration(t *testing.T) {
	if got, want := guard(13, 9600), 14583334*time.Nanosecond; got != want {
		t.Fatalf("guard = %v; want %v", got, want)
	}
	if got := guard(int(^uint(0)>>1), 9600); got != ioBudget {
		t.Fatalf("overflow guard = %v", got)
	}
}
