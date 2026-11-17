package hid

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

func browserFixture(t *testing.T, mode string) (*Browser, *recordingTransport) {
	t.Helper()
	controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 2, Height: 2, ButtonMode: mode})
	return NewBrowser(controller, mouse), transport
}

func browserFrames(transport *recordingTransport) [][]byte {
	_, _, _, events := transport.snapshot()
	var frames [][]byte
	for _, event := range events {
		frames = append(frames, event.Frame)
	}
	return frames
}

func TestBrowserPhysicalCodeGoldens(t *testing.T) {
	var fixture struct {
		Keys      map[string]byte `json:"keys"`
		Modifiers map[string]byte `json:"modifiers"`
	}
	readJSON(t, "browser_codes.json", &fixture)
	if len(fixture.Keys) != 104 || len(fixture.Modifiers) != 8 || !reflect.DeepEqual(browserKeys, fixture.Keys) || !reflect.DeepEqual(browserModifiers, fixture.Modifiers) {
		t.Fatal("physical-code tables differ from complete Python reference")
	}
	for code, key := range fixture.Keys {
		browser, transport := browserFixture(t, "absolute")
		if err := browser.Key(t.Context(), code, true); err != nil {
			t.Fatal(err)
		}
		if err := browser.Key(t.Context(), code, false); err != nil {
			t.Fatal(err)
		}
		if got, want := browserFrames(transport), [][]byte{ch9329.Keyboard(0, key), ch9329.Keyboard(0)}; !reflect.DeepEqual(got, want) {
			t.Fatalf("physical code %s=%x; want %x", code, got, want)
		}
	}
	for code, bit := range fixture.Modifiers {
		browser, transport := browserFixture(t, "absolute")
		_ = browser.Key(t.Context(), code, true)
		_ = browser.Key(t.Context(), code, false)
		if got, want := browserFrames(transport), [][]byte{ch9329.Keyboard(bit), ch9329.Keyboard(0)}; !reflect.DeepEqual(got, want) {
			t.Fatalf("modifier code %s=%x; want %x", code, got, want)
		}
	}
}

