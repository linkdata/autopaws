//go:build (linux || darwin) && (amd64 || arm64)

package capture

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/linkdata/autopaws/v4l2"
)

func TestManagerSwitchFlushesOldGenerationAndWaitsForOwnedClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old, current := newManagerSource(), newManagerSource()
		old.ignoreCancel = true
		releaseClose := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(releaseClose) })
		t.Cleanup(unblock)
		old.closeGate = releaseClose
		opened := make(chan string, 2)
		options := managerOptions(old, nil)
		options.OpenSource = func(_ context.Context, path string, _ v4l2.Options) (Source, error) {
			opened <- path
			if path == "/dev/new" {
				return current, nil
			}
			return old, nil
		}
		manager := mustManager(t, options)
		t.Cleanup(unblock)
		subscription, _ := manager.Subscribe()
		<-opened
		old.reads <- managerRead{frame: managerFrame(managerJPEG(t, true), 2, 1)}
		<-old.reading
		<-old.reading // First frame has been published and next read owns source.
		pending := snapshotAsync(manager, context.Background())
		synctest.Wait()
		switched := make(chan error, 1)
		go func() { switched <- manager.SetDevice(context.Background(), "/dev/new") }()
		synctest.Wait()
		if result := <-pending; !errors.Is(result.err, ErrCaptureChanged) {
			t.Fatalf("old pending snapshot switched targets: %v", result.err)
		}
		if len(subscription.frames) != 0 {
			t.Fatal("switch left an unsent old frame in subscriber slot")
		}
		old.reads <- managerRead{frame: managerFrame(managerJPEG(t, true), 3, 99)}
		synctest.Wait()
		select {
		case path := <-opened:
			t.Fatalf("replacement %s opened before old Close completed", path)
		default:
		}
		unblock()
		if path := <-opened; path != "/dev/new" {
			t.Fatal(path)
		}
		if err := <-switched; err != nil {
			t.Fatal(err)
		}
		retried := snapshotAsync(manager, context.Background())
		synctest.Wait()
		current.reads <- managerRead{frame: managerFrame(managerJPEG(t, false), 4, 1)}
		if result := <-retried; result.err != nil || result.snapshot.Generation != 2 || result.snapshot.Height != 96 {
			t.Fatalf("new-generation snapshot inherited old source/crop: %+v", result)
		}
		frame, err := subscription.Next(context.Background())
		if err != nil || frame.Metadata().Generation != 2 || !frame.Metadata().Crop.Empty() {
			t.Fatalf("subscription did not resume with reset crop: %v,%v", frame, err)
		}
		if old.closes.Load() != 1 || manager.Info().Err != nil {
			t.Fatal("switch lost old source ownership or retained failed status")
		}
	})
}

func TestManagerFailedOpenResolutionRepair(t *testing.T) {
	for _, subscribed := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "subscribed"}[subscribed], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				source := newManagerSource()
				failure := errors.New("unsupported old size")
				var attempts atomic.Int32
				options := managerOptions(source, nil)
				options.Width = 1
				options.OpenSource = func(_ context.Context, _ string, options v4l2.Options) (Source, error) {
					attempts.Add(1)
					if options.Width != 128 {
						return nil, failure
					}
					return source, nil
				}
				manager := mustManager(t, options)
				if subscribed {
					if _, err := manager.Subscribe(); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					if err := manager.SetResolution(context.Background(), 2, 96); !errors.Is(err, failure) || !errors.Is(err, ErrUnavailable) {
						t.Fatalf("failed subscribed retry=%v", err)
					}
				} else if _, err := manager.Snapshot(context.Background()); !errors.Is(err, failure) {
					t.Fatalf("failed idle startup=%v", err)
				}
				// Failure is reported before the old producer retires. Let that
				// owner finish before exercising idle/subscribed repair.
				synctest.Wait()
				before := attempts.Load()
				if err := manager.SetResolution(context.Background(), 128, 96); err != nil {
					t.Fatal(err)
				}
				if !subscribed && attempts.Load() != before {
					t.Fatal("idle repair opened source before demand")
				}
				done := snapshotAsync(manager, context.Background())
				synctest.Wait() // Existing subscriber may already have signalled ReadFrame.
				<-source.reading
				source.reads <- managerRead{frame: managerFrame(managerJPEG(t, false), 2, 1)}
				if result := <-done; result.err != nil {
					t.Fatalf("repaired snapshot=%v", result.err)
				}
				info := manager.Info()
				if info.Width != 128 || info.Height != 96 || info.Device != "/dev/fake" || info.Err != nil || info.Format.Width != 128 {
					t.Fatalf("repair lost requested/actual settings: %+v", info)
				}
			})
		})
	}
}

