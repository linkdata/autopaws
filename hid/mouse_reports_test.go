package hid

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

// Count transitions independently for the two report IDs, as distinct pointer
// collections do. Repeating a held mask on the other ID creates an extra edge.
func mouseReportEdges(frames [][]byte) (down, up [3]int) {
	var previous [2]byte
	for _, frame := range frames {
		if len(frame) < 7 || frame[3] != 4 && frame[3] != 5 {
			continue
		}
		index := int(frame[3] - 4)
		mask := frame[6]
		for bit := range 3 {
			value := byte(1 << bit)
			if previous[index]&value == 0 && mask&value != 0 {
				down[bit]++
			}
			if previous[index]&value != 0 && mask&value == 0 {
				up[bit]++
			}
		}
		previous[index] = mask
	}
	return
}

func TestDefaultBrowserDragHasOneButtonEdgePair(t *testing.T) {
	browser, transport := browserFixture(t, "relative")
	if err := browser.Buttons(t.Context(), 100, 200, 1); err != nil {
		t.Fatal(err)
	}
	for _, point := range []Point{{200, 300}, {300, 400}, {400, 500}} {
		if err := browser.Move(t.Context(), point.X, point.Y); err != nil {
			t.Fatal(err)
		}
	}
	if err := browser.Scroll(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if err := browser.Buttons(t.Context(), 400, 500, 0); err != nil {
		t.Fatal(err)
	}
	frames := browserFrames(transport)
	if down, up := mouseReportEdges(frames); down != ([3]int{1, 0, 0}) || up != ([3]int{1, 0, 0}) {
		t.Fatalf("duplicated button edges: down%v up%v", down, up)
	}
	for _, frame := range frames {
		if frame[3] == 4 && frame[6] != 0 {
			t.Fatalf("relative hold copied into absolute report: %x", frame)
		}
	}
}

func TestMouseButtonOwnersSurviveModeOverrides(t *testing.T) {
	controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
	if err := mouse.Down(t.Context(), "left", ButtonOptions{Position: &Point{100, 200}, Mode: "absolute"}); err != nil {
		t.Fatal(err)
	}
	if err := mouse.Down(t.Context(), "right", ButtonOptions{}); err != nil {
		t.Fatal(err)
	}
	if controller.absoluteButtons != 1 || controller.relativeButtons != 2 || controller.buttons != 3 {
		t.Fatal("new press migrated existing owner")
	}
	if err := mouse.MoveAbs(t.Context(), 300, 400); err != nil {
		t.Fatal(err)
	}
	if err := mouse.MoveRel(t.Context(), 3, 4); err != nil {
		t.Fatal(err)
	}
	if err := mouse.Scroll(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if err := mouse.Up(t.Context(), "left", ButtonOptions{Mode: "relative"}); err != nil {
		t.Fatal(err)
	}
	if err := mouse.Up(t.Context(), "right", ButtonOptions{Mode: "absolute"}); err != nil {
		t.Fatal(err)
	}
	frames := browserFrames(transport)
	if frames[2][6] != 1 || frames[3][6] != 2 || frames[4][6] != 2 {
		t.Fatal("movement or wheel copied another report's mask")
	}
	// An unpositioned absolute-owner release reuses the last absolute location;
	// relative/external motion cannot be converted back into normalized pixels.
	if !bytes.Equal(frames[5], ch9329.MouseAbsolute(0, 300, 400, 0)) {
		t.Fatalf("absolute release position=%x", frames[5])
	}
	if down, up := mouseReportEdges(frames); down != ([3]int{1, 1, 0}) || up != ([3]int{1, 1, 0}) {
		t.Fatalf("wrong owner released: down%v up%v", down, up)
	}
	if controller.buttons|controller.absoluteButtons|controller.relativeButtons != 0 {
		t.Fatal("owned button remained held")
	}
}

func TestMouseCleanupReleasesPossiblyDeliveredAbsolutePress(t *testing.T) {
	for _, releaseFails := range []bool{false, true} {
		controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute"})
		failure := errors.New("write may have reached the chip")
		failAt := 1
		if releaseFails {
			if err := mouse.Down(t.Context(), "left", ButtonOptions{Position: &Point{100, 200}}); err != nil {
				t.Fatal(err)
			}
			failAt = 2
		}
		transport.writeHook = func(_ context.Context, n int, _ []byte) error {
			if n == failAt {
				return failure
			}
			return nil
		}
		var err error
		if releaseFails {
			err = mouse.Up(t.Context(), "left", ButtonOptions{})
		} else {
			err = mouse.Down(t.Context(), "left", ButtonOptions{Position: &Point{100, 200}})
		}
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
		frames := browserFrames(transport)
		if !bytes.Equal(frames[len(frames)-2], ch9329.MouseAbsolute(0, 100, 200, 0)) || !bytes.Equal(frames[len(frames)-1], ch9329.MouseRelative(0, 0, 0, 0)) {
			t.Fatalf("absolute failure cleanup=%x", frames)
		}
		if controller.buttons|controller.absolutePending|controller.relativePending != 0 {
			t.Fatal("possible hold survived successful cleanup")
		}
	}
}

func TestPrepareReleasesAbsoluteOwnerBeforeNewGenerationInput(t *testing.T) {
	controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute"})
	if err := mouse.Down(t.Context(), "left", ButtonOptions{Position: &Point{100, 200}}); err != nil {
		t.Fatal(err)
	}
	transport.generation++
	failure := errors.New("absolute recovery release failed")
	transport.writeHook = func(_ context.Context, n int, _ []byte) error {
		if n == 2 {
			return failure
		}
		return nil
	}
	if err := mouse.MoveAbs(t.Context(), 300, 400); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if controller.generation != 1 {
		t.Fatal("generation admitted before absolute reset completed")
	}
	transport.writeHook = nil
	if err := mouse.MoveAbs(t.Context(), 300, 400); err != nil {
		t.Fatal(err)
	}
	frames := browserFrames(transport)
	if len(frames) != 4 || !bytes.Equal(frames[2], ch9329.MouseAbsolute(0, 100, 200, 0)) || !bytes.Equal(frames[3], ch9329.MouseAbsolute(0, 300, 400, 0)) {
		t.Fatalf("recovery replayed held buttons: %x", frames)
	}
	if controller.generation != 2 || controller.buttons != 0 {
		t.Fatal("absolute reset did not finish")
	}
}

func TestMouseAbsoluteCancellationAndCloseReleaseOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute"})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- mouse.Drag(ctx, Point{100, 200}, Point{300, 400}, "left", "") }()
		time.Sleep(25 * time.Millisecond)
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		if down, up := mouseReportEdges(browserFrames(transport)); down[0] != 1 || up[0] != 1 {
			t.Fatalf("cancel/close duplicated edges: %v %v", down, up)
		}
	})
}

