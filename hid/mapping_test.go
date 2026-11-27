package hid

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

func mappingCapture() MappingCapture {
	return MappingCapture{Device: "fixture", Width: 4096, Height: 4096, Generation: 1, Sequence: 1}
}

func mappingGeometry() MappingGeometry {
	return MappingGeometry{
		Offset: Point{-1024, -512}, Width: 2049, Height: 3072,
		Span: MappingSpan{X: -2048, Y: -1024, Width: 4096, Height: 4096},
	}
}

func setDisplayMapping(t *testing.T, mouse *Mouse, geometry *MappingGeometry) MappingTransform {
	t.Helper()
	if geometry == nil {
		value := mappingGeometry()
		geometry = &value
	}
	if err := mouse.SetMappingCapture(t.Context(), mappingCapture()); err != nil {
		t.Fatal(err)
	}
	if err := mouse.SetMapping(t.Context(), MappingSet{Geometry: geometry}); err != nil {
		t.Fatal(err)
	}
	state := mouse.Mapping()
	if !state.Ready || state.Mode != "extended" || state.Geometry == nil || state.Transform == nil {
		t.Fatalf("geometry did not activate mapping: %+v", state)
	}
	return *state.Transform
}

func TestMappingSignedGeometryAndOverflow(t *testing.T) {
	for _, offset := range []Point{{-1920, 0}, {1920, 0}, {0, -1080}, {0, 1080}, {-1920, -1080}, {1920, 1080}} {
		span := MappingSpan{X: min(0, offset.X), Y: min(0, offset.Y), Width: 3840, Height: 2160}
		geometry := MappingGeometry{Offset: offset, Width: 1920, Height: 1080, Span: span}
		transform, err := geometryTransform(geometry)
		if err != nil {
			t.Fatal(err)
		}
		for _, point := range []Point{{0, 0}, {4095, 4095}} {
			if _, err := transform.apply(point); err != nil {
				t.Fatalf("%+v: %v", geometry, err)
			}
		}
		first, _ := transform.apply(Point{})
		want := Point{}
		if offset.X > 0 {
			want.X = 2048
		}
		if offset.Y > 0 {
			want.Y = 2048
		}
		if first != want {
			t.Fatalf("offset%v starts at%v, want%v", offset, first, want)
		}
	}
	maxInt := int(^uint(0) >> 1)
	for _, geometry := range []MappingGeometry{
		{Offset: Point{maxInt, 0}, Width: 2, Height: 2, Span: MappingSpan{X: -maxInt - 1, Width: maxInt, Height: 2}},
		{Width: 1, Height: 2, Span: MappingSpan{Width: 2, Height: 2}},
	} {
		if _, err := geometryTransform(geometry); !errors.Is(err, ErrMapping) {
			t.Fatalf("accepted invalid geometry: %+v: %v", geometry, err)
		}
	}
}