func TestBrowserKeyboardOrderRolloverAndUnknownCodes(t *testing.T) {
	browser, transport := browserFixture(t, "absolute")
	for _, code := range []string{"KeyQ", "KeyW", "KeyE", "KeyR", "KeyT", "KeyY", "KeyU"} {
		if err := browser.Key(t.Context(), code, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, writes, _, _ := transport.snapshot(); writes != 6 || !slices.Equal(browser.keys, []byte{0x14, 0x1a, 8, 0x15, 0x17, 0x1c}) {
		t.Fatal("seventh key changed deterministic six-key press order")
	}
	for _, event := range []struct {
		code string
		down bool
	}{{"KeyQ", true}, {"KeyE", false}, {"KeyU", true}, {"KeyZ", false}, {"ControlRight", true}, {"ControlRight", false}} {
		if err := browser.Key(t.Context(), event.code, event.down); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(browser.keys, []byte{0x14, 0x1a, 0x15, 0x17, 0x1c, 0x18}) || browser.modifier != 0 {
		t.Fatal("released slot was not filled in press order")
	}
	prepares, writes, _, _ := transport.snapshot()
	if err := browser.Key(t.Context(), "UnknownKey", true); err != nil {
		t.Fatal(err)
	}
	if afterPrepares, afterWrites, _, _ := transport.snapshot(); afterPrepares != prepares || afterWrites != writes {
		t.Fatal("unknown physical code touched input")
	}
}

func TestBrowserMouseWireModesAndNormalizedCoordinates(t *testing.T) {
	for _, mode := range []string{"absolute", "relative"} {
		t.Run(mode, func(t *testing.T) {
			browser, transport := browserFixture(t, mode)
			if err := browser.Buttons(t.Context(), 2048, 1024, 1); err != nil {
				t.Fatal(err)
			}
			if err := browser.Move(t.Context(), 1, 4095); err != nil {
				t.Fatal(err)
			}
			if err := browser.Scroll(t.Context(), -1000); err != nil {
				t.Fatal(err)
			}
			if err := browser.Buttons(t.Context(), 2048, 1024, 0); err != nil {
				t.Fatal(err)
			}
			if err := browser.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			var want [][]byte
			if mode == "absolute" {
				want = [][]byte{ch9329.MouseAbsolute(1, 2048, 1024, 0), ch9329.MouseAbsolute(1, 1, 4095, 0), ch9329.MouseRelative(1, 0, 0, -127), ch9329.MouseAbsolute(0, 2048, 1024, 0)}
			} else {
				// Relative moves intentionally carry held ABS bits, correcting
				// Python's button-free move to match the verified shared Mouse.
				want = [][]byte{ch9329.MouseAbsolute(0, 2048, 1024, 0), ch9329.MouseRelative(1, 0, 0, 0), ch9329.MouseAbsolute(1, 1, 4095, 0), ch9329.MouseRelative(1, 0, 0, -127), ch9329.MouseAbsolute(0, 2048, 1024, 0), ch9329.MouseRelative(0, 0, 0, 0)}
			}
			if got := browserFrames(transport); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s browser reports=%x; want %x", mode, got, want)
			}
		})
	}
}

func TestBrowserValidationBeforeHardware(t *testing.T) {
	browser, transport := browserFixture(t, "relative")
	checks := []func() error{
		func() error { return browser.Move(t.Context(), -1, 0) },
		func() error { return browser.Move(t.Context(), 0, 4096) },
		func() error { return browser.Buttons(t.Context(), 4096, 0, 1) },
		func() error { return browser.Buttons(t.Context(), 0, 0, -1) },
		func() error { return browser.Buttons(t.Context(), 0, 0, 8) },
		func() error { return browser.Shortcut(t.Context(), nil) },
		func() error { return browser.Shortcut(t.Context(), []string{"KeyA", "unknown"}) },
		func() error {
			return browser.Shortcut(t.Context(), []string{"KeyA", "KeyB", "KeyC", "KeyD", "KeyE", "KeyF", "KeyG"})
		},
	}
	for _, check := range checks {
		if err := check(); err == nil {
			t.Fatal("invalid browser input succeeded")
		}
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
		t.Fatal("invalid browser input touched hardware")
	}
}

func TestBrowserPassiveReleaseAndGenerationReset(t *testing.T) {
	browser, transport := browserFixture(t, "relative")
	if err := browser.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
		t.Fatal("passive browser opened/sent input")
	}
	_ = browser.Key(t.Context(), "ControlLeft", true)
	_ = browser.Key(t.Context(), "KeyA", true)
	_ = browser.Buttons(t.Context(), 0, 0, 1)
	transport.generation = 2
	if err := browser.Move(t.Context(), 4095, 0); err != nil {
		t.Fatal(err)
	}
	if browser.modifier != 0 || len(browser.keys) != 0 || browser.buttons != 0 || !bytes.Equal(browserFrames(transport)[4], ch9329.MouseAbsolute(0, 4095, 0, 0)) {
		t.Fatal("Prepare reset did not reconcile per-browser held state")
	}
	prepares, _, _, _ := transport.snapshot()
	_ = browser.Release(t.Context())
	if after, _, _, _ := transport.snapshot(); after != prepares {
		t.Fatal("reset-released browser sent a second cleanup")
	}
	_ = browser.Key(t.Context(), "KeyB", true)
	transport.generation = 3
	if err := browser.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if browser.modifier != 0 || len(browser.keys) != 0 {
		t.Fatal("release did not honor changed preparation")
	}
}

func TestBrowserReleaseOwnsOnlyHeldClassesAndDoesNotRetryFailures(t *testing.T) {
	browser, transport := browserFixture(t, "relative")
	_ = browser.Key(t.Context(), "ShiftLeft", true)
	_ = browser.Buttons(t.Context(), 0, 0, 3)
	prepares, writes, _, _ := transport.snapshot()
	failure := errors.New("release send failed")
	transport.writeHook = func(context.Context, int, []byte) error { return failure }
	if err := browser.Release(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("release lost failure: %v", err)
	}
	afterPrepares, afterWrites, _, _ := transport.snapshot()
	if afterPrepares != prepares+1 || afterWrites != writes+2 || browser.modifier != 0 || browser.buttons != 0 {
		t.Fatalf("release retried/reset its cleanup budget: prepares%d writes%d", afterPrepares, afterWrites)
	}
	_ = browser.Release(t.Context())
	if finalPrepares, finalWrites, _, _ := transport.snapshot(); finalPrepares != afterPrepares || finalWrites != afterWrites {
		t.Fatal("failed release retried on disconnect")
	}
	transport.writeHook = nil
	transport.generation++
	_ = browser.Key(t.Context(), "KeyC", true)
	if err := browser.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = browser.Buttons(t.Context(), 0, 0, 1)
	if err := browser.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserReleaseBoundsAdmissionAndRetainsUnattemptedState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, transport := browserFixture(t, "relative")
		if err := browser.Key(t.Context(), "KeyA", true); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := browser.Release(ctx); !errors.Is(err, context.Canceled) || len(browser.keys) != 1 {
			t.Fatalf("unattempted cancelled release lost owned state: %v", err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		active := make(chan error, 1)
		go func() {
			active <- browser.controller.run(t.Context(), releaseMouse, func(*action) error {
				close(entered)
				<-release
				return nil
			})
		}()
		<-entered
		queued, stop := context.WithTimeout(t.Context(), time.Millisecond)
		defer stop()
		if err := browser.Release(queued); !errors.Is(err, context.DeadlineExceeded) || len(browser.keys) != 1 {
			t.Fatalf("bounded queued release lost owned state: %v", err)
		}
		unblock()
		if err := <-active; err != nil {
			t.Fatal(err)
		}
		transport.writeHook = func(ctx context.Context, _ int, _ []byte) error {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) != time.Second {
				t.Error("release without caller deadline did not use one second")
			}
			return nil
		}
		if err := browser.Release(t.Context()); err != nil || len(browser.keys) != 0 {
			t.Fatalf("later release did not finish: %v", err)
		}
	})
}

