//go:build (linux || darwin) && (amd64 || arm64)

package serial

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/hid"
)

func TestHIDCancelsDuringSerialPacingAndResetsWithoutLaterInput(t *testing.T) {
	for _, kind := range []string{"keyboard", "mouse"} {
		t.Run(kind, func(t *testing.T) {
			testHIDPacingCleanup(t, kind)
		})
	}
}

func testHIDPacingCleanup(t *testing.T, kind string) {
	for _, deadline := range []bool{false, true} {
		name := "cancellation"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pressed, recovered := &fakeDevice{}, &fakeDevice{}
				port, paths := fakePort(t, Options{}, pressed, recovered)
				controller := hid.NewController(port)
				layout, err := hid.LoadLayout("us104", "")
				if err != nil {
					t.Fatal(err)
				}
				keyboard := hid.NewKeyboard(controller, layout)
				mouse, err := hid.NewMouse(controller, hid.MouseOptions{Width: 1920, Height: 1080, ButtonMode: "relative"})
				if err != nil {
					t.Fatal(err)
				}
				reset := ch9329.ReleaseAll()
				press := ch9329.Keyboard(0, 4)
				if kind == "mouse" {
					press = ch9329.MouseRelative(1, 0, 0, 0)
				}
				pressWritten := false
				queued := make(chan struct{})
				pressed.write = func(frame []byte) (int, error) {
					pressWritten = bytes.Equal(frame, press)
					return len(frame), nil
				}
				pressed.queue = func(context.Context) (int, error) {
					if pressWritten {
						close(queued)
					}
					return 0, nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				want := error(context.Canceled)
				if deadline {
					cancel()
					// Expire after the two initial releases and the complete
					// press write, halfway through the press's UART guard.
					delay := guard(len(reset[0]), port.opts.Baud) + guard(len(reset[1]), port.opts.Baud) + guard(len(press), port.opts.Baud)/2
					ctx, cancel = context.WithTimeout(context.Background(), delay)
					want = context.DeadlineExceeded
				}
				defer cancel()
				result := make(chan error, 1)
				go func() {
					if kind == "mouse" {
						result <- mouse.Click(ctx, "left", hid.ButtonOptions{})
					} else {
						result <- keyboard.SendKey(ctx, "a", nil)
					}
				}()
				<-queued
				synctest.Wait()
				if !deadline {
					cancel()
				}
				if err := <-result; !errors.Is(err, want) {
					t.Fatalf("%s action = %v; want %v", kind, err, want)
				}
				// No subsequent normal action is allowed to cause this
				// recovery: the interrupted action must do its own cleanup.
				requireFrames(t, pressed, reset[0], reset[1], press)
				requireFrames(t, recovered, reset[0], reset[1])
				if len(*paths) != 2 {
					t.Fatalf("opened %v; want initial device and cleanup recovery", *paths)
				}
				if _, closes := pressed.snapshot(); closes != 1 {
					t.Fatalf("invalidated device closed %d times", closes)
				}
				if err := controller.Close(); err != nil {
					t.Fatal(err)
				}
				if _, closes := recovered.snapshot(); closes != 1 {
					t.Fatalf("cleanup device closed %d times", closes)
				}
			})
		})
	}
}
