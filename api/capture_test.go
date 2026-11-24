//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"sync/atomic"
	"testing"

	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/devices"
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
		JPEG         []byte `json:"jpeg_b64"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		TargetWidth  int    `json:"target_width"`
		TargetHeight int    `json:"target_height"`
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
}