func TestManagerFailureNotifiesBeforeOwnedCloseAndSubscriptionRepairs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old, current := newManagerSource(), newManagerSource()
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		old.closeGate = release
		options := managerOptions(old, nil)
		options.OpenSource = func(_ context.Context, path string, _ v4l2.Options) (Source, error) {
			if path == "/dev/repaired" {
				return current, nil
			}
			return old, nil
		}
		manager := mustManager(t, options)
		t.Cleanup(unblock)
		subscription, _ := manager.Subscribe()
		pending := snapshotAsync(manager, context.Background())
		synctest.Wait()
		old.reads <- managerRead{err: v4l2.ErrDisconnected}
		if result := <-pending; !errors.Is(result.err, ErrUnavailable) || !errors.Is(result.err, v4l2.ErrDisconnected) {
			t.Fatalf("failure was held behind driver Close: %v", result.err)
		}
		if _, err := subscription.Next(context.Background()); !errors.Is(err, v4l2.ErrDisconnected) {
			t.Fatalf("subscriber was not told about failed source: %v", err)
		}
		if old.closes.Load() != 0 || !errors.Is(manager.Info().Err, v4l2.ErrDisconnected) || manager.Info().Running {
			t.Fatal("test did not retain blocked kernel cleanup or expose failure")
		}
		if _, err := manager.Snapshot(context.Background()); !errors.Is(err, v4l2.ErrDisconnected) {
			t.Fatalf("known failed source made new snapshot wait for blocked Close: %v", err)
		}
		late, err := manager.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := late.Next(context.Background()); !errors.Is(err, v4l2.ErrDisconnected) {
			t.Fatalf("new subscriber missed known failure: %v", err)
		}
		_ = late.Close()
		ctx, cancel := context.WithCancel(context.Background())
		waiting := make(chan error, 1)
		go func() { _, err := subscription.Next(ctx); waiting <- err }()
		synctest.Wait()
		cancel()
		if err := <-waiting; !errors.Is(err, context.Canceled) {
			t.Fatalf("failure event repeated instead of waiting for repair: %v", err)
		}
		repaired := make(chan error, 1)
		go func() { repaired <- manager.SetDevice(context.Background(), "/dev/repaired") }()
		synctest.Wait()
		unblock()
		if err := <-repaired; err != nil {
			t.Fatal(err)
		}
		current.reads <- managerRead{frame: managerFrame(managerJPEG(t, false), 2, 1)}
		if frame, err := subscription.Next(context.Background()); err != nil || frame.Metadata().Generation != 2 {
			t.Fatalf("subscription was lost across repair: %v,%v", frame, err)
		}
		if manager.Info().Err != nil {
			t.Fatal("successful repair retained prior failure")
		}
	})
}

func TestManagerConcurrentSnapshotsJoinHealthyRetryAfterFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newManagerSource()
		failure := errors.New("previous source failed to open")
		retrying, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		var attempts atomic.Int32
		options := managerOptions(source, nil)
		options.OpenSource = func(context.Context, string, v4l2.Options) (Source, error) {
			if attempts.Add(1) == 1 {
				return nil, failure
			}
			close(retrying)
			<-release
			return source, nil
		}
		manager := mustManager(t, options)
		t.Cleanup(unblock)
		if _, err := manager.Snapshot(context.Background()); !errors.Is(err, failure) {
			t.Fatalf("initial failure=%v", err)
		}
		synctest.Wait() // The failed owner has finished; a demand can retry.
		first := snapshotAsync(manager, context.Background())
		<-retrying
		second := snapshotAsync(manager, context.Background())
		subscription, err := manager.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		for _, done := range []<-chan snapshotResult{first, second} {
			select {
			case result := <-done:
				t.Fatalf("healthy retry rejected concurrent demand with old failure: %+v", result)
			default:
			}
		}
		if len(subscription.frames) != 0 || !errors.Is(manager.Info().Err, failure) {
			t.Fatal("retry did not retain diagnostic separately from current run state")
		}
		unblock()
		source.reads <- managerRead{frame: managerFrame(managerJPEG(t, false), 2, 1)}
		for _, done := range []<-chan snapshotResult{first, second} {
			if result := <-done; result.err != nil || result.snapshot.Width != 128 {
				t.Fatalf("joined retry snapshot=%+v", result)
			}
		}
		if frame, err := subscription.Next(context.Background()); err != nil || frame.Metadata().Sequence != 1 {
			t.Fatalf("subscriber did not join healthy retry: %v,%v", frame, err)
		}
		if manager.Info().Err != nil || attempts.Load() != 2 {
			t.Fatal("successful shared retry retained failure or reopened again")
		}
	})
}

func TestManagerStartupDeadlineDoesNotAbandonOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newManagerSource()
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		options := managerOptions(source, nil)
		options.OpenSource = func(context.Context, string, v4l2.Options) (Source, error) {
			close(entered)
			<-release // Models a synchronous driver setup ignoring cancellation.
			return source, nil
		}
		manager, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { unblock(); _ = manager.Close() })
		started := time.Now()
		done := snapshotAsync(manager, context.Background())
		<-entered
		if result := <-done; !errors.Is(result.err, context.DeadlineExceeded) || time.Since(started) != 5*time.Second {
			t.Fatalf("startup exceeded waiter budget: %v elapsed=%s", result.err, time.Since(started))
		}
		closed := make(chan error, 1)
		go func() { closed <- manager.Close() }()
		synctest.Wait()
		select {
		case err := <-closed:
			t.Fatalf("Close abandoned blocked driver setup: %v", err)
		default:
		}
		unblock()
		if err := <-closed; err != nil || source.closes.Load() != 1 {
			t.Fatalf("late source ownership not joined: closes=%d err=%v", source.closes.Load(), err)
		}
	})
}