func TestAbsoluteCleanupAndCloseShareOneReportBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute"})
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
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- mouse.Click(ctx, "left", ButtonOptions{Position: &Point{100, 200}}) }()
		time.Sleep(5 * time.Millisecond)
		cancel()
		<-entered
		if err := controller.Close(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if len(deadlines) != 2 || deadlines[0] != deadlines[1] || time.Since(transport.start) != time.Second+5*time.Millisecond {
			t.Fatalf("cleanup replayed or renewed budget: %v", deadlines)
		}
		frames := browserFrames(transport)
		if len(frames) != 3 || frames[1][3] != 4 || frames[2][3] != 2 {
			t.Fatalf("cleanup did not attempt each report once: %x", frames)
		}
	})
}

func TestOriginCancellationAfterPrepositionNeverPressesNewButton(t *testing.T) {
	controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	transport.writeHook = func(_ context.Context, n int, _ []byte) error {
		if n == 1 {
			cancel()
		}
		return nil
	}
	if err := mouse.Click(ctx, "left", ButtonOptions{Position: &Point{0, 0}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	frames := browserFrames(transport)
	if len(frames) != 2 || !bytes.Equal(frames[0], ch9329.MouseAbsolute(0, 0, 1, 0)) || !bytes.Equal(frames[1], ch9329.MouseRelative(0, 0, 0, 0)) {
		t.Fatalf("canceled origin press reached transport: %x", frames)
	}
	if controller.buttons|controller.absolutePending|controller.relativePending != 0 {
		t.Fatal("canceled press left a possible hold")
	}
}

func TestOriginPrepositionPreservesOnlyExistingAbsoluteButtons(t *testing.T) {
	_, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute"})
	if err := mouse.Down(t.Context(), "left", ButtonOptions{Position: &Point{100, 200}}); err != nil {
		t.Fatal(err)
	}
	if err := mouse.Down(t.Context(), "right", ButtonOptions{Position: &Point{0, 0}}); err != nil {
		t.Fatal(err)
	}
	frames := browserFrames(transport)
	if len(frames) != 3 || !bytes.Equal(frames[1], ch9329.MouseAbsolute(1, 0, 1, 0)) || !bytes.Equal(frames[2], ch9329.MouseAbsolute(3, 0, 0, 0)) {
		t.Fatalf("origin preposition pressed new button: %x", frames)
	}
}

func TestOriginFailedReleaseCleanupDoesNotRepressUncertainButton(t *testing.T) {
	_, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute"})
	if err := mouse.Down(t.Context(), "left", ButtonOptions{Position: &Point{100, 200}}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("origin release may have arrived")
	transport.writeHook = func(_ context.Context, n int, _ []byte) error {
		if n == 3 {
			return failure
		}
		return nil
	}
	if err := mouse.Up(t.Context(), "left", ButtonOptions{Position: &Point{0, 0}}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	frames := browserFrames(transport)
	if down, up := mouseReportEdges(frames); down[0] != 1 || up[0] != 1 {
		t.Fatalf("failed release repressed button: down%v up%v packets%x", down, up, frames)
	}
	for _, frame := range frames[3:] {
		if frame[6] != 0 {
			t.Fatalf("cleanup reasserted uncertain hold: %x", frame)
		}
	}
}

func TestOriginCleanupAndGenerationResetNeverReassertButtons(t *testing.T) {
	for _, generationChange := range []bool{false, true} {
		for _, pending := range []bool{false, true} {
			controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, ButtonMode: "absolute"})
			failure := errors.New("origin press/release may have arrived")
			if pending {
				transport.writeHook = func(_ context.Context, n int, _ []byte) error {
					if n >= 2 {
						return failure
					}
					return nil
				}
			}
			err := mouse.Down(t.Context(), "left", ButtonOptions{Position: &Point{0, 0}})
			if pending && !errors.Is(err, failure) || !pending && err != nil {
				t.Fatal(err)
			}
			transport.writeHook = nil
			before := len(browserFrames(transport))
			if generationChange {
				transport.generation++
				if err := mouse.MoveAbs(t.Context(), 100, 200); err != nil {
					t.Fatal(err)
				}
			} else if err := controller.Close(); err != nil {
				t.Fatal(err)
			}
			frames := browserFrames(transport)[before:]
			wantCount := 3
			if generationChange {
				wantCount = 2
			}
			if len(frames) != wantCount {
				t.Fatalf("pending%v generation%v: cleanup prepositioned: %x", pending, generationChange, frames)
			}
			for _, frame := range frames {
				if (frame[3] == 4 || frame[3] == 5) && frame[6] != 0 {
					t.Fatalf("cleanup reasserted button: %x", frames)
				}
			}
			if controller.buttons|controller.absoluteButtons|controller.absolutePending|controller.relativeButtons|controller.relativePending != 0 {
				t.Fatal("origin cleanup retained owner state")
			}
		}
	}
}
