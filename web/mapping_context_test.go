//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/v4l2"
)

func TestHIDReleaseFailureDoesNotCloseHealthyVideoOrRepeatBinding(t *testing.T) {
	fixture := newFixture(t, Options{FPS: 120})
	if err := fixture.mouse.SetMappingCapture(t.Context(), hid.MappingCapture{
		Device: "/dev/fake", Width: 128, Height: 96, Crop: image.Rect(10, 12, 118, 84), Generation: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.mouse.SetMapping(t.Context(), hid.MappingSet{Geometry: &hid.MappingGeometry{
		Offset: hid.Point{X: 10, Y: 12}, Width: 108, Height: 72, Span: hid.MappingSpan{Width: 128, Height: 96},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.mouse.MoveRel(t.Context(), 1, 1); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("serial adapter unavailable")
	fixture.transport.mu.Lock()
	fixture.transport.hook = func(context.Context, []byte) error { return failure }
	fixture.transport.mu.Unlock()
	defer func() {
		fixture.transport.mu.Lock()
		fixture.transport.hook = nil
		fixture.transport.mu.Unlock()
	}()
	conn := newFakeSocket()
	done := make(chan struct{})
	go func() { fixture.server.serveConnection(conn); close(done) }()
	t.Cleanup(func() { _ = conn.CloseNow(); receive(t, done) })
	receiveMapping(t, conn)
	initialRevision := fixture.mouse.Mapping().Revision
	fixture.source.frames <- sourceFrame(sourceJPEG(t), 1)
	var diagnostic, video bool
	deadline := time.After(5 * time.Second)
	for !video {
		select {
		case message := <-conn.output:
			video = message.kind == websocket.MessageBinary
			if !video {
				var config struct {
					Mapping mappingConfig `json:"mapping"`
				}
				if err := json.Unmarshal(message.data, &config); err != nil {
					t.Fatal(err)
				}
				diagnostic = diagnostic || strings.Contains(config.Mapping.Message, failure.Error())
			}
		case <-conn.closed:
			t.Fatal("HID cleanup failure closed healthy video")
		case <-deadline:
			t.Fatal("healthy video did not resume")
		}
	}
	state := fixture.mouse.Mapping()
	if !diagnostic || state.Ready || state.Geometry == nil || state.Capture.Crop != image.Rect(0, 12, 128, 84) || state.Capture.Sequence != 1 {
		t.Fatalf("HID failure lost its diagnostic or failed to bind gated video: %+v", state)
	}
	if state.Revision != initialRevision+1 {
		t.Fatalf("capture transition repeated: %+v", state)
	}
	prepares, reports := fixture.transport.snapshot()
	fixture.source.frames <- sourceFrame(sourceJPEG(t), 2)
	if second := receive(t, conn.output); second.kind != websocket.MessageBinary {
		t.Fatalf("video did not continue: %s", second.data)
	}
	afterPrepares, afterReports := fixture.transport.snapshot()
	if afterPrepares != prepares || len(afterReports) != len(reports) || fixture.mouse.Mapping().Revision != state.Revision {
		t.Fatal("unchanged capture retried HID cleanup or mapping invalidation")
	}
	select {
	case <-conn.closed:
		t.Fatal("video connection closed after HID cleanup failure")
	default:
	}
}

func TestPendingVideoCannotUseANewerMappingsDifferentCrop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fixture := newFixture(t, Options{})
		observed := make(chan error, 1)
		newerCrop := image.Rect(10, 12, 118, 84)
		captures, err := capture.New(capture.Options{
			Device: "/dev/fake", Width: 128, Height: 96,
			Autocrop: true, ScreenWidth: 1920, ScreenHeight: 1080,
			OpenSource: func(context.Context, string, v4l2.Options) (capture.Source, error) { return fixture.source, nil },
			OnFrame: func(device string, metadata capture.Metadata) {
				// Another consumer has already bound a newer crop and reapplied its geometry.
				err := fixture.mouse.SetMappingCapture(t.Context(), hid.MappingCapture{
					Device: device,
					Width:  metadata.Width, Height: metadata.Height, Crop: newerCrop,
					Generation: metadata.Generation, Sequence: metadata.DeliverySequence + 1,
				})
				if err == nil {
					err = fixture.mouse.SetMapping(t.Context(), hid.MappingSet{Geometry: &hid.MappingGeometry{
						Offset: hid.Point{X: 10, Y: 12}, Width: 108, Height: 72,
						Span: hid.MappingSpan{Width: 128, Height: 96},
					}})
				}
				observed <- err
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer captures.Close()
		server, err := New(fixture.controller, fixture.mouse, captures, Options{})
		if err != nil {
			t.Fatal(err)
		}
		subscription, err := captures.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		defer subscription.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		conn := newFakeSocket()
		done := make(chan error, 1)
		go func() { done <- server.write(ctx, conn, subscription, nil) }()
		receiveMapping(t, conn)
		fixture.source.frames <- sourceFrame(sourceJPEG(t), 1)
		if err := receive(t, observed); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		for len(conn.output) != 0 {
			if message := <-conn.output; message.kind == websocket.MessageBinary {
				t.Fatal("older pending crop was sent under the newer mapping revision")
			}
		}
		if state := fixture.mouse.Mapping(); !state.Ready || state.Capture.Crop != newerCrop || state.Capture.Sequence != 2 {
			t.Fatalf("stale frame replaced the newer mapping context: %+v", state)
		}
		cancel()
		if err := receive(t, done); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}
