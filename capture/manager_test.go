//go:build (linux || darwin || windows) && (amd64 || arm64)

package capture

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/linkdata/autopaws/avfoundation"
	"github.com/linkdata/autopaws/directshow"
	"github.com/linkdata/autopaws/v4l2"
)

type managerRead struct {
	frame v4l2.Frame
	err   error
}

type managerSource struct {
	format       v4l2.Format
	reads        chan managerRead
	reading      chan struct{}
	closed       chan struct{}
	closeGate    <-chan struct{}
	closeErr     error
	closes       atomic.Int32
	ignoreCancel bool
}

func newManagerSource() *managerSource {
	return &managerSource{format: v4l2.Format{Width: 128, Height: 96, FourCC: v4l2.PixFmtMJPEG}, reads: make(chan managerRead), reading: make(chan struct{}, 100), closed: make(chan struct{})}
}

func (source *managerSource) Format() v4l2.Format { return source.format }

func (source *managerSource) ReadFrame(ctx context.Context) (v4l2.Frame, error) {
	source.reading <- struct{}{}
	if source.ignoreCancel {
		result := <-source.reads
		return result.frame, result.err
	}
	select {
	case result := <-source.reads:
		return result.frame, result.err
	case <-ctx.Done():
		return v4l2.Frame{}, ctx.Err()
	}
}

func (source *managerSource) Close() error {
	if source.closeGate != nil {
		<-source.closeGate
	}
	source.closes.Add(1)
	close(source.closed)
	return source.closeErr
}

func managerJPEG(t testing.TB, letterbox bool) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 128, 96))
	for y := range 96 {
		for x := range 128 {
			if !letterbox || y >= 12 && y < 84 {
				img.SetGray(x, y, color.Gray{Y: 220})
			}
		}
	}
	var data bytes.Buffer
	if err := jpeg.Encode(&data, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func managerFrame(data []byte, micros int64, sequence uint32) v4l2.Frame {
	return v4l2.Frame{Data: bytes.Clone(data), Timestamp: v4l2.Timestamp{Microseconds: micros}, Flags: v4l2.TimestampMonotonic | v4l2.TimestampStartOfExposure, Sequence: sequence}
}

func managerOptions(source *managerSource, opened chan<- struct{}) Options {
	return Options{
		Device: "/dev/fake", Width: 128, Height: 96, Autocrop: true,
		ScreenWidth: 128, ScreenHeight: 72,
		IdleTimeout:  2 * time.Second,
		MonotonicNow: func() (int64, error) { return 1_000, nil },
		OpenSource: func(context.Context, string, v4l2.Options) (Source, error) {
			if opened != nil {
				opened <- struct{}{}
			}
			return source, nil
		},
	}
}

func mustManager(t *testing.T, options Options) *Manager {
	t.Helper()
	manager, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	return manager
}

func snapshotAsync(manager *Manager, ctx context.Context) <-chan snapshotResult {
	done := make(chan snapshotResult, 1)
	go func() {
		snapshot, err := manager.Snapshot(ctx)
		done <- snapshotResult{snapshot, err}
	}()
	return done
}

func TestManagerColdFreshSnapshotAndIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newManagerSource()
		opened := make(chan struct{}, 1)
		manager := mustManager(t, managerOptions(source, opened))
		if info := manager.Info(); info.Running || info.Generation != 1 || info.Err != nil {
			t.Fatalf("new manager opened a device: %+v", info)
		}
		done := snapshotAsync(manager, context.Background())
		<-opened
		data := managerJPEG(t, true)
		for i, frame := range []v4l2.Frame{managerFrame(data, 0, 1), managerFrame(data, 1, 2), managerFrame([]byte("corrupt warmup"), 2, 3), managerFrame(data, 3, 4)} {
			<-source.reading
			source.reads <- managerRead{frame: frame}
			if i < 3 {
				synctest.Wait()
				select {
				case result := <-done:
					t.Fatalf("old/equal/corrupt frame satisfied request: %+v", result)
				default:
				}
			}
		}
		result := <-done
		if result.err != nil || result.snapshot.Width != 128 || result.snapshot.Height != 72 || result.snapshot.Generation != 1 {
			t.Fatalf("cold letterboxed snapshot=%+v", result)
		}
		config, err := jpeg.DecodeConfig(bytes.NewReader(result.snapshot.JPEG))
		if err != nil || config.Width != 128 || config.Height != 72 {
			t.Fatalf("snapshot metadata differs from JPEG: %+v,%v", config, err)
		}
		time.Sleep(1999 * time.Millisecond)
		synctest.Wait()
		if source.closes.Load() != 0 {
			t.Fatal("source stopped before idle timeout")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if source.closes.Load() != 1 || manager.Info().Running {
			t.Fatal("source remained running after idle timeout")
		}
	})
}

