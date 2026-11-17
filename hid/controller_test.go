package hid

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

// cancelAtAdmission closes a real context while run evaluates its admission
// select, making cancellation and a free action gate ready together.
type cancelAtAdmission struct {
	context.Context
	cancel context.CancelFunc
}

func (ctx cancelAtAdmission) Done() <-chan struct{} {
	ctx.cancel()
	return ctx.Context.Done()
}

func TestControllerCancellationAtAdmission(t *testing.T) {
	controller, keyboard, transport := keyboardFixture(t, "us104")
	for range 128 {
		ctx, cancel := context.WithCancel(context.Background())
		if err := keyboard.SendKey(cancelAtAdmission{ctx, cancel}, "a", nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled admission returned %v", err)
		}
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
		t.Fatalf("cancelled admission touched hardware: prepares=%d writes=%d", prepares, writes)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestControllerQueuedCancellationAndActionOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, keyboard, transport := keyboardFixture(t, "us104")
		first := make(chan error, 1)
		go func() { first <- keyboard.TypeText(context.Background(), "ab", TypeOptions{}) }()
		synctest.Wait()
		ctx, cancel := context.WithCancel(context.Background())
		queued := make(chan error, 1)
		go func() { queued <- keyboard.SendKey(ctx, "z", nil) }()
		synctest.Wait()
		cancel()
		if err := <-queued; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		second := make(chan error, 1)
		go func() {
			second <- controller.run(context.Background(), releaseMouse, func(current *action) error {
				return current.write(ch9329.MouseRelative(0, 1, 0, 0))
			})
		}()
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if err := <-second; err != nil {
			t.Fatal(err)
		}
		prepares, writes, _, events := transport.snapshot()
		if prepares != 2 || writes != 5 || events[4].At != 60*time.Millisecond || events[4].Frame[3] != 5 {
			t.Fatalf("actions interleaved or queued cancellation emitted input: prepares=%d events=%v", prepares, events)
		}
	})
}

func TestControllerCancellationCleanup(t *testing.T) {
	for _, cancelAt := range []time.Duration{5 * time.Millisecond, 15 * time.Millisecond} {
		t.Run(cancelAt.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				controller, keyboard, transport := keyboardFixture(t, "us104")
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- keyboard.TypeText(ctx, "ab", TypeOptions{}) }()
				time.Sleep(cancelAt)
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				prepares, writes, _, events := transport.snapshot()
				if prepares != 2 || !bytes.Equal(events[writes-1].Frame, ch9329.Keyboard(0)) || events[writes-1].At != cancelAt {
					t.Fatalf("matching cleanup missing: prepares=%d events=%v", prepares, events)
				}
				if len(controller.keys) != 0 || controller.modifier != 0 {
					t.Fatal("cleanup retained held keyboard state")
				}
				if err := keyboard.SendKey(context.Background(), "z", nil); err != nil {
					t.Fatalf("cancelled action stranded gate: %v", err)
				}
			})
		})
	}
}

func TestControllerFailedSendRecovery(t *testing.T) {
	controller, keyboard, transport := keyboardFixture(t, "us104")
	if err := controller.run(context.Background(), releaseMouse, func(current *action) error {
		controller.buttons = 1
		return current.write(ch9329.MouseRelative(1, 0, 0, 0))
	}); err != nil {
		t.Fatal(err)
	}
	pressError := errors.New("press pacing failed")
	transport.prepareHook = func(_ context.Context, n int) (uint64, error) {
		if n == 2 {
			return 1, nil
		}
		return 2, nil
	}
	transport.writeHook = func(context.Context, int, []byte) error {
		if controller.buttons != 1 {
			t.Error("earlier mouse-down was cleared before the failed keypress")
		}
		return pressError
	}
	if err := keyboard.SendKey(context.Background(), "a", nil); !errors.Is(err, pressError) {
		t.Fatal(err)
	}
	prepares, writes, _, _ := transport.snapshot()
	if prepares != 3 || writes != 2 || controller.generation != 2 || len(controller.keys) != 0 || controller.buttons != 0 {
		t.Fatalf("failed press was replayed or recovery retained state: prepares=%d writes=%d controller=%+v", prepares, writes, controller)
	}
}