func TestManagerSettersSupersedeCancelAndKeepAcceptedSettings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newManagerSource()
		opened, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		options := managerOptions(source, nil)
		options.Device = ""
		options.OpenSource = func(context.Context, string, v4l2.Options) (Source, error) {
			close(opened)
			<-release
			return source, nil
		}
		manager, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { unblock(); _ = manager.Close() })
		_, _ = manager.Subscribe()
		first := make(chan error, 1)
		go func() { first <- manager.SetDevice(context.Background(), "/dev/one") }()
		<-opened
		ctx, cancel := context.WithCancel(context.Background())
		second := make(chan error, 1)
		go func() { second <- manager.SetDevice(ctx, "/dev/two") }()
		if err := <-first; !errors.Is(err, ErrCaptureChanged) {
			t.Fatalf("superseded setter=%v", err)
		}
		cancel()
		if err := <-second; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled setter=%v", err)
		}
		if info := manager.Info(); info.Device != "/dev/two" || info.Generation != 3 {
			t.Fatalf("cancellation rolled accepted settings back: %+v", info)
		}
		closed := make(chan error, 1)
		go func() { closed <- manager.Close() }()
		synctest.Wait()
		if err := manager.waitOpen(context.Background(), 3); !errors.Is(err, ErrClosed) {
			t.Fatalf("open wait after Close=%v", err)
		}
		unblock()
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
	})
}

func TestManagerClockAndTerminalSourceErrors(t *testing.T) {
	clockFailure := errors.New("clock unavailable")
	options := managerOptions(newManagerSource(), nil)
	options.MonotonicNow = func() (int64, error) { return 0, clockFailure }
	manager := mustManager(t, options)
	if _, err := manager.Snapshot(context.Background()); !errors.Is(err, ErrUnavailable) || !errors.Is(err, clockFailure) {
		t.Fatalf("clock error lost: %v", err)
	}
	for _, kind := range []string{"clock domain", "timestamp", "unplug"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				source := newManagerSource()
				manager := mustManager(t, managerOptions(source, nil))
				done := snapshotAsync(manager, context.Background())
				<-source.reading
				frame := managerFrame(managerJPEG(t, false), 2, 1)
				input := managerRead{frame: frame}
				switch kind {
				case "clock domain":
					input.frame.Flags = v4l2.TimestampUnknown
				case "timestamp":
					input.frame.Timestamp.Microseconds = 1_000_000
				case "unplug":
					input.err = v4l2.ErrDisconnected
				}
				source.reads <- input
				if result := <-done; !errors.Is(result.err, ErrUnavailable) {
					t.Fatalf("%s error=%v", kind, result.err)
				}
			})
		})
	}
}

func TestManagerCloseJoinsAndRetainsCleanupFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newManagerSource()
		failure := errors.New("STREAMOFF failed")
		source.closeErr = failure
		manager, err := New(managerOptions(source, nil))
		if err != nil {
			t.Fatal(err)
		}
		subscription, _ := manager.Subscribe()
		<-source.reading
		pending := snapshotAsync(manager, context.Background())
		synctest.Wait()
		if err := manager.Close(); !errors.Is(err, failure) {
			t.Fatalf("source close failure lost: %v", err)
		}
		if result := <-pending; !errors.Is(result.err, ErrClosed) {
			t.Fatalf("Close left pending snapshot: %v", result.err)
		}
		if _, err := subscription.Next(context.Background()); !errors.Is(err, ErrClosed) {
			t.Fatalf("Close left subscription alive: %v", err)
		}
		if err := manager.Close(); !errors.Is(err, failure) || source.closes.Load() != 1 {
			t.Fatalf("repeated Close=%v closes=%d", err, source.closes.Load())
		}
		if !errors.Is(manager.Info().Err, ErrClosed) {
			t.Fatal("closed manager info appeared healthy/idle")
		}
	})
}

func TestManagerIdleCleanupFailureIsVisibleAndRetained(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newManagerSource()
		failure := errors.New("idle STREAMOFF failure")
		source.closeErr = failure
		manager, err := New(managerOptions(source, nil))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		done := snapshotAsync(manager, context.Background())
		source.reads <- managerRead{frame: managerFrame(managerJPEG(t, false), 2, 1)}
		if result := <-done; result.err != nil {
			t.Fatal(result.err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if info := manager.Info(); info.Running || !errors.Is(info.Err, failure) || !errors.Is(info.Err, ErrUnavailable) {
			t.Fatalf("idle cleanup failure hidden: %+v", info)
		}
		if err := manager.Close(); !errors.Is(err, failure) || source.closes.Load() != 1 {
			t.Fatalf("cleanup error/ownership lost: %v closes=%d", err, source.closes.Load())
		}
	})
}