func TestBrowserShortcutSharesGateAndRestoresHeldState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, keyboard, transport := keyboardFixture(t, "us104")
		mouse, _ := NewMouse(controller, MouseOptions{Width: 2, Height: 2})
		browser := NewBrowser(controller, mouse)
		shortcut := make(chan error, 1)
		go func() {
			shortcut <- browser.Shortcut(t.Context(), []string{"ControlLeft", "AltLeft", "Delete", "Delete"})
		}()
		synctest.Wait()
		api := make(chan error, 1)
		go func() { api <- keyboard.SendKey(t.Context(), "z", nil) }()
		synctest.Wait()
		if got := browserFrames(transport); len(got) != 1 || !bytes.Equal(got[0], ch9329.Keyboard(5, 0x4c)) {
			t.Fatal("API action interleaved the shortcut hold")
		}
		if err := <-shortcut; err != nil {
			t.Fatal(err)
		}
		if err := <-api; err != nil {
			t.Fatal(err)
		}
		want := [][]byte{ch9329.Keyboard(5, 0x4c), ch9329.Keyboard(0), ch9329.Keyboard(0, 0x1d), ch9329.Keyboard(0)}
		if got := browserFrames(transport); !reflect.DeepEqual(got, want) {
			t.Fatalf("shortcut/API reports=%x; want %x", got, want)
		}
		_ = browser.Key(t.Context(), "ShiftLeft", true)
		_ = browser.Key(t.Context(), "KeyA", true)
		if err := browser.Shortcut(t.Context(), []string{"AltLeft", "Tab", "KeyA"}); err != nil {
			t.Fatal(err)
		}
		frames := browserFrames(transport)
		if !bytes.Equal(frames[len(frames)-2], ch9329.Keyboard(6, 4, 0x2b)) || !bytes.Equal(frames[len(frames)-1], ch9329.Keyboard(2, 4)) {
			t.Fatal("shortcut did not preserve prior held state and order")
		}
	})
}

