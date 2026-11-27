//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/devices"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/v4l2"
)

type metadataCaptureSource struct{ frames <-chan v4l2.Frame }

func (*metadataCaptureSource) Format() v4l2.Format {
	return v4l2.Format{Width: 5, Height: 3, FourCC: v4l2.PixFmtMJPEG}
}

func (source *metadataCaptureSource) ReadFrame(ctx context.Context) (v4l2.Frame, error) {
	select {
	case frame := <-source.frames:
		return frame, nil
	case <-ctx.Done():
		return v4l2.Frame{}, ctx.Err()
	}
}

func (*metadataCaptureSource) Close() error { return nil }

func TestCaptureFrameReturnsTargetDimensionsWithoutDiscovery(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 5, 3)), nil); err != nil {
		t.Fatal(err)
	}
	frames := make(chan v4l2.Frame, 1)
	frames <- v4l2.Frame{Data: encoded.Bytes(), Timestamp: v4l2.Timestamp{Microseconds: 1}, Flags: v4l2.TimestampMonotonic}
	manager, err := capture.New(capture.Options{
		Device: "test-capture", Width: 5, Height: 3,
		MonotonicNow: func() (int64, error) { return 1, nil },
		OpenSource: func(context.Context, string, v4l2.Options) (capture.Source, error) {
			return &metadataCaptureSource{frames: frames}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	var discoveries atomic.Int32
	server, controller, _ := bareServer(t, Options{
		ScreenWidth: 1920, ScreenHeight: 1080, Capture: manager,
		DiscoverCapture: func(context.Context) ([]devices.VideoDevice, error) {
			discoveries.Add(1)
			return nil, nil
		},
	})
	address, _ := serveForTest(t, server, controller)
	var frame struct {
		JPEG            []byte `json:"jpeg_b64"`
		Width           int    `json:"width"`
		Height          int    `json:"height"`
		TargetWidth     int    `json:"target_width"`
		TargetHeight    int    `json:"target_height"`
		MappingRevision uint64 `json:"mapping_revision"`
		FrameSequence   uint64 `json:"frame_sequence"`
	}
	if err := apiclient.New(address).Call(t.Context(), "capture_frame", nil, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Width != 5 || frame.Height != 3 || frame.TargetWidth != 1920 || frame.TargetHeight != 1080 || !bytes.Equal(frame.JPEG, encoded.Bytes()) {
		t.Fatalf("snapshot metadata or JPEG changed: %+v", frame)
	}
	if got := discoveries.Load(); got != 0 {
		t.Fatalf("snapshot enumerated capture devices %d times", got)
	}
	if mapping := server.mouse.Mapping(); frame.MappingRevision != mapping.Revision || frame.FrameSequence == 0 || frame.FrameSequence != mapping.Capture.Sequence {
		t.Fatalf("capture did not bind mapping revision/frame token: %+v %+v", frame, mapping)
	}
	// A newer context may win while an older snapshot finishes its API handler.
	if err := manager.SetDevice(t.Context(), "test-capture"); err != nil {
		t.Fatal(err)
	}
	if err := server.mouse.SetMappingCapture(t.Context(), hid.MappingCapture{Device: "replacement", Width: 5, Height: 3, Generation: manager.Info().Generation + 1, Sequence: frame.FrameSequence + 1}); err != nil {
		t.Fatal(err)
	}
	frames <- v4l2.Frame{Data: encoded.Bytes(), Timestamp: v4l2.Timestamp{Microseconds: 2}, Flags: v4l2.TimestampMonotonic}
	if err := apiclient.New(address).Call(t.Context(), "capture_frame", nil, &frame); !errors.Is(err, apiwire.ErrCaptureChanged) {
		t.Fatalf("old frame returned with new mapping revision: %v", err)
	}
}

func TestCaptureRemainsAvailableAfterUsedHIDFails(t *testing.T) {
	for _, test := range []struct{ method, params string }{
		{"set_capture_device", `{"device":"replacement"}`},
		{"set_capture_resolution", `{"width":1280,"height":720}`},
	} {
		t.Run(test.method, func(t *testing.T) {
			var encoded bytes.Buffer
			if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 5, 3)), nil); err != nil {
				t.Fatal(err)
			}
			frames := make(chan v4l2.Frame)
			feedCtx, stop := context.WithCancel(t.Context())
			feeding := make(chan struct{})
			go func() {
				defer close(feeding)
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-feedCtx.Done():
						return
					case <-ticker.C:
						select {
						case frames <- v4l2.Frame{Data: encoded.Bytes(), Timestamp: v4l2.Timestamp{Microseconds: 1}, Flags: v4l2.TimestampMonotonic}:
						case <-feedCtx.Done():
							return
						}
					}
				}
			}()
			manager, err := capture.New(capture.Options{
				Device: "initial", Width: 5, Height: 3, MonotonicNow: func() (int64, error) { return 1, nil },
				OpenSource: func(context.Context, string, v4l2.Options) (capture.Source, error) {
					return &metadataCaptureSource{frames: frames}, nil
				},
			})
			if err != nil {
				stop()
				<-feeding
				t.Fatal(err)
			}
			t.Cleanup(func() { stop(); <-feeding; _ = manager.Close() })
			server, _, transport := bareServer(t, Options{Capture: manager, ScreenWidth: 1920, ScreenHeight: 1080})
			if err := server.mouse.SetMappingCapture(t.Context(), hid.MappingCapture{Device: "initial", Width: 5, Height: 3, Generation: 1}); err != nil {
				t.Fatal(err)
			}
			geometry := hid.MappingGeometry{Width: 1920, Height: 1080, Span: hid.MappingSpan{Width: 3840, Height: 1200}}
			if err := server.mouse.SetMapping(t.Context(), hid.MappingSet{Geometry: &geometry}); err != nil {
				t.Fatal(err)
			}
			if err := server.mouse.MoveRel(t.Context(), 1, 0); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("test serial adapter disconnected")
			transport.prepare = func(context.Context) error { return failure }
			before := manager.Info()
			if result, err := server.handlers[test.method](t.Context(), json.RawMessage(test.params)); err != nil || result == nil {
				t.Fatalf("healthy capture setter depended on HID: %v", err)
			}
			changed := manager.Info()
			if changed.Generation != before.Generation+1 || test.method == "set_capture_device" && changed.Device != "replacement" || test.method == "set_capture_resolution" && (changed.Width != 1280 || changed.Height != 720) {
				t.Fatalf("capture setter did not reach manager: %+v", changed)
			}
			if result, err := server.captureFrame(t.Context(), json.RawMessage(`{}`)); err != nil || result == nil {
				t.Fatalf("healthy standalone capture depended on HID: %v", err)
			}
			bound := server.mouse.Mapping()
			if bound.Capture.Generation != changed.Generation || bound.Ready || !strings.Contains(bound.Message, failure.Error()) {
				t.Fatalf("HID diagnostics/readiness or new capture binding lost: %+v", bound)
			}
			prepares := transport.prepares
			if _, err := server.captureFrame(t.Context(), json.RawMessage(`{}`)); err != nil || transport.prepares != prepares || server.mouse.Mapping().Revision != bound.Revision {
				t.Fatalf("same-context capture retried failed HID release: %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := server.handlers[test.method](ctx, json.RawMessage(test.params)); !errors.Is(err, context.Canceled) || manager.Info().Generation != changed.Generation {
				t.Fatalf("cancelled setter changed capture: %v", err)
			}
		})
	}
}
