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

func TestAutomaticRecoveryJoinsCloseAndPreservesFreshSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old, current := newManagerSource(), newManagerSource()
		opened := make(chan struct{})
		closeGate, openGate := make(chan struct{}), make(chan struct{})
		unblockClose := sync.OnceFunc(func() { close(closeGate) })
		unblockOpen := sync.OnceFunc(func() { close(openGate) })
		old.closeGate = closeGate
		var attempts atomic.Int32
		options := managerOptions(old, nil)
		options.OpenSource = func(context.Context, string, v4l2.Options) (Source, error) {
			if attempts.Add(1) == 1 {
				return old, nil
			}
			close(opened)
			<-openGate
			return current, nil
		}
		manager := mustManager(t, options)
		manager.recoveryDelay = time.Second
		t.Cleanup(unblockClose)
		t.Cleanup(unblockOpen)
		subscription, _ := manager.Subscribe()
		<-old.reading
		old.reads <- managerRead{frame: managerFrame(managerJPEG(t, true), 2, 1)}
		if _, err := subscription.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		pending := snapshotAsync(manager, context.Background())
		synctest.Wait()
		old.reads <- managerRead{err: v4l2.ErrDisconnected}
		if result := <-pending; !errors.Is(result.err, ErrUnavailable) {
			t.Fatalf("source failure was not published before Close: %v", result.err)
		}
		if _, err := subscription.Next(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("subscriber missed source failure: %v", err)
		}
		time.Sleep(3 * time.Second)
		if attempts.Load() != 1 || manager.Info().Running || manager.Info().Generation != 1 {
			t.Fatal("recovery overlapped owned Close or changed generation before recovery")
		}
		unblockClose()
		<-old.closed
		synctest.Wait()
		time.Sleep(time.Second)
		<-opened
		pending = snapshotAsync(manager, context.Background())
		synctest.Wait()
		unblockOpen()
		<-current.reading
		if info := manager.Info(); !info.Running || info.Err != nil || info.Generation != 2 {
			t.Fatalf("successful recovery state = %+v", info)
		}
		current.reads <- managerRead{frame: managerFrame(managerJPEG(t, false), 1, 1)}
		synctest.Wait()
		select {
		case result := <-pending:
			t.Fatalf("recovery accepted a pre-request frame: %+v", result)
		default:
		}
		current.reads <- managerRead{frame: managerFrame(managerJPEG(t, false), 3, 2)}
		if result := <-pending; result.err != nil || result.snapshot.Generation != 2 || result.snapshot.Height != 96 {
			t.Fatalf("pending snapshot did not join recovered generation: %+v", result)
		}
		frame, err := subscription.Next(context.Background())
		if err != nil || frame.Metadata().Generation != 2 || frame.Metadata().Sequence != 2 || !frame.Metadata().Crop.Empty() {
			t.Fatalf("subscription retained old source generation or crop: %v, %v", frame, err)
		}
		if old.closes.Load() != 1 || attempts.Load() != 2 {
			t.Fatal("recovery did not retain one source owner per attempt")
		}
	})
}

func TestAutomaticRecoveryBacksOffUntilHealthy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current := newManagerSource()
		var attempts atomic.Int32
		options := managerOptions(current, nil)
		options.OpenSource = func(context.Context, string, v4l2.Options) (Source, error) {
			if attempts.Add(1) < 3 {
				return nil, v4l2.ErrDisconnected
			}
			return current, nil
		}
		manager := mustManager(t, options)
		manager.recoveryDelay = time.Second
		subscription, _ := manager.Subscribe()
		synctest.Wait()
		if attempts.Load() != 1 {
			t.Fatal("initial failed source spun")
		}
		time.Sleep(time.Second - time.Millisecond)
		if attempts.Load() != 1 {
			t.Fatal("retry ignored its backoff")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if attempts.Load() != 2 || manager.Info().Generation != 1 {
			t.Fatal("failed retry spun or advanced the recovery generation")
		}
		pending := snapshotAsync(manager, context.Background())
		synctest.Wait()
		time.Sleep(time.Second)
		<-current.reading
		current.reads <- managerRead{frame: managerFrame(managerJPEG(t, false), 3, 1)}
		if result := <-pending; result.err != nil || result.snapshot.Generation != 2 {
			t.Fatalf("snapshot did not resume after backoff: %+v", result)
		}
		if frame, err := subscription.Next(context.Background()); err != nil || frame.Metadata().Generation != 2 {
			t.Fatalf("existing subscription did not resume: %v, %v", frame, err)
		}
		if attempts.Load() != 3 {
			t.Fatal("successful source was reopened again")
		}
	})
}

func TestAutomaticRecoveryCancellation(t *testing.T) {
	for _, action := range []string{"unsubscribe", "settings", "close"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				current := newManagerSource()
				var attempts atomic.Int32
				options := managerOptions(current, nil)
				options.OpenSource = func(_ context.Context, path string, _ v4l2.Options) (Source, error) {
					attempts.Add(1)
					if path == "/dev/new" {
						return current, nil
					}
					return nil, v4l2.ErrDisconnected
				}
				manager := mustManager(t, options)
				manager.recoveryDelay = time.Second
				subscription, _ := manager.Subscribe()
				synctest.Wait()
				switch action {
				case "unsubscribe":
					_ = subscription.Close()
				case "settings":
					if err := manager.SetDevice(context.Background(), "/dev/new"); err != nil {
						t.Fatal(err)
					}
				case "close":
					if err := manager.Close(); err != nil {
						t.Fatal(err)
					}
				}
				time.Sleep(3 * time.Second)
				synctest.Wait()
				if action == "settings" {
					if attempts.Load() != 2 || manager.Info().Device != "/dev/new" || manager.Info().Generation != 2 {
						t.Fatalf("retry overrode changed settings: attempts=%d info=%+v", attempts.Load(), manager.Info())
					}
				} else if attempts.Load() != 1 {
					t.Fatal("retry survived lost demand or shutdown")
				}
			})
		})
	}
}

func TestAutomaticRecoveryPreservesTenSecondHealthyIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newManagerSource()
		options := managerOptions(source, nil)
		options.IdleTimeout = 0
		manager := mustManager(t, options)
		manager.recoveryDelay = time.Second
		subscription, _ := manager.Subscribe()
		<-source.reading
		_ = subscription.Close()
		time.Sleep(9 * time.Second)
		if source.closes.Load() != 0 {
			t.Fatal("healthy source closed before the existing idle grace")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if source.closes.Load() != 1 {
			t.Fatal("healthy source was not closed after ten seconds")
		}
	})
}

func TestAutomaticRecoveryOnlyDefaultsForNativeMacOSSource(t *testing.T) {
	defaultManager := mustManager(t, Options{Width: 128, Height: 96})
	if (defaultManager.recoveryDelay > 0) != recoverDefaultSource || defaultManager.options.IdleTimeout != 10*time.Second {
		t.Fatal("native recovery policy changed the platform or idle default")
	}
	customManager := mustManager(t, managerOptions(newManagerSource(), nil))
	if customManager.recoveryDelay != 0 {
		t.Fatal("injected source unexpectedly acquired a platform recovery policy")
	}
}
