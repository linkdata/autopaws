package hid

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	"github.com/linkdata/autopaws/ch9329"
)

func TestAbsoluteRefreshIsOptIn(t *testing.T) {
	for _, test := range []struct {
		name    string
		refresh bool
		want    [][]byte
	}{
		{"default", false, [][]byte{
			ch9329.MouseAbsolute(0, 100, 200, 0), ch9329.MouseAbsolute(1, 100, 200, 0), ch9329.MouseAbsolute(0, 100, 200, 0),
		}},
		{"enabled", true, [][]byte{
			ch9329.MouseAbsolute(0, 100, 201, 0), ch9329.MouseAbsolute(0, 100, 200, 0),
			ch9329.MouseAbsolute(0, 100, 201, 0), ch9329.MouseAbsolute(0, 100, 200, 0),
			ch9329.MouseAbsolute(1, 100, 200, 0), ch9329.MouseAbsolute(0, 100, 200, 0),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute", AbsoluteRefresh: test.refresh})
			if err := mouse.MoveAbs(t.Context(), 100, 200); err != nil {
				t.Fatal(err)
			}
			if err := mouse.Click(t.Context(), "left", ButtonOptions{Position: &Point{100, 200}}); err != nil {
				t.Fatal(err)
			}
			if got := browserFrames(transport); !slices.EqualFunc(got, test.want, bytes.Equal) {
				t.Fatalf("absolute refresh reports=%x; want %x", got, test.want)
			}
		})
	}
}

func TestAbsoluteMoveRefreshesCachedAxesAfterOtherMotion(t *testing.T) {
	for name, point := range map[string]Point{
		"origin": {}, "near origin": {0, 1}, "top edge": {1, 0}, "far corner": {4095, 4095}, "interior": {1024, 2048},
	} {
		t.Run(name, func(t *testing.T) {
			_, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, AbsoluteRefresh: true})
			var cached, cursor Point
			transport.writeHook = func(_ context.Context, _ int, frame []byte) error {
				switch frame[3] {
				case 4:
					if frame[6] != 0 {
						t.Fatalf("positioning pressed a button: %x", frame)
					}
					reported := Point{int(binary.LittleEndian.Uint16(frame[7:9])), int(binary.LittleEndian.Uint16(frame[9:11]))}
					if point != (Point{}) && reported == (Point{}) {
						t.Fatal("nonzero positioning refreshed through the origin")
					}
					// Model a target that ignores unchanged absolute axes, even
					// when another input source has moved the real cursor.
					if reported != cached {
						cursor, cached = reported, reported
					}
				case 5:
					cursor.X += int(int8(frame[7]))
					cursor.Y += int(int8(frame[8]))
				}
				return nil
			}
			move := func() {
				t.Helper()
				if err := mouse.MoveAbs(t.Context(), point.X, point.Y); err != nil {
					t.Fatal(err)
				}
				if cursor != point {
					t.Fatalf("target cursor=%v, cached axes=%v; want %v", cursor, cached, point)
				}
			}
			move()
			dx, dy := 30, 20
			if point.X == 4095 {
				dx = -dx
			}
			if point.Y == 4095 {
				dy = -dy
			}
			if err := mouse.MoveRel(t.Context(), dx, dy); err != nil {
				t.Fatal(err)
			}
			move()
			cursor = Point{2000, 1000} // A physical mouse bypasses the controller.
			move()
		})
	}
}