func TestManagerSnapshotTotalDeadline(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "no frames", true: "only corrupt frames"}[corrupt], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				source := newManagerSource()
				manager := mustManager(t, managerOptions(source, nil))
				started := time.Now()
				done := snapshotAsync(manager, context.Background())
				if corrupt {
					<-source.reading
					source.reads <- managerRead{frame: managerFrame([]byte("bad JPEG"), 2, 1)}
				}
				result := <-done
				if !errors.Is(result.err, context.DeadlineExceeded) || time.Since(started) != 5*time.Second {
					t.Fatalf("total acquisition deadline=%v elapsed=%s", result.err, time.Since(started))
				}
				manager.mu.Lock()
				pending := len(manager.pending)
				manager.mu.Unlock()
				if pending != 0 {
					t.Fatal("timed-out snapshot retained interest")
				}
				time.Sleep(2 * time.Second)
				synctest.Wait()
				if source.closes.Load() != 1 {
					t.Fatal("deadline leaked a running source")
				}
			})
		})
	}
}

func TestManagerSnapshotCancellationIsIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newManagerSource()
		options := managerOptions(source, nil)
		var clock atomic.Int64
		clock.Store(1_000)
		options.MonotonicNow = func() (int64, error) { return clock.Load(), nil }
		manager := mustManager(t, options)
		subscription, err := manager.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		first := snapshotAsync(manager, ctx)
		synctest.Wait()
		clock.Store(2_000)
		second := snapshotAsync(manager, context.Background())
		synctest.Wait()
		cancel()
		if result := <-first; !errors.Is(result.err, context.Canceled) {
			t.Fatalf("snapshot cancellation=%v", result.err)
		}
		data := managerJPEG(t, false)
		source.reads <- managerRead{frame: managerFrame(data, 2, 1)}
		if frame, err := subscription.Next(context.Background()); err != nil || frame.Metadata().Sequence != 1 {
			t.Fatalf("other subscriber was canceled: %v,%v", frame, err)
		}
		select {
		case result := <-second:
			t.Fatalf("equal timestamp satisfied later snapshot: %+v", result)
		default:
		}
		source.reads <- managerRead{frame: managerFrame(data, 3, 2)}
		if result := <-second; result.err != nil {
			t.Fatal(result.err)
		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if source.closes.Load() != 0 {
			t.Fatal("snapshot cancellation/finish stopped an active subscriber")
		}
		_ = subscription.Close()
		_ = subscription.Close()
		if _, err := subscription.Next(context.Background()); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed subscription returned a buffered frame: %v", err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if source.closes.Load() != 1 {
			t.Fatal("last interest did not stop source")
		}
	})
}

func TestManagerLatestFrameWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newManagerSource()
		manager := mustManager(t, managerOptions(source, nil))
		fast, _ := manager.Subscribe()
		slow, _ := manager.Subscribe()
		data := managerJPEG(t, false)
		for sequence := uint32(1); sequence <= 3; sequence++ {
			source.reads <- managerRead{frame: managerFrame(data, int64(sequence+1), sequence)}
			frame, err := fast.Next(context.Background())
			if err != nil || frame.Metadata().Sequence != sequence {
				t.Fatalf("fast reader sequence=%v,%v", frame, err)
			}
		}
		synctest.Wait() // Fast delivery does not guarantee the slow slot is updated.
		if frame, err := slow.Next(context.Background()); err != nil || frame.Metadata().Sequence != 3 {
			t.Fatalf("slow subscriber blocked publication or kept old frame: %v,%v", frame, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		waiting := make(chan error, 1)
		go func() { _, err := slow.Next(ctx); waiting <- err }()
		synctest.Wait()
		cancel()
		if err := <-waiting; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := slow.Next(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestManagerNewInterestCancelsIdleStopAndLaterDemandRestarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := newManagerSource(), newManagerSource()
		var opens atomic.Int32
		options := managerOptions(first, nil)
		options.OpenSource = func(context.Context, string, v4l2.Options) (Source, error) {
			if opens.Add(1) == 1 {
				return first, nil
			}
			return second, nil
		}
		manager := mustManager(t, options)
		done := snapshotAsync(manager, context.Background())
		first.reads <- managerRead{frame: managerFrame(managerJPEG(t, false), 2, 1)}
		if result := <-done; result.err != nil {
			t.Fatal(result.err)
		}
		time.Sleep(time.Second)
		subscription, err := manager.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if first.closes.Load() != 0 || opens.Load() != 1 {
			t.Fatal("new interest failed to cancel idle stop")
		}
		_ = subscription.Close()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if first.closes.Load() != 1 {
			t.Fatal("source did not stop after final interest ended")
		}
		done = snapshotAsync(manager, context.Background())
		second.reads <- managerRead{frame: managerFrame(managerJPEG(t, false), 3, 1)}
		if result := <-done; result.err != nil || opens.Load() != 2 {
			t.Fatalf("next demand failed to restart source: %+v opens=%d", result, opens.Load())
		}
	})
}

func TestManagerOptionsAvailabilityAndDefaultSource(t *testing.T) {
	for _, options := range []Options{{}, {Width: 1}, {Height: 1}, {Width: 1, Height: 1, IdleTimeout: -1}, {Device: "+1", Width: 1, Height: 1}, {Width: 1, Height: 1, ScreenWidth: -1}, {Width: 1, Height: 1, ScreenHeight: -1}} {
		if _, err := New(options); !errors.Is(err, ErrOptions) {
			t.Errorf("invalid options accepted: %+v,%v", options, err)
		}
	}
	manager := mustManager(t, Options{Width: 128, Height: 96})
	if manager.options.IdleTimeout != 10*time.Second {
		t.Fatalf("default idle grace=%s; want 10s", manager.options.IdleTimeout)
	}
	if _, err := manager.Snapshot(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unselected snapshot=%v", err)
	}
	if err := manager.SetResolution(context.Background(), 1, 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	unavailable, err := manager.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unavailable.Next(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unselected subscriber waited without status: %v", err)
	}
	_ = unavailable.Close()
	for _, path := range []string{"", "0", "-1", "+2"} {
		if err := manager.SetDevice(context.Background(), path); !errors.Is(err, ErrOptions) {
			t.Fatalf("invalid selected path %q=%v", path, err)
		}
	}
	if err := manager.SetResolution(context.Background(), 0, 1); !errors.Is(err, ErrOptions) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Snapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := manager.SetDevice(ctx, "/dev/other"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "absent-device")
	if err := manager.SetDevice(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	expected := os.ErrNotExist
	switch runtime.GOOS {
	case "darwin":
		expected = avfoundation.ErrDevice
	case "windows":
		expected = directshow.ErrDevice
	}
	if _, err := manager.Snapshot(context.Background()); !errors.Is(err, ErrUnavailable) || !errors.Is(err, expected) {
		t.Fatalf("default source open error lost: %v", err)
	}
	if info := manager.Info(); info.Device != path || info.Running || !errors.Is(info.Err, expected) {
		t.Fatalf("failed open lost settings/status: %+v", info)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Snapshot(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := manager.Subscribe(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := manager.SetDevice(context.Background(), "/dev/new"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