func TestMappingGeometryActivatesWithoutInputOrObservation(t *testing.T) {
	_, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096, MappingMode: "extended"})
	geometry := mappingGeometry()
	if err := mouse.SetMapping(t.Context(), MappingSet{Mode: "extended", Geometry: &geometry}); err != nil {
		t.Fatal(err)
	}
	state := mouse.Mapping()
	if !state.Ready || state.Mode != "extended" || state.Transform == nil || state.Geometry == nil {
		t.Fatalf("valid geometry remained unavailable: %+v", state)
	}
	geometry.Offset.X++
	state.Geometry.Offset.Y++
	state.Transform.ScaleX = 99
	if got := mouse.Mapping(); got.Geometry.Offset != mappingGeometry().Offset || got.Transform.ScaleX == 99 {
		t.Fatal("caller or snapshot modified stored geometry")
	}
	before := mouse.Mapping().Revision
	if err := mouse.SetMappingCapture(t.Context(), MappingCapture{Device: "fixture", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	if err := mouse.SetMappingCapture(t.Context(), mappingCapture()); err != nil {
		t.Fatal(err)
	}
	if got := mouse.Mapping(); !got.Ready || got.Revision != before {
		t.Fatal("first image invalidated explicitly configured geometry")
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
		t.Fatal("geometry configuration moved the target or opened hardware")
	}
	if err := mouse.ResetMapping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := mouse.Mapping(); got.Mode != "extended" || got.Ready || got.Geometry != nil {
		t.Fatalf("reset did not retain unconfigured extended mode: %+v", got)
	}
}

func TestMappingExtendedSelectionPreservesActiveGeometry(t *testing.T) {
	_, mouse, _ := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
	setDisplayMapping(t, mouse, nil)
	before := mouse.Mapping()
	if err := mouse.SetMapping(t.Context(), MappingSet{Mode: "extended"}); err != nil {
		t.Fatal(err)
	}
	state := mouse.Mapping()
	if !state.Ready || state.Transform == nil || *state.Transform != *before.Transform || state.Geometry == nil || *state.Geometry != *before.Geometry || state.Revision <= before.Revision {
		t.Fatalf("idempotent mode selection lost active geometry: %+v", state)
	}
	capture := state.Capture
	capture.Generation++
	if err := mouse.SetMappingCapture(t.Context(), capture); err != nil {
		t.Fatal(err)
	}
	if err := mouse.SetMapping(t.Context(), MappingSet{Mode: "extended"}); err != nil {
		t.Fatal(err)
	}
	if state := mouse.Mapping(); state.Ready || state.Transform != nil || state.Geometry == nil {
		t.Fatal("mode selection reactivated invalidated geometry")
	}
}

func TestMappingFailedReleaseKeepsLegacyAvailableAndRejectsStaleRevision(t *testing.T) {
	for _, mode := range []string{"legacy", "extended"} {
		t.Run(mode, func(t *testing.T) {
			controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
			if mode == "extended" {
				setDisplayMapping(t, mouse, nil)
			}
			if err := mouse.Down(t.Context(), "left", ButtonOptions{}); err != nil {
				t.Fatal(err)
			}
			before := mouse.Mapping()
			failure := errors.New("transient mapping release failure")
			transport.writeHook = func(_ context.Context, _ int, frame []byte) error {
				if bytes.Equal(frame, ch9329.Keyboard(0)) {
					return failure
				}
				return nil
			}
			if err := mouse.ResetMapping(t.Context()); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			state := mouse.Mapping()
			if state.Mode != mode || state.Ready != (mode == "legacy") || state.Revision <= before.Revision || controller.buttons != 0 {
				t.Fatalf("failed transition corrupted readiness or release: %+v", state)
			}
			frames := browserFrames(transport)
			if !bytes.Equal(frames[len(frames)-1], ch9329.MouseRelative(0, 0, 0, 0)) {
				t.Fatal("keyboard release failure skipped mouse cleanup")
			}
			transport.writeHook = nil
			if err := mouse.MoveAbs(WithMappingRevision(t.Context(), before.Revision), 100, 200); !errors.Is(err, ErrMappingChanged) {
				t.Fatalf("stale action survived failed transition: %v", err)
			}
			if len(browserFrames(transport)) != len(frames) {
				t.Fatal("stale action emitted input")
			}
			if mode == "legacy" {
				if err := mouse.MoveAbs(t.Context(), 100, 200); err != nil {
					t.Fatalf("failed transition blocked identity input: %v", err)
				}
			} else if err := mouse.MoveAbs(t.Context(), 100, 200); !errors.Is(err, ErrMappingUnavailable) {
				t.Fatalf("failed transition resumed extended input: %v", err)
			}
			if mode == "extended" {
				if err := mouse.SetMapping(t.Context(), MappingSet{Mode: "extended"}); err != nil {
					t.Fatal(err)
				}
				if state := mouse.Mapping(); state.Ready || state.Transform != nil || state.Geometry == nil {
					t.Fatal("mode selection reactivated geometry after a failed release")
				}
			}
		})
	}
}

func TestMappingCanceledWhileWaitingKeepsLegacyAvailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
		if err := controller.acquire(t.Context()); err != nil {
			t.Fatal(err)
		}
		before := mouse.Mapping()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- mouse.SetMapping(ctx, MappingSet{Mode: "extended"}) }()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		<-controller.gate
		if state := mouse.Mapping(); state.Mode != "legacy" || !state.Ready || state.Revision <= before.Revision {
			t.Fatalf("cancelled transition blocked legacy mapping: %+v", state)
		}
		if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
			t.Fatal("cancelled mapping transition opened hardware")
		}
		if err := mouse.MoveAbs(t.Context(), 100, 200); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMappingQueuedUpdatesRespectContextDeadline(t *testing.T) {
	for _, operation := range []string{"capture", "mapping"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
				if err := controller.acquire(t.Context()); err != nil {
					t.Fatal(err)
				}
				releaseGate := sync.OnceFunc(func() { <-controller.gate })
				defer releaseGate()
				first := make(chan error, 1)
				go func() { first <- mouse.SetMapping(t.Context(), MappingSet{Mode: "extended"}) }()
				synctest.Wait() // First update owns mapping admission, waiting for HID.
				before := mouse.Mapping()
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				var err error
				if operation == "capture" {
					err = mouse.SetMappingCapture(ctx, mappingCapture())
				} else {
					err = mouse.ResetMapping(ctx)
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("queued update ignored its deadline: %v", err)
				}
				if state := mouse.Mapping(); state.Revision != before.Revision || state.Capture != before.Capture || state.Mode != before.Mode || state.Ready != before.Ready {
					t.Fatalf("expired queued update changed mapping: %+v", state)
				}
				releaseGate()
				if err := <-first; err != nil {
					t.Fatal(err)
				}
				if err := mouse.SetMappingCapture(t.Context(), mappingCapture()); err != nil {
					t.Fatalf("expired waiter retained mapping admission: %v", err)
				}
				if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
					t.Fatal("queued metadata updates opened hardware")
				}
			})
		})
	}
}