func TestPositionedButtonsPositionBeforePress(t *testing.T) {
	for name, point := range map[string]Point{"origin": {}, "near origin": {0, 1}, "interior": {100, 200}} {
		t.Run(name, func(t *testing.T) {
			controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute", AbsoluteRefresh: true})
			var cached, cursor Point
			var held byte
			var down, up []Point
			transport.writeHook = func(_ context.Context, _ int, frame []byte) error {
				switch frame[3] {
				case 4:
					// This target dispatches button edges before changed axes.
					if held == 0 && frame[6] != 0 {
						down = append(down, cursor)
					} else if held != 0 && frame[6] == 0 {
						up = append(up, cursor)
					}
					held = frame[6]
					reported := Point{int(binary.LittleEndian.Uint16(frame[7:9])), int(binary.LittleEndian.Uint16(frame[9:11]))}
					if reported != cached {
						cursor, cached = reported, reported
					}
				case 5:
					cursor.X += int(int8(frame[7]))
					cursor.Y += int(int8(frame[8]))
				}
				return nil
			}
			anchor := Point{1024, 2048}
			if err := mouse.MoveAbs(t.Context(), anchor.X, anchor.Y); err != nil {
				t.Fatal(err)
			}
			if err := mouse.MoveRel(t.Context(), 30, 20); err != nil {
				t.Fatal(err)
			}
			if err := mouse.Click(t.Context(), "left", ButtonOptions{Position: &anchor}); err != nil {
				t.Fatal(err)
			}
			cursor = Point{2000, 1000}
			if err := mouse.Click(t.Context(), "left", ButtonOptions{Position: &anchor}); err != nil {
				t.Fatal(err)
			}
			if err := mouse.Down(t.Context(), "left", ButtonOptions{Position: &point}); err != nil {
				t.Fatal(err)
			}
			before := len(browserFrames(transport))
			if err := mouse.Up(t.Context(), "left", ButtonOptions{Position: &point}); err != nil {
				t.Fatal(err)
			}
			released := browserFrames(transport)[before:]
			if len(released) != 1 || !bytes.Equal(released[0], ch9329.MouseAbsolute(0, point.X, point.Y, 0)) {
				t.Fatalf("button release was not exact: %x", released)
			}
			if err := mouse.Down(t.Context(), "left", ButtonOptions{Position: &point}); err != nil {
				t.Fatal(err)
			}
			if err := controller.Close(); err != nil {
				t.Fatal(err)
			}
			want := []Point{anchor, anchor, point, point}
			if len(down) != len(want) || len(up) != len(want) {
				t.Fatalf("extra/missing button edges: down%v up%v", down, up)
			}
			for index := range want {
				if down[index] != want[index] || up[index] != want[index] {
					t.Fatalf("button edge at stale cursor: down%v up%v; want %v", down, up, want)
				}
			}
		})
	}
}