func TestBrowserShortcutValidatesHeldUnionAfterPrepareReset(t *testing.T) {
	for _, reset := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			browser, transport := browserFixture(t, "relative")
			for _, code := range []string{"KeyA", "KeyB", "KeyC", "KeyD", "KeyE", "KeyF"} {
				if err := browser.Key(t.Context(), code, true); err != nil {
					t.Fatal(err)
				}
			}
			if reset {
				transport.generation++
			}
			err := browser.Shortcut(t.Context(), []string{"Tab"})
			frames := browserFrames(transport)
			if reset {
				if err != nil || len(frames) != 8 || !bytes.Equal(frames[6], ch9329.Keyboard(0, 0x2b)) || !bytes.Equal(frames[7], ch9329.Keyboard(0)) || len(browser.keys) != 0 {
					t.Fatalf("reset held keys incorrectly rejected/changed shortcut: %v frames%x", err, frames)
				}
			} else if !errors.Is(err, ErrKeycode) || len(frames) != 6 || !slices.Equal(browser.keys, []byte{4, 5, 6, 7, 8, 9}) {
				t.Fatalf("oversized held union emitted a half-chord or released held keys: %v frames%x", err, frames)
			}
		})
	}
}

func TestBrowserDirtyFailureCleanupSuppressesDisconnectRetry(t *testing.T) {
	for _, failureAt := range []int{1, 2} {
		t.Run(string(rune('0'+failureAt)), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browser, transport := browserFixture(t, "relative")
				failure := errors.New("failed shortcut write")
				transport.writeHook = func(_ context.Context, n int, _ []byte) error {
					if n == failureAt {
						return failure
					}
					return nil
				}
				if err := browser.Shortcut(t.Context(), []string{"AltLeft", "Tab"}); !errors.Is(err, failure) {
					t.Fatal(err)
				}
				prepares, writes, _, _ := transport.snapshot()
				_ = browser.Release(t.Context())
				if gotPrepares, gotWrites, _, _ := transport.snapshot(); gotPrepares != prepares || gotWrites != writes || browser.modifier != 0 || len(browser.keys) != 0 {
					t.Fatal("dirty failure caused another disconnect cleanup")
				}
			})
		})
	}
	synctest.Test(t, func(t *testing.T) {
		browser, transport := browserFixture(t, "relative")
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- browser.Shortcut(ctx, []string{"AltLeft", "Tab"}) }()
		time.Sleep(5 * time.Millisecond)
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		prepares, writes, _, _ := transport.snapshot()
		_ = browser.Release(context.Background())
		if gotPrepares, gotWrites, _, _ := transport.snapshot(); gotPrepares != prepares || gotWrites != writes {
			t.Fatal("canceled shortcut started a second cleanup")
		}
	})
}

func TestBrowserFailedButtonChangeReleasesBothWithoutReplay(t *testing.T) {
	for _, failedWrite := range []int{1, 2} {
		browser, transport := browserFixture(t, "relative")
		failure := errors.New("button send failed")
		transport.writeHook = func(_ context.Context, n int, _ []byte) error {
			if n == failedWrite {
				return failure
			}
			return nil
		}
		if err := browser.Buttons(t.Context(), 1000, 2000, 1); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		frames := browserFrames(transport)
		if !bytes.Equal(frames[len(frames)-2], ch9329.Keyboard(0)) || !bytes.Equal(frames[len(frames)-1], ch9329.MouseRelative(0, 0, 0, 0)) || browser.buttons != 0 {
			t.Fatal("failed button transition did not reconcile held input")
		}
		prepares, writes, _, _ := transport.snapshot()
		_ = browser.Release(t.Context())
		if gotPrepares, gotWrites, _, _ := transport.snapshot(); gotPrepares != prepares || gotWrites != writes {
			t.Fatal("button failure started another release attempt")
		}
	}
}

func TestBrowserDependencyPanics(t *testing.T) {
	controller, mouse, _ := mouseFixture(t, MouseOptions{Width: 1, Height: 1})
	for _, create := range []func(){func() { NewBrowser(nil, mouse) }, func() { NewBrowser(controller, nil) }, func() { NewBrowser(NewController(&recordingTransport{}), mouse) }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("invalid browser dependencies accepted")
				}
			}()
			create()
		}()
	}
}