func TestMappingCaptureCommitsOnceWhenReleaseFails(t *testing.T) {
	for _, mode := range []string{"legacy", "extended"} {
		t.Run(mode, func(t *testing.T) {
			_, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
			if mode == "extended" {
				setDisplayMapping(t, mouse, nil)
			} else if err := mouse.SetMappingCapture(t.Context(), mappingCapture()); err != nil {
				t.Fatal(err)
			}
			if err := mouse.Down(t.Context(), "left", ButtonOptions{}); err != nil {
				t.Fatal(err)
			}
			before := mouse.Mapping()
			failure := errors.New("serial unavailable")
			transport.writeHook = func(context.Context, int, []byte) error { return failure }
			capture := before.Capture
			capture.Generation++
			capture.Sequence++
			capture.Crop = image.Rect(10, 0, 4000, 4096)
			if err := mouse.SetMappingCapture(t.Context(), capture); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			bound := mouse.Mapping()
			if bound.Capture != capture || bound.Revision != before.Revision+1 || bound.Ready != (mode == "legacy") || bound.Message == "" {
				t.Fatalf("release failure did not bind capture safely: %+v", bound)
			}
			if mode == "extended" && (bound.Transform != nil || bound.Geometry == nil) {
				t.Fatal("release failure reactivated or discarded extended geometry")
			}
			prepares, writes, _, _ := transport.snapshot()
			capture.Sequence++
			if err := mouse.SetMappingCapture(t.Context(), capture); err != nil {
				t.Fatalf("ordinary next frame retried failed cleanup: %v", err)
			}
			if state := mouse.Mapping(); state.Capture != capture || state.Revision != bound.Revision || state.Ready != bound.Ready {
				t.Fatalf("ordinary next frame repeated transition: %+v", state)
			}
			if afterPrepare, afterWrite, _, _ := transport.snapshot(); afterPrepare != prepares || afterWrite != writes {
				t.Fatal("ordinary next frame retried HID release")
			}
			transport.writeHook = nil
		})
	}
}