func TestPositionedPressRetriesPossibleAbsoluteHold(t *testing.T) {
	controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute", AbsoluteRefresh: true})
	failure := errors.New("press and cleanup may have arrived")
	transport.writeHook = func(_ context.Context, n int, _ []byte) error {
		if n >= 3 {
			return failure
		}
		return nil
	}
	point := Point{100, 200}
	if err := mouse.Down(t.Context(), "left", ButtonOptions{Position: &point}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if controller.absoluteButtons != 0 || controller.absolutePending != 1 {
		t.Fatal("test did not retain the possibly delivered press")
	}
	transport.writeHook = nil
	before := len(browserFrames(transport))
	point = Point{300, 400}
	if err := mouse.Down(t.Context(), "left", ButtonOptions{Position: &point}); err != nil {
		t.Fatal(err)
	}
	frames := browserFrames(transport)[before:]
	if len(frames) != 2 || !bytes.Equal(frames[0], ch9329.MouseAbsolute(0, 300, 400, 0)) ||
		!bytes.Equal(frames[1], ch9329.MouseAbsolute(1, 300, 400, 0)) {
		t.Fatalf("retry lost its press or positioned with a possible hold: %x", frames)
	}
	if controller.buttons != 1 || controller.absoluteButtons != 1 || controller.absolutePending != 0 {
		t.Fatal("retried press did not resolve possible ownership")
	}
}

func TestPositionedPressCancellationBeforeButtonEdge(t *testing.T) {
	for _, cancelAt := range []int{1, 2} {
		controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute", AbsoluteRefresh: true})
		ctx, cancel := context.WithCancel(t.Context())
		transport.writeHook = func(_ context.Context, n int, _ []byte) error {
			if n == cancelAt {
				cancel()
			}
			return nil
		}
		err := mouse.Down(ctx, "left", ButtonOptions{Position: &Point{100, 200}})
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		frames := browserFrames(transport)
		if len(frames) != cancelAt+1 || !bytes.Equal(frames[len(frames)-1], ch9329.MouseRelative(0, 0, 0, 0)) {
			t.Fatalf("canceled positioned press cleanup: %x", frames)
		}
		for _, frame := range frames {
			if frame[6] != 0 {
				t.Fatalf("positioning cancellation pressed a button: %x", frames)
			}
		}
		if controller.buttons|controller.absolutePending|controller.relativePending != 0 {
			t.Fatal("positioning cancellation retained a possible hold")
		}
	}
}

func TestAbsoluteRefreshSkipsHeldAndUncertainButtons(t *testing.T) {
	for _, test := range []struct {
		name                                       string
		absolute, relative, absPending, relPending byte
	}{
		{"absolute held", 1, 0, 0, 0},
		{"relative held", 0, 2, 0, 0},
		{"absolute uncertain", 0, 0, 1, 0},
		{"relative uncertain", 0, 0, 0, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, AbsoluteRefresh: true})
			if err := controller.run(t.Context(), releaseMouse, func(current *action) error {
				controller.absoluteButtons, controller.relativeButtons = test.absolute, test.relative
				controller.buttons = test.absolute | test.relative
				controller.absolutePending, controller.relativePending = test.absPending, test.relPending
				return mouse.moveAbs(current, Point{100, 200})
			}); err != nil {
				t.Fatal(err)
			}
			frames := browserFrames(transport)
			if len(frames) != 1 || !bytes.Equal(frames[0], ch9329.MouseAbsolute(test.absolute, 100, 200, 0)) {
				t.Fatalf("held or uncertain positioning was refreshed: %x", frames)
			}
		})
	}
}

func TestAbsoluteRefreshFailureAndCancellation(t *testing.T) {
	for _, name := range []string{"neighbor failure", "exact failure", "cancel after neighbor"} {
		t.Run(name, func(t *testing.T) {
			controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, AbsoluteRefresh: true})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("position report may have arrived")
			wantError, wantFrames := failure, 2
			switch name {
			case "exact failure":
				wantFrames = 3
			case "cancel after neighbor":
				wantError = context.Canceled
			}
			transport.writeHook = func(_ context.Context, n int, _ []byte) error {
				if name == "cancel after neighbor" && n == 1 {
					cancel()
				} else if name == "neighbor failure" && n == 1 || name == "exact failure" && n == 2 {
					return failure
				}
				return nil
			}
			if err := mouse.MoveAbs(ctx, 100, 200); !errors.Is(err, wantError) {
				t.Fatalf("move error=%v; want %v", err, wantError)
			}
			frames := browserFrames(transport)
			if len(frames) != wantFrames || !bytes.Equal(frames[0], ch9329.MouseAbsolute(0, 100, 201, 0)) ||
				!bytes.Equal(frames[len(frames)-1], ch9329.MouseRelative(0, 0, 0, 0)) {
				t.Fatalf("incorrect interrupted refresh/cleanup: %x", frames)
			}
			for _, frame := range frames {
				if frame[6] != 0 {
					t.Fatalf("interrupted positioning pressed a button: %x", frame)
				}
			}
			if controller.buttons|controller.absolutePending|controller.relativePending != 0 {
				t.Fatal("interrupted positioning left a possible hold")
			}
			transport.writeHook = nil
			if err := mouse.MoveRel(t.Context(), 1, 1); err != nil {
				t.Fatalf("interrupted refresh stranded admission: %v", err)
			}
		})
	}
}
