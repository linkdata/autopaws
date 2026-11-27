package hid

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

func mouseFixture(t *testing.T, options MouseOptions) (*Controller, *Mouse, *recordingTransport) {
	t.Helper()
	transport := &recordingTransport{start: time.Now(), generation: 1}
	controller := NewController(transport)
	mouse, err := NewMouse(controller, options)
	if err != nil {
		t.Fatal(err)
	}
	return controller, mouse, transport
}

func TestMouseActionGoldens(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name    string `json:"name"`
			Width   int    `json:"screen_width"`
			Height  int    `json:"screen_height"`
			Mode    string `json:"default_button_mode"`
			Buttons byte   `json:"held_buttons_after"`
			Steps   []struct {
				Method  string            `json:"method"`
				Args    []json.RawMessage `json:"args"`
				Options struct {
					Mode string `json:"button_mode"`
				} `json:"options"`
				Events []struct {
					Kind  string  `json:"kind"`
					Hex   string  `json:"hex"`
					Delay float64 `json:"milliseconds"`
				} `json:"events"`
				Buttons byte `json:"held_buttons_after"`
				Error   *struct {
					Type string `json:"type"`
				} `json:"error"`
			} `json:"steps"`
		} `json:"cases"`
	}
	readJSON(t, "mouse_actions.json", &fixture)
	if len(fixture.Cases) != 44 {
		t.Fatalf("mouse action cases=%d; want 44", len(fixture.Cases))
	}
	for _, vector := range fixture.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				controller, mouse, transport := mouseFixture(t, MouseOptions{Width: vector.Width, Height: vector.Height, ButtonMode: vector.Mode})
				var elapsed time.Duration
				var want []frameEvent
				prepares := 0
				for _, step := range vector.Steps {
					err := applyMouseStep(t, mouse, step.Method, step.Args, step.Options.Mode)
					if step.Error == nil {
						if err != nil {
							t.Fatal(err)
						}
						prepares++
					} else if step.Error.Type != "invalid_button_mode" || !errors.Is(err, ErrButtonMode) {
						t.Fatalf("expected invalid button mode, got %v", err)
					}
					for _, event := range step.Events {
						switch event.Kind {
						case "frame":
							frame, err := hex.DecodeString(event.Hex)
							if err != nil {
								t.Fatal(err)
							}
							want = append(want, frameEvent{Frame: frame, At: elapsed})
						case "sleep":
							elapsed += time.Duration(event.Delay * float64(time.Millisecond))
						default:
							t.Fatalf("unknown event kind %q", event.Kind)
						}
					}
					gotPrepares, _, _, events := transport.snapshot()
					if !reflect.DeepEqual(events, want) || gotPrepares != prepares || controller.buttons != step.Buttons || time.Since(transport.start) != elapsed {
						t.Fatalf("%s: events=%v prepares=%d buttons=%d elapsed=%s; want %v/%d/%d/%s", step.Method, events, gotPrepares, controller.buttons, time.Since(transport.start), want, prepares, step.Buttons, elapsed)
					}
				}
				if controller.buttons != vector.Buttons {
					t.Fatalf("final buttons=%d; want %d", controller.buttons, vector.Buttons)
				}
			})
		})
	}
}

func applyMouseStep(t *testing.T, mouse *Mouse, method string, args []json.RawMessage, mode string) error {
	t.Helper()
	ctx := context.Background()
	switch method {
	case "move_absolute", "move_relative":
		var x, y int
		decodeArgument(t, args[0], &x)
		decodeArgument(t, args[1], &y)
		if method == "move_absolute" {
			return mouse.MoveAbs(ctx, x, y)
		}
		return mouse.MoveRel(ctx, x, y)
	case "scroll":
		var amount int
		decodeArgument(t, args[0], &amount)
		return mouse.Scroll(ctx, amount)
	case "click", "mouse_down", "mouse_up":
		var button string
		decodeArgument(t, args[0], &button)
		options := ButtonOptions{Mode: mode}
		if len(args) == 3 {
			options.Position = &Point{}
			decodeArgument(t, args[1], &options.Position.X)
			decodeArgument(t, args[2], &options.Position.Y)
		}
		switch method {
		case "click":
			return mouse.Click(ctx, button, options)
		case "mouse_down":
			return mouse.Down(ctx, button, options)
		default:
			return mouse.Up(ctx, button, options)
		}
	default:
		t.Fatalf("unknown fixture method %q", method)
		return nil
	}
}