func TestMappingRangeChecksBeforePacketClamping(t *testing.T) {
	for _, test := range []struct {
		transform MappingTransform
		point     Point
	}{
		{MappingTransform{ScaleX: 1, ScaleY: 1}, Point{-1, 10}},
		{MappingTransform{ScaleX: 1, ScaleY: 1}, Point{10, 4096}},
		{MappingTransform{ScaleX: 2, ScaleY: 1}, Point{4095, 10}},
		{MappingTransform{ScaleX: 1, ScaleY: 1, TranslateX: -1}, Point{0, 10}},
		{MappingTransform{ScaleX: math.NaN(), ScaleY: 1}, Point{100, 100}},
		{MappingTransform{ScaleX: 1, ScaleY: math.Inf(1)}, Point{100, 100}},
	} {
		if _, err := test.transform.apply(test.point); !errors.Is(err, ErrCoordinates) {
			t.Fatalf("invalid transformed point accepted: %+v %v: %v", test.transform, test.point, err)
		}
	}
}

func TestMappingUnconfiguredPreservesRawInput(t *testing.T) {
	_, mouse, transport := mouseFixture(t, MouseOptions{Width: 100, Height: 100, MappingMode: "extended"})
	if err := mouse.MoveAbs(t.Context(), 50, 50); !errors.Is(err, ErrMappingUnavailable) {
		t.Fatal(err)
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
		t.Fatal("unconfigured mapping opened hardware")
	}
	if err := mouse.MoveRel(t.Context(), 5, -7); err != nil {
		t.Fatal(err)
	}
	if err := mouse.Click(t.Context(), "left", ButtonOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, frame := range browserFrames(transport) {
		if frame[3] != 5 {
			t.Fatalf("unconfigured raw input emitted absolute report: %x", frame)
		}
	}
	if err := mouse.ResetMapping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state := mouse.Mapping(); state.Mode != "extended" || state.Ready {
		t.Fatalf("reset fell back to legacy: %+v", state)
	}
	if err := mouse.SetMapping(t.Context(), MappingSet{Mode: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := mouse.MoveAbs(t.Context(), 99, 99); err != nil {
		t.Fatal(err)
	}
	frames := browserFrames(transport)
	if !bytes.Equal(frames[len(frames)-1], ch9329.MouseAbsolute(0, 4095, 4095, 0)) {
		t.Fatal("legacy identity changed")
	}
}

func TestMappingSharedAbsolutePaths(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
		transform := setDisplayMapping(t, mouse, nil)
		_, _, _, before := transport.snapshot()
		if err := mouse.MoveAbs(t.Context(), 1024, 3072); err != nil {
			t.Fatal(err)
		}
		browser := NewBrowser(mouse.controller, mouse)
		if err := browser.Move(WithMappingRevision(t.Context(), mouse.Mapping().Revision), 1024, 3072); err != nil {
			t.Fatal(err)
		}
		point, _ := transform.apply(Point{1024, 3072})
		frames := browserFrames(transport)
		want := ch9329.MouseAbsolute(0, point.X, point.Y, 0)
		if !bytes.Equal(frames[len(before)], want) || !bytes.Equal(frames[len(before)+1], want) {
			t.Fatal("browser/API apply different transforms")
		}
		start := len(frames)
		if err := mouse.Drag(t.Context(), Point{0, 0}, Point{4095, 4095}, "left", "relative"); err != nil {
			t.Fatal(err)
		}
		frames = browserFrames(transport)[start:]
		if down, up := mouseReportEdges(frames); down != ([3]int{1, 0, 0}) || up != ([3]int{1, 0, 0}) {
			t.Fatalf("mapped drag button edges: down%v up%v", down, up)
		}
		for _, frame := range frames {
			if frame[3] == 4 && frame[6] != 0 {
				t.Fatalf("mapped drag copied relative hold into absolute report: %x", frame)
			}
		}
	})
}

func TestMappingCaptureBindingAndStaleUpdates(t *testing.T) {
	_, mouse, _ := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
	setDisplayMapping(t, mouse, nil)
	before := mouse.Mapping()
	capture := before.Capture
	capture.Sequence += 5
	if err := mouse.SetMappingCapture(t.Context(), capture); err != nil {
		t.Fatal(err)
	}
	if state := mouse.Mapping(); state.Revision != before.Revision || !state.Ready {
		t.Fatal("ordinary frame invalidated mapping")
	}
	stale := capture
	stale.Sequence--
	stale.Crop = image.Rect(10, 0, 4000, 4096)
	if err := mouse.SetMappingCapture(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	if state := mouse.Mapping(); state.Revision != before.Revision || !state.Capture.Crop.Empty() {
		t.Fatal("stale API snapshot overwrote new capture")
	}
	capture.Crop = stale.Crop
	capture.Sequence++
	if err := mouse.SetMappingCapture(t.Context(), capture); err != nil {
		t.Fatal(err)
	}
	if state := mouse.Mapping(); state.Ready || state.Mode != "extended" || state.Revision <= before.Revision || state.Geometry == nil || state.Transform != nil {
		t.Fatal("crop change retained mapping")
	}
	if err := mouse.MoveAbs(t.Context(), 100, 100); !errors.Is(err, ErrMappingUnavailable) {
		t.Fatalf("stale capture geometry still admitted movement: %v", err)
	}
	if err := mouse.SetMapping(t.Context(), MappingSet{Geometry: mouse.Mapping().Geometry}); err != nil {
		t.Fatal(err)
	}
	if !mouse.Mapping().Ready {
		t.Fatal("reapplying retained geometry did not restore readiness")
	}
	if err := mouse.InvalidateMapping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state := mouse.Mapping(); state.Ready || state.Geometry == nil || state.Transform != nil {
		t.Fatal("explicit invalidation did not retain inactive geometry")
	}
}

func TestMappingInitialCaptureAndSnapshotOwnership(t *testing.T) {
	_, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
	before := mouse.Mapping()
	if err := mouse.SetMappingCapture(t.Context(), mappingCapture()); err != nil {
		t.Fatal(err)
	}
	if state := mouse.Mapping(); state.Revision != before.Revision || !state.Ready {
		t.Fatal("first legacy image requires a second image")
	}
	before.Transform.ScaleX = 99
	if mouse.Mapping().Transform.ScaleX != 1 {
		t.Fatal("snapshot mutated shared transform")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	capture := mappingCapture()
	capture.Generation++
	if err := mouse.SetMappingCapture(ctx, capture); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if state := mouse.Mapping(); state.Capture.Generation != 1 {
		t.Fatal("cancelled capture binding changed state")
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
		t.Fatal("initial capture binding opened hardware")
	}
}

func TestMappingFirstFormattedImagePreservesIdentityAndHeldInput(t *testing.T) {
	controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
	if err := mouse.SetMappingCapture(t.Context(), MappingCapture{Device: "fixture", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	if err := mouse.Down(t.Context(), "left", ButtonOptions{}); err != nil {
		t.Fatal(err)
	}
	before, changed := mouse.WatchMapping()
	if err := mouse.SetMappingCapture(t.Context(), mappingCapture()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("first image did not notify mapping viewers")
	}
	if state := mouse.Mapping(); state.Revision != before.Revision || !state.Ready || state.Capture.Width != 4096 || controller.relativeButtons != 1 {
		t.Fatalf("first formatted image invalidated identity or held input: %+v", state)
	}
	if frames := browserFrames(transport); len(frames) != 1 || !bytes.Equal(frames[0], ch9329.MouseRelative(1, 0, 0, 0)) {
		t.Fatalf("first formatted image emitted cleanup: %x", frames)
	}
	if err := mouse.MoveAbs(t.Context(), 1024, 2048); err != nil {
		t.Fatal(err)
	}
	frames := browserFrames(transport)
	if !bytes.Equal(frames[len(frames)-1], ch9329.MouseAbsolute(0, 1024, 2048, 0)) || controller.relativeButtons != 1 {
		t.Fatal("first image changed identity coordinates or button ownership")
	}
}

func TestMappingPrevalidatesWholeDrag(t *testing.T) {
	_, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
	mouse.mapping.state.Transform = &MappingTransform{ScaleX: 2, ScaleY: 1}
	if err := mouse.Drag(t.Context(), Point{0, 0}, Point{4095, 4095}, "left", ""); !errors.Is(err, ErrCoordinates) {
		t.Fatal(err)
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
		t.Fatal("invalid transformed endpoint pressed first")
	}
	if err := mouse.SetMapping(t.Context(), MappingSet{}); !errors.Is(err, ErrMapping) {
		t.Fatal("accepted empty mapping request")
	}
}

func TestMappingInvalidationCancelsDragAndRejectsOldRevision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
		setDisplayMapping(t, mouse, nil)
		revision := mouse.Mapping().Revision
		done := make(chan error, 1)
		go func() { done <- mouse.Drag(context.Background(), Point{10, 10}, Point{2000, 2000}, "left", "") }()
		synctest.Wait()
		if err := mouse.ResetMapping(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, ErrMappingChanged) {
			t.Fatalf("drag survived mapping change: %v", err)
		}
		frames := browserFrames(transport)
		if !bytes.Equal(frames[len(frames)-1], ch9329.MouseRelative(0, 0, 0, 0)) || mouse.controller.buttons != 0 {
			t.Fatal("mapping invalidation stranded held button")
		}
		before := len(frames)
		if err := mouse.MoveAbs(WithMappingRevision(t.Context(), revision), 10, 10); !errors.Is(err, ErrMappingChanged) {
			t.Fatal(err)
		}
		if len(browserFrames(transport)) != before {
			t.Fatal("stale revision emitted input")
		}
	})
}

func TestMappingInvalidatesQueuedActionBeforeWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
		if err := controller.acquire(t.Context()); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- mouse.MoveAbs(t.Context(), 1000, 2000) }()
		synctest.Wait()
		changed := make(chan error, 1)
		go func() { changed <- mouse.ResetMapping(t.Context()) }()
		synctest.Wait()
		<-controller.gate
		if err := <-done; !errors.Is(err, ErrMappingChanged) {
			t.Fatalf("queued old action admitted: %v", err)
		}
		if err := <-changed; err != nil {
			t.Fatal(err)
		}
		if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
			t.Fatal("queued stale action touched hardware")
		}
	})
}

