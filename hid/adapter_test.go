package hid

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

type adapterTransport struct {
	*recordingTransport
	get func(context.Context) (ch9329.USBMode, error)
	set func(context.Context, byte) (ch9329.USBMode, error)
}

func (transport *adapterTransport) GetUSBMode(ctx context.Context) (ch9329.USBMode, error) {
	return transport.get(ctx)
}

func (transport *adapterTransport) SetUSBMode(ctx context.Context, raw byte) (ch9329.USBMode, error) {
	return transport.set(ctx, raw)
}

func TestAdapterUnsupportedAndInvalidModeDoNotPrepare(t *testing.T) {
	controller, _, transport := mouseFixture(t, MouseOptions{Width: 10, Height: 10})
	if _, err := controller.GetUSBMode(t.Context()); !errors.Is(err, ErrAdapterUSBMode) {
		t.Fatal(err)
	}
	if _, err := controller.SetUSBMode(t.Context(), 2); !errors.Is(err, ErrAdapterUSBMode) {
		t.Fatal(err)
	}
	if _, err := controller.SetUSBMode(t.Context(), 1); !errors.Is(err, ch9329.ErrUSBMode) {
		t.Fatal(err)
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
		t.Fatalf("unsupported request touched hardware: prepares=%d writes=%d", prepares, writes)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterModeChangeReleasesAndSerializesInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &adapterTransport{recordingTransport: &recordingTransport{start: time.Now(), generation: 1}}
		controller := NewController(transport)
		mouse, err := NewMouse(controller, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute", AbsoluteRefresh: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := mouse.Down(t.Context(), "left", ButtonOptions{Position: &Point{100, 200}}); err != nil {
			t.Fatal(err)
		}
		before := len(browserFrames(transport.recordingTransport))
		entered, finish := make(chan struct{}), make(chan struct{})
		transport.set = func(ctx context.Context, raw byte) (ch9329.USBMode, error) {
			frames := browserFrames(transport.recordingTransport)[before:]
			want := [][]byte{ch9329.Keyboard(0), ch9329.MouseAbsolute(0, 100, 200, 0), ch9329.MouseRelative(0, 0, 0, 0)}
			if raw != 2 || !slices.EqualFunc(frames, want, bytes.Equal) || controller.buttons != 0 {
				t.Errorf("configuration started before input release: raw=%d frames=%x buttons=%d", raw, frames, controller.buttons)
			}
			close(entered)
			<-finish
			transport.mu.Lock()
			transport.generation++
			transport.mu.Unlock()
			return ch9329.USBMode{RawMode: raw, Mode: raw}, ctx.Err()
		}
		changed := make(chan error, 1)
		go func() { _, err := controller.SetUSBMode(t.Context(), 2); changed <- err }()
		<-entered
		moved := make(chan error, 1)
		go func() { moved <- mouse.MoveRel(t.Context(), 7, 8) }()
		synctest.Wait()
		if len(browserFrames(transport.recordingTransport)) != before+3 {
			t.Fatal("input interleaved adapter configuration")
		}
		close(finish)
		if err := <-changed; err != nil {
			t.Fatal(err)
		}
		if err := <-moved; err != nil {
			t.Fatal(err)
		}
		frames := browserFrames(transport.recordingTransport)
		if !bytes.Equal(frames[len(frames)-1], ch9329.MouseRelative(0, 7, 8, 0)) || controller.generation != 2 {
			t.Fatalf("fresh input retained a hold or missed reset: %x generation=%d", frames, controller.generation)
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAdapterReadPreservesHoldAndCanceledRequestDoesNotProbe(t *testing.T) {
	transport := &adapterTransport{recordingTransport: &recordingTransport{start: time.Now(), generation: 1}}
	controller := NewController(transport)
	mouse, err := NewMouse(controller, MouseOptions{Width: 10, Height: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := mouse.Down(t.Context(), "left", ButtonOptions{}); err != nil {
		t.Fatal(err)
	}
	before := len(browserFrames(transport.recordingTransport))
	calls := 0
	transport.get = func(ctx context.Context) (ch9329.USBMode, error) {
		calls++
		return ch9329.USBMode{RawMode: 0x80, Source: "hardware_pins"}, ctx.Err()
	}
	mode, err := controller.GetUSBMode(t.Context())
	if err != nil || mode.RawMode != 0x80 || controller.buttons != 1 || len(browserFrames(transport.recordingTransport)) != before {
		t.Fatalf("read changed held input: mode=%+v error=%v buttons=%d", mode, err, controller.buttons)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := controller.GetUSBMode(ctx); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("canceled request probed adapter: calls=%d error=%v", calls, err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterReleaseFailurePreventsModeWrite(t *testing.T) {
	transport := &adapterTransport{recordingTransport: &recordingTransport{start: time.Now(), generation: 1}}
	controller := NewController(transport)
	failure := errors.New("release failed")
	transport.writeHook = func(context.Context, int, []byte) error { return failure }
	transport.set = func(context.Context, byte) (ch9329.USBMode, error) {
		t.Error("adapter mode written after failed release")
		return ch9329.USBMode{}, nil
	}
	if _, err := controller.SetUSBMode(t.Context(), 2); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	transport.writeHook = nil
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterQueryFailureReleasesHeldInput(t *testing.T) {
	transport := &adapterTransport{recordingTransport: &recordingTransport{start: time.Now(), generation: 1}}
	controller := NewController(transport)
	mouse, err := NewMouse(controller, MouseOptions{Width: 10, Height: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := mouse.Down(t.Context(), "left", ButtonOptions{}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("adapter query failed")
	transport.get = func(context.Context) (ch9329.USBMode, error) { return ch9329.USBMode{}, failure }
	before := len(browserFrames(transport.recordingTransport))
	if _, err := controller.GetUSBMode(t.Context()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	frames := browserFrames(transport.recordingTransport)[before:]
	want := [][]byte{ch9329.Keyboard(0), ch9329.MouseRelative(0, 0, 0, 0)}
	if !slices.EqualFunc(frames, want, bytes.Equal) || controller.buttons != 0 {
		t.Fatalf("query failure left input held: frames=%x buttons=%d", frames, controller.buttons)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
}