func TestMouseCoordinates(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Width  int    `json:"screen_width"`
			Height int    `json:"screen_height"`
			X      int    `json:"x"`
			Y      int    `json:"y"`
			HID    [2]int `json:"hid"`
			Reject bool   `json:"rejects"`
		} `json:"cases"`
	}
	readJSON(t, "mouse_coordinates.json", &fixture)
	if len(fixture.Cases) != 21 {
		t.Fatalf("coordinate cases=%d; want 21", len(fixture.Cases))
	}
	for _, vector := range fixture.Cases {
		_, mouse, transport := mouseFixture(t, MouseOptions{Width: vector.Width, Height: vector.Height})
		err := mouse.MoveAbs(context.Background(), vector.X, vector.Y)
		if vector.Reject {
			if !errors.Is(err, ErrCoordinates) {
				t.Fatalf("invalid point (%d,%d) accepted: %v", vector.X, vector.Y, err)
			}
			if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
				t.Fatal("invalid coordinates prepared or emitted input")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, events := transport.snapshot()
		last := events[len(events)-1].Frame
		got := [2]int{int(binary.LittleEndian.Uint16(last[7:9])), int(binary.LittleEndian.Uint16(last[9:11]))}
		if got != vector.HID {
			t.Fatalf("%dx%d (%d,%d) maps to %v; want %v", vector.Width, vector.Height, vector.X, vector.Y, got, vector.HID)
		}
	}
	for _, vector := range []struct {
		point Point
		want  Point
	}{
		{Point{0, 1079}, Point{0, 4095}},
		{Point{1919, 0}, Point{4095, 0}},
	} {
		_, mouse, _ := mouseFixture(t, MouseOptions{Width: 1920, Height: 1080})
		if got, err := mouse.position(vector.point); err != nil || got != vector.want {
			t.Fatalf("corner mapping=%v, %v; want %v", got, err, vector.want)
		}
	}
	_, enormous, _ := mouseFixture(t, MouseOptions{Width: math.MaxInt, Height: math.MaxInt})
	for _, vector := range []struct{ pixel, want int }{{0, 0}, {1, 0}, {(math.MaxInt - 1) / 2, 2048}, {math.MaxInt - 1, 4095}} {
		got, err := enormous.position(Point{vector.pixel, vector.pixel})
		if err != nil || got != (Point{vector.want, vector.want}) {
			t.Fatalf("large coordinate %d maps to %v, %v; want %d", vector.pixel, got, err, vector.want)
		}
	}
}

func TestMouseValidationDoesNotChangeHeldState(t *testing.T) {
	controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 10, Height: 20})
	if err := mouse.Down(context.Background(), "left", ButtonOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct {
		function func() error
		want     error
	}{
		{func() error { return mouse.Click(context.Background(), "unknown", ButtonOptions{}) }, ErrButton},
		{func() error { return mouse.Down(context.Background(), "unknown", ButtonOptions{}) }, ErrButton},
		{func() error { return mouse.Up(context.Background(), "unknown", ButtonOptions{}) }, ErrButton},
		{func() error { return mouse.Down(context.Background(), "left", ButtonOptions{Position: &Point{-1, 0}}) }, ErrCoordinates},
		{func() error { return mouse.Up(context.Background(), "left", ButtonOptions{Position: &Point{0, 20}}) }, ErrCoordinates},
		{func() error { return mouse.Click(context.Background(), "left", ButtonOptions{Mode: "unknown"}) }, ErrButtonMode},
		{func() error { return mouse.MoveAbs(context.Background(), 10, 0) }, ErrCoordinates},
		{func() error { return mouse.MoveRel(context.Background(), -128, 0) }, ErrMovement},
		{func() error { return mouse.MoveRel(context.Background(), 128, 0) }, ErrMovement},
		{func() error { return mouse.MoveRel(context.Background(), 0, -128) }, ErrMovement},
		{func() error { return mouse.MoveRel(context.Background(), 0, 128) }, ErrMovement},
		{func() error { return mouse.Drag(context.Background(), Point{}, Point{}, "unknown", "") }, ErrButton},
		{func() error { return mouse.Drag(context.Background(), Point{}, Point{10, 0}, "left", "") }, ErrCoordinates},
	} {
		if err := call.function(); !errors.Is(err, call.want) {
			t.Fatalf("invalid mouse input returned %v; want %v", err, call.want)
		}
		if prepares, writes, _, _ := transport.snapshot(); prepares != 1 || writes != 1 || controller.buttons != 1 {
			t.Fatalf("invalid input changed held state or emitted input: %d/%d buttons=%d", prepares, writes, controller.buttons)
		}
	}
	for _, options := range []MouseOptions{{Width: 0, Height: 1}, {Width: 1, Height: -1}, {Width: math.MinInt, Height: 1}} {
		if _, err := NewMouse(controller, options); !errors.Is(err, ErrScreenSize) {
			t.Fatalf("invalid dimensions accepted: %v", err)
		}
	}
	if _, err := NewMouse(controller, MouseOptions{Width: 1, Height: 1, ButtonMode: "unknown"}); !errors.Is(err, ErrButtonMode) {
		t.Fatalf("invalid mode accepted: %v", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("nil controller did not panic")
			}
		}()
		_, _ = NewMouse(nil, MouseOptions{Width: 1, Height: 1})
	}()
}