func TestMappingChangeReleasesKeyboardAndBothButtonOwners(t *testing.T) {
	browser, transport := browserFixture(t, "absolute")
	if err := browser.Key(t.Context(), "ShiftLeft", true); err != nil {
		t.Fatal(err)
	}
	if err := browser.Key(t.Context(), "KeyA", true); err != nil {
		t.Fatal(err)
	}
	if err := browser.Buttons(t.Context(), 100, 200, 1); err != nil {
		t.Fatal(err)
	}
	if err := browser.mouse.Down(t.Context(), "right", ButtonOptions{}); err != nil {
		t.Fatal(err)
	}
	before := len(browserFrames(transport))
	if err := browser.mouse.SetMapping(t.Context(), MappingSet{Mode: "extended"}); err != nil {
		t.Fatal(err)
	}
	frames := browserFrames(transport)[before:]
	want := [][]byte{ch9329.Keyboard(0), ch9329.MouseAbsolute(0, 100, 200, 0), ch9329.MouseRelative(0, 0, 0, 0)}
	if len(frames) != len(want) {
		t.Fatalf("global release reports: %x", frames)
	}
	for index := range want {
		if !bytes.Equal(frames[index], want[index]) {
			t.Fatalf("global release report %d: %x", index, frames[index])
		}
	}
	if err := browser.Key(t.Context(), "KeyB", true); err != nil {
		t.Fatal(err)
	}
	frames = browserFrames(transport)
	if !bytes.Equal(frames[len(frames)-1], ch9329.Keyboard(0, 5)) || browser.buttons != 0 {
		t.Fatal("browser reasserted pre-mapping keys or buttons")
	}
}

func TestBrowserInvalidMappedGoalReleasesButtons(t *testing.T) {
	_, mouse, transport := mouseFixture(t, MouseOptions{Width: 4096, Height: 4096})
	setDisplayMapping(t, mouse, nil)
	browser := NewBrowser(mouse.controller, mouse)
	if err := browser.Buttons(t.Context(), 100, 100, 1); err != nil {
		t.Fatal(err)
	}
	if err := browser.Move(t.Context(), -1, 100); !errors.Is(err, ErrCoordinates) {
		t.Fatal(err)
	}
	frames := browserFrames(transport)
	if browser.buttons != 0 || mouse.controller.buttons != 0 || !bytes.Equal(frames[len(frames)-1], ch9329.MouseRelative(0, 0, 0, 0)) {
		t.Fatal("invalid streamed goal stranded drag")
	}
}