func TestControllerCleanupFailures(t *testing.T) {
	for _, failPrepare := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "prepare"}[failPrepare], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				controller, keyboard, transport := keyboardFixture(t, "us104")
				original := errors.New("original write failure")
				cleanup := errors.New("cleanup failure")
				transport.writeHook = func(_ context.Context, n int, _ []byte) error {
					if n == 2 {
						return original
					}
					if n == 3 {
						return cleanup
					}
					return nil
				}
				transport.prepareHook = func(_ context.Context, n int) (uint64, error) {
					if failPrepare && n == 2 {
						return 0, cleanup
					}
					if n > 2 {
						return 2, nil
					}
					return 1, nil
				}
				if err := keyboard.SendKey(context.Background(), "a", nil); !errors.Is(err, original) || !errors.Is(err, cleanup) {
					t.Fatalf("lost original/cleanup error: %v", err)
				}
				if err := controller.run(context.Background(), releaseKeyboard, func(*action) error {
					if controller.buttons != 0 || len(controller.keys) != 0 || controller.modifier != 0 {
						t.Error("new generation did not clear state before the next action")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestControllerPreInputFailures(t *testing.T) {
	controller, keyboard, transport := keyboardFixture(t, "us104")
	prepareError := errors.New("prepare failed")
	transport.prepareHook = func(context.Context, int) (uint64, error) { return 0, prepareError }
	if err := keyboard.SendKey(context.Background(), "a", nil); !errors.Is(err, prepareError) {
		t.Fatal(err)
	}
	transport.prepareHook = nil
	ctx, cancel := context.WithCancel(context.Background())
	if err := controller.run(ctx, releaseKeyboard, func(current *action) error {
		cancel()
		return current.write(ch9329.Keyboard(0, 4))
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	transport.prepareHook = func(context.Context, int) (uint64, error) {
		cancel()
		return 1, nil
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if err := keyboard.SendKey(ctx, "a", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 3 || writes != 0 {
		t.Fatalf("pre-input failure emitted cleanup: prepares=%d writes=%d", prepares, writes)
	}
}

func TestControllerCloseUnusedAndConcurrent(t *testing.T) {
	controller, keyboard, transport := keyboardFixture(t, "us104")
	closed := errors.New("transport close failure")
	transport.closeError = closed
	var callers sync.WaitGroup
	for range 16 {
		callers.Go(func() {
			if err := controller.Close(); !errors.Is(err, closed) {
				t.Errorf("Close lost its shared error: %v", err)
			}
		})
	}
	callers.Wait()
	for range 64 {
		if err := keyboard.SendKey(context.Background(), "a", nil); !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
	}
	if prepares, writes, closes, _ := transport.snapshot(); prepares != 0 || writes != 0 || closes != 1 {
		t.Fatalf("closing unused controller opened hardware or repeated Close: %d/%d/%d", prepares, writes, closes)
	}
}

func TestControllerCloseReleasesAll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, keyboard, transport := keyboardFixture(t, "us104")
		if err := controller.run(context.Background(), releaseMouse, func(current *action) error {
			controller.buttons = 1
			return current.write(ch9329.MouseRelative(1, 0, 0, 0))
		}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- keyboard.SendKey(context.Background(), "a", nil) }()
		time.Sleep(5 * time.Millisecond)
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		prepares, writes, closes, events := transport.snapshot()
		if prepares != 3 || writes != 4 || closes != 1 || !bytes.Equal(events[2].Frame, ch9329.Keyboard(0)) ||
			!bytes.Equal(events[3].Frame, ch9329.MouseRelative(0, 0, 0, 0)) || controller.buttons != 0 {
			t.Fatalf("shutdown failed to release both states: %d/%d/%d %v", prepares, writes, closes, events)
		}
	})
}

func TestControllerCloseReusesActiveCleanupBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, keyboard, transport := keyboardFixture(t, "us104")
		entered := make(chan struct{})
		var deadlines []time.Time
		transport.writeHook = func(ctx context.Context, n int, _ []byte) error {
			if n >= 2 {
				deadline, _ := ctx.Deadline()
				deadlines = append(deadlines, deadline)
				if n == 2 {
					close(entered)
					<-ctx.Done()
				}
			}
			return ctx.Err()
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- keyboard.SendKey(ctx, "a", nil) }()
		time.Sleep(5 * time.Millisecond)
		cancel()
		<-entered
		if err := controller.Close(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close lost cleanup timeout: %v", err)
		}
		if err := <-done; !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("action lost original/cleanup errors: %v", err)
		}
		if len(deadlines) != 2 || deadlines[0] != deadlines[1] || time.Since(transport.start) != time.Second+5*time.Millisecond {
			t.Fatalf("shutdown started another cleanup budget: deadlines=%v elapsed=%s", deadlines, time.Since(transport.start))
		}
	})
}

func TestControllerCloseCleanupFailures(t *testing.T) {
	for _, prepareFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "reports", true: "prepare"}[prepareFails], func(t *testing.T) {
			controller, _, transport := keyboardFixture(t, "us104")
			if err := controller.run(context.Background(), releaseKeyboard, func(*action) error { return nil }); err != nil {
				t.Fatal(err)
			}
			releaseError := errors.New("release failed")
			closeError := errors.New("close failed")
			transport.closeError = closeError
			transport.writeHook = func(context.Context, int, []byte) error { return releaseError }
			if prepareFails {
				transport.prepareHook = func(context.Context, int) (uint64, error) { return 0, releaseError }
			}
			if err := controller.Close(); !errors.Is(err, releaseError) || !errors.Is(err, closeError) {
				t.Fatalf("Close lost a release/close error: %v", err)
			}
		})
	}
}

func TestControllerCloseAfterRecoveryReset(t *testing.T) {
	controller, keyboard, transport := keyboardFixture(t, "us104")
	entered := make(chan struct{})
	transport.writeHook = func(ctx context.Context, _ int, _ []byte) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	transport.prepareHook = func(_ context.Context, n int) (uint64, error) { return uint64(n), nil }
	done := make(chan error, 1)
	go func() { done <- keyboard.SendKey(context.Background(), "a", nil) }()
	<-entered
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if prepares, writes, closes, _ := transport.snapshot(); prepares != 2 || writes != 1 || closes != 1 {
		t.Fatalf("Close repeated an already-completed reset: %d/%d/%d", prepares, writes, closes)
	}
}