func TestMouseClickPreservesOtherButtons(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
		if err := mouse.Down(context.Background(), "right", ButtonOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := mouse.Click(context.Background(), "", ButtonOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := mouse.Down(context.Background(), "left", ButtonOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := mouse.Click(context.Background(), "left", ButtonOptions{}); err != nil {
			t.Fatal(err)
		}
		for _, amount := range []int{math.MinInt, math.MaxInt} {
			if err := mouse.Scroll(context.Background(), amount); err != nil {
				t.Fatal(err)
			}
		}
		_, _, _, events := transport.snapshot()
		wantMasks := []byte{2, 3, 2, 3, 3, 2, 2, 2}
		for i, want := range wantMasks {
			if events[i].Frame[6] != want {
				t.Fatalf("button mask %d=%d; want %d", i, events[i].Frame[6], want)
			}
		}
		if events[6].Frame[9] != 0x81 || events[7].Frame[9] != 0x7f || controller.buttons != 2 {
			t.Fatalf("scroll clamp or held state incorrect: %v buttons=%d", events[6:], controller.buttons)
		}
	})
}

func TestMouseCompositeActionsDoNotInterleaveKeyboard(t *testing.T) {
	for _, operation := range []string{"click", "drag"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
				keyboard := NewKeyboard(controller, mustLoad(t, "us104", ""))
				mouseDone := make(chan error, 1)
				go func() {
					if operation == "click" {
						mouseDone <- mouse.Click(context.Background(), "left", ButtonOptions{Position: &Point{10, 20}})
					} else {
						mouseDone <- mouse.Drag(context.Background(), Point{10, 20}, Point{100, 200}, "left", "")
					}
				}()
				synctest.Wait()
				keyDone := make(chan error, 1)
				go func() { keyDone <- keyboard.SendKey(context.Background(), "a", nil) }()
				if err := <-mouseDone; err != nil {
					t.Fatal(err)
				}
				if err := <-keyDone; err != nil {
					t.Fatal(err)
				}
				_, _, _, events := transport.snapshot()
				mouseDuration := 10 * time.Millisecond
				if operation == "drag" {
					mouseDuration = 100 * time.Millisecond
				}
				if !bytes.Equal(events[len(events)-2].Frame, ch9329.Keyboard(0, 4)) || events[len(events)-2].At != mouseDuration ||
					time.Since(transport.start) != mouseDuration+10*time.Millisecond {
					t.Fatalf("keyboard interleaved with %s: %v", operation, events)
				}
			})
		})
	}
}

func TestMouseDragModesAndTiming(t *testing.T) {
	for _, mode := range []string{"absolute", "relative"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
				if err := mouse.Drag(context.Background(), Point{10, 20}, Point{100, 200}, "", mode); err != nil {
					t.Fatal(err)
				}
				want := []frameEvent{
					{ch9329.MouseAbsolute(1, 10, 20, 0), 0},
					{ch9329.MouseAbsolute(1, 100, 200, 0), 50 * time.Millisecond},
					{ch9329.MouseAbsolute(0, 100, 200, 0), 100 * time.Millisecond},
				}
				if mode == "relative" {
					want = []frameEvent{
						{ch9329.MouseAbsolute(0, 10, 20, 0), 0},
						{ch9329.MouseRelative(1, 0, 0, 0), 0},
						{ch9329.MouseAbsolute(0, 100, 200, 0), 50 * time.Millisecond},
						{ch9329.MouseAbsolute(0, 100, 200, 0), 100 * time.Millisecond},
						{ch9329.MouseRelative(0, 0, 0, 0), 100 * time.Millisecond},
					}
				}
				_, _, _, events := transport.snapshot()
				if !reflect.DeepEqual(events, want) || controller.buttons != 0 || time.Since(transport.start) != 100*time.Millisecond {
					t.Fatalf("drag events=%v buttons=%d elapsed=%s; want %v", events, controller.buttons, time.Since(transport.start), want)
				}
			})
		})
	}
}

func TestMouseDefaultRelativeAndExplicitAbsoluteReports(t *testing.T) {
	for _, vector := range []struct {
		name, configured, action string
		relative                 bool
	}{
		{"empty defaults", "", "", true},
		{"configured absolute", "absolute", "", false},
		{"action absolute", "", "absolute", false},
		{"action relative", "absolute", "relative", true},
	} {
		t.Run(vector.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 10, Height: 20, ButtonMode: vector.configured})
				if err := mouse.Click(t.Context(), "left", ButtonOptions{Position: &Point{9, 19}, Mode: vector.action}); err != nil {
					t.Fatal(err)
				}
				want := []frameEvent{
					{ch9329.MouseAbsolute(1, 4095, 4095, 0), 0},
					{ch9329.MouseAbsolute(0, 4095, 4095, 0), 10 * time.Millisecond},
				}
				if vector.relative {
					want = []frameEvent{
						{ch9329.MouseAbsolute(0, 4095, 4095, 0), 0},
						{ch9329.MouseRelative(1, 0, 0, 0), 0},
						{ch9329.MouseAbsolute(0, 4095, 4095, 0), 10 * time.Millisecond},
						{ch9329.MouseRelative(0, 0, 0, 0), 10 * time.Millisecond},
					}
				}
				_, _, _, got := transport.snapshot()
				if !reflect.DeepEqual(got, want) || controller.buttons != 0 {
					t.Fatalf("click reports = %v, held buttons %d; want %v, released", got, controller.buttons, want)
				}
			})
		})
	}
}

func TestMouseTransportFailuresReleaseMouseOnly(t *testing.T) {
	cases := []struct {
		name     string
		failAt   int
		releases int
		call     func(*Mouse) error
	}{
		{"move", 1, 1, func(mouse *Mouse) error { return mouse.MoveAbs(context.Background(), 1, 2) }},
		{"relative move", 1, 1, func(mouse *Mouse) error { return mouse.MoveRel(context.Background(), 1, 2) }},
		{"scroll", 1, 1, func(mouse *Mouse) error { return mouse.Scroll(context.Background(), 1) }},
		{"relative positioned down", 2, 1, func(mouse *Mouse) error {
			return mouse.Down(context.Background(), "left", ButtonOptions{Position: &Point{1, 2}, Mode: "relative"})
		}},
		{"up", 1, 1, func(mouse *Mouse) error { return mouse.Up(context.Background(), "left", ButtonOptions{}) }},
		{"click down", 1, 2, func(mouse *Mouse) error {
			return mouse.Click(context.Background(), "left", ButtonOptions{Position: &Point{1, 2}})
		}},
		{"click up", 2, 1, func(mouse *Mouse) error { return mouse.Click(context.Background(), "left", ButtonOptions{}) }},
		{"drag down", 2, 2, func(mouse *Mouse) error {
			return mouse.Drag(context.Background(), Point{}, Point{1, 2}, "left", "")
		}},
		{"drag move", 3, 2, func(mouse *Mouse) error {
			return mouse.Drag(context.Background(), Point{}, Point{1, 2}, "left", "")
		}},
		{"drag up", 4, 2, func(mouse *Mouse) error {
			return mouse.Drag(context.Background(), Point{}, Point{1, 2}, "left", "")
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 10, Height: 20, ButtonMode: "absolute"})
				failure := errors.New("send failed")
				transport.writeHook = func(_ context.Context, n int, _ []byte) error {
					if n == test.failAt {
						return failure
					}
					return nil
				}
				if err := test.call(mouse); !errors.Is(err, failure) {
					t.Fatalf("lost send failure: %v", err)
				}
				prepares, writes, _, events := transport.snapshot()
				if prepares != 2 || writes != test.failAt+test.releases || controller.buttons != 0 || !bytes.Equal(events[writes-1].Frame, ch9329.MouseRelative(0, 0, 0, 0)) {
					t.Fatalf("mouse release failed or emitted keyboard input: %d/%d buttons=%d %v", prepares, writes, controller.buttons, events)
				}
				if err := mouse.MoveRel(context.Background(), 0, 0); err != nil {
					t.Fatalf("error stranded action gate: %v", err)
				}
			})
		})
	}
}

func TestMouseCancellationDuringCompositeWaits(t *testing.T) {
	for _, cancelAt := range []time.Duration{5 * time.Millisecond, 25 * time.Millisecond, 75 * time.Millisecond} {
		t.Run(cancelAt.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 10, Height: 20})
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() {
					if cancelAt == 5*time.Millisecond {
						done <- mouse.Click(ctx, "left", ButtonOptions{})
					} else {
						done <- mouse.Drag(ctx, Point{}, Point{1, 2}, "left", "")
					}
				}()
				time.Sleep(cancelAt)
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				_, writes, _, events := transport.snapshot()
				if !bytes.Equal(events[writes-1].Frame, ch9329.MouseRelative(0, 0, 0, 0)) || controller.buttons != 0 || events[writes-1].At != cancelAt {
					t.Fatalf("cancelled action did not release immediately: %v buttons=%d", events, controller.buttons)
				}
			})
		})
	}
}

func TestMouseCleanupPreservesHeldKeyboard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 10, Height: 20})
		if err := controller.run(context.Background(), releaseKeyboard, func(current *action) error {
			return current.write(ch9329.Keyboard(2, 4))
		}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- mouse.Click(ctx, "left", ButtonOptions{}) }()
		time.Sleep(5 * time.Millisecond)
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		_, _, _, events := transport.snapshot()
		if len(events) != 3 || !bytes.Equal(events[1].Frame, ch9329.MouseRelative(1, 0, 0, 0)) ||
			!bytes.Equal(events[2].Frame, ch9329.MouseRelative(0, 0, 0, 0)) {
			t.Fatalf("mouse-only cleanup released keyboard state: %v", events)
		}
	})
}

func TestMouseCancelledSendResetsHeldButtonsWithoutReplay(t *testing.T) {
	controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 10, Height: 20})
	if err := mouse.Down(context.Background(), "right", ButtonOptions{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport.prepareHook = func(_ context.Context, n int) (uint64, error) {
		if n < 3 {
			return 1, nil
		}
		return 2, nil
	}
	transport.writeHook = func(ctx context.Context, n int, frame []byte) error {
		if n == 2 {
			if controller.buttons != 2 || frame[6] != 3 {
				t.Errorf("failed click did not begin from the held right button: buttons=%d frame=%x", controller.buttons, frame)
			}
			cancel()
			return ctx.Err()
		}
		return nil
	}
	if err := mouse.Click(ctx, "left", ButtonOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 3 || writes != 2 || controller.buttons != 0 || controller.generation != 2 {
		t.Fatalf("mouse cleanup did not reset without replay: %d/%d buttons=%d generation=%d", prepares, writes, controller.buttons, controller.generation)
	}
	if err := mouse.MoveRel(context.Background(), 1, 2); err != nil {
		t.Fatal(err)
	}
	_, _, _, events := transport.snapshot()
	if !bytes.Equal(events[2].Frame, ch9329.MouseRelative(0, 1, 2, 0)) {
		t.Fatalf("next move revived old held buttons: %x", events[2].Frame)
	}
}

func TestMouseCleanupBudgetSharedWithClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 10, Height: 20})
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
		go func() { done <- mouse.Click(ctx, "left", ButtonOptions{}) }()
		time.Sleep(5 * time.Millisecond)
		cancel()
		<-entered
		if err := controller.Close(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lost cancellation/cleanup error: %v", err)
		}
		_, _, _, events := transport.snapshot()
		if len(deadlines) != 2 || deadlines[0] != deadlines[1] || time.Since(transport.start) != time.Second+5*time.Millisecond ||
			len(events) != 3 || events[1].Frame[3] != 5 || events[2].Frame[3] != 2 {
			t.Fatalf("Close did not share mouse cleanup's remaining budget: %v %v", deadlines, events)
		}
	})
}

func TestMousePositionCopiedBeforeAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute"})
		keyboard := NewKeyboard(controller, mustLoad(t, "us104", ""))
		keyDone := make(chan error, 1)
		go func() { keyDone <- keyboard.SendKey(context.Background(), "a", nil) }()
		synctest.Wait()
		point := Point{1, 2}
		mouseDone := make(chan error, 1)
		go func() { mouseDone <- mouse.Down(context.Background(), "left", ButtonOptions{Position: &point}) }()
		synctest.Wait()
		point = Point{3, 4}
		if err := <-keyDone; err != nil {
			t.Fatal(err)
		}
		if err := <-mouseDone; err != nil {
			t.Fatal(err)
		}
		_, _, _, events := transport.snapshot()
		if !bytes.Equal(events[2].Frame, ch9329.MouseAbsolute(1, 1, 2, 0)) {
			t.Fatalf("queued position was not snapshotted: %v", events[2])
		}
	})
}
