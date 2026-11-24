//go:build linux && (amd64 || arm64)

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/linkdata/autopaws/api"
	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/devices"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/v4l2"
)

type captureSource struct {
	format  v4l2.Format
	reads   chan v4l2.Frame
	started chan struct{}
	closed  chan struct{}
	start   sync.Once
	close   sync.Once
	closes  atomic.Int32
}

func newCaptureSource(width, height uint32) *captureSource {
	return &captureSource{format: v4l2.Format{Width: width, Height: height, FourCC: v4l2.PixFmtMJPEG, SizeImage: 1 << 20}, reads: make(chan v4l2.Frame, 8), started: make(chan struct{}), closed: make(chan struct{})}
}

func (source *captureSource) Format() v4l2.Format { return source.format }
func (source *captureSource) ReadFrame(ctx context.Context) (v4l2.Frame, error) {
	source.start.Do(func() { close(source.started) })
	select {
	case frame := <-source.reads:
		return frame, nil
	case <-ctx.Done():
		return v4l2.Frame{}, ctx.Err()
	}
}

func (source *captureSource) Close() error {
	source.close.Do(func() { source.closes.Add(1); close(source.closed) })
	return nil
}

func captureJPEG(t *testing.T, width, height int, cropped bool) []byte {
	t.Helper()
	img := image.NewYCbCr(image.Rect(0, 0, width, height), image.YCbCrSubsampleRatio420)
	for y := range height {
		for x := range width {
			if !cropped || y >= 16 && y < height-16 {
				img.Y[img.YOffset(x, y)] = 180
			}
		}
	}
	for i := range img.Cb {
		img.Cb[i], img.Cr[i] = 128, 128
	}
	var data bytes.Buffer
	if err := jpeg.Encode(&data, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func sourceFrame(jpeg []byte, microseconds int64) v4l2.Frame {
	return v4l2.Frame{Data: bytes.Clone(jpeg), Timestamp: v4l2.Timestamp{Microseconds: microseconds}, Flags: v4l2.TimestampMonotonic}
}

func captureManager(t *testing.T, options capture.Options) *capture.Manager {
	t.Helper()
	if options.MonotonicNow == nil {
		options.MonotonicNow = func() (int64, error) { return 1_000_000, nil }
	}
	options.IdleTimeout = 10 * time.Millisecond
	manager, err := capture.New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	return manager
}

func captureAPI(t *testing.T, manager *capture.Manager, discover func(context.Context) ([]devices.VideoDevice, error)) (*apiclient.Client, *recordingTransport) {
	t.Helper()
	transport := &recordingTransport{}
	controller := hid.NewController(transport)
	layout, err := hid.LoadLayout("us104", "")
	if err != nil {
		t.Fatal(err)
	}
	mouse, err := hid.NewMouse(controller, hid.MouseOptions{Width: 1920, Height: 1080})
	if err != nil {
		t.Fatal(err)
	}
	server := api.New(hid.NewKeyboard(controller, layout), mouse, api.Options{Capture: manager, DiscoverCapture: discover, ScreenWidth: 1920, ScreenHeight: 1080})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		if err := controller.Close(); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
		server.Wait()
	})
	return apiclient.New(listener.Addr().String()), transport
}

func captureContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func captureWait(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(7 * time.Second):
		t.Fatal("capture test did not reach the controlled operation")
	}
}

// Real discovery over fake sysfs proves the API projection uses #35's filtering
// and USB identity. Both nodes claim aggregate capture; video1 is metadata only.
func captureDiscovery(t *testing.T, populated bool) (func(context.Context) ([]devices.VideoDevice, error), string) {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{"devices/usb4/4-1", "class/video4linux"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	usb := filepath.Join(root, "devices/usb4/4-1")
	for name, value := range map[string]string{"idVendor": "2b89\n", "idProduct": "5389\n"} {
		if err := os.WriteFile(filepath.Join(usb, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if populated {
		for _, name := range []string{"video0", "video1"} {
			dir := filepath.Join(usb, "4-1:1.0", "video4linux", name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "name"), []byte("UGREEN\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dir, filepath.Join(root, "class/video4linux", name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return func(ctx context.Context) ([]devices.VideoDevice, error) {
		return devices.DiscoverVideo(ctx, devices.Options{SysRoot: root, Probe: func(_ context.Context, path string) (v4l2.Info, error) {
			info := v4l2.Info{Capabilities: v4l2.CapDeviceCaps | v4l2.CapVideoCapture}
			switch path {
			case "/dev/video0":
				info.DeviceCaps = v4l2.CapVideoCapture
				info.Formats = []v4l2.PixelFormat{{FourCC: v4l2.PixFmtMJPEG, Description: "Motion-JPEG", Flags: 1}}
			case "/dev/video1":
			default:
				t.Errorf("unexpected fake discovery probe %q", path)
			}
			return info, nil
		}})
	}, usb
}

type captureWireStatus struct {
	Device            string
	Width             uint32 `json:"requested_width"`
	Height            uint32 `json:"requested_height"`
	Running, Autocrop bool
	Generation        uint64
	Error, Detail     string
	Format            *struct{ Width, Height, FourCC, BytesPerLine, SizeImage uint32 }
	Available         []json.RawMessage `json:"available_devices"`
}

type captureInfoWire struct {
	Capture captureWireStatus `json:"capture"`
}

func TestCaptureAPIAvailabilityDiscoveryAndIdleInfo(t *testing.T) {
	discover, usb := captureDiscovery(t, true)
	manager := captureManager(t, capture.Options{Width: 1920, Height: 1080})
	client, transport := captureAPI(t, manager, discover)
	var listing struct {
		Devices []struct {
			Device, Name string
			Formats      []struct {
				FourCC, Flags uint32
				Description   string
			}
			USB struct {
				VID, PID string
				BusPath  string `json:"bus_path"`
			}
		}
	}
	if err := client.Call(captureContext(t), "list_capture_devices", nil, &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Devices) != 1 || listing.Devices[0].Device != "/dev/video0" || listing.Devices[0].USB.BusPath != usb || len(listing.Devices[0].Formats) != 1 || listing.Devices[0].Formats[0].FourCC != v4l2.PixFmtMJPEG {
		t.Fatalf("discovery projection = %+v", listing)
	}
	for _, method := range []string{"capture_frame", "set_capture_resolution"} {
		var args any
		if method == "set_capture_resolution" {
			args = map[string]int{"width": 128, "height": 96}
		}
		if err := client.Call(captureContext(t), method, args, nil); !errors.Is(err, apiwire.ErrUnavailable) {
			t.Fatalf("%s without selection = %v", method, err)
		}
	}
	var info captureInfoWire
	if err := client.Call(captureContext(t), "get_device_info", nil, &info); err != nil || info.Capture.Error != "unavailable" || len(info.Capture.Available) != 1 {
		t.Fatalf("unselected info = %+v, %v", info, err)
	}
	if err := client.Call(captureContext(t), "set_capture_device", map[string]string{"device": "/dev/video0"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(captureContext(t), "set_capture_resolution", map[string]int{"width": 128, "height": 96}, nil); err != nil {
		t.Fatal(err)
	}
	info = captureInfoWire{}
	if err := client.Call(captureContext(t), "get_device_info", nil, &info); err != nil || info.Capture.Error != "" || info.Capture.Running || info.Capture.Device != "/dev/video0" || info.Capture.Width != 128 || info.Capture.Height != 96 || info.Capture.Format != nil || info.Capture.Generation != 3 {
		t.Fatalf("selected idle info = %+v, %v", info, err)
	}
	if prepares, frames, _ := transport.snapshot(); prepares != 0 || len(frames) != 0 {
		t.Fatal("capture information touched HID hardware")
	}
	empty, _ := captureDiscovery(t, false)
	emptyClient, _ := captureAPI(t, nil, empty)
	var noDevices struct{ Devices []json.RawMessage }
	if err := emptyClient.Call(captureContext(t), "list_capture_devices", nil, &noDevices); err != nil || noDevices.Devices == nil || len(noDevices.Devices) != 0 {
		t.Fatalf("empty list = %+v, %v", noDevices, err)
	}
}

func TestCaptureAPIFreshCroppedSnapshot(t *testing.T) {
	source := newCaptureSource(160, 120)
	data := captureJPEG(t, 160, 120, true)
	source.reads <- sourceFrame(data, 1000) // Equal timestamp is not fresh.
	source.reads <- sourceFrame([]byte("corrupt warmup"), 2000)
	source.reads <- sourceFrame(data, 3000)
	manager := captureManager(t, capture.Options{
		Device: "/dev/video0", Width: 1920, Height: 1080, Autocrop: true,
		OpenSource: func(context.Context, string, v4l2.Options) (capture.Source, error) { return source, nil },
	})
	discover, _ := captureDiscovery(t, true)
	client, _ := captureAPI(t, manager, discover)
	var frame struct {
		JPEG          []byte `json:"jpeg_b64"`
		Width, Height int
		TargetWidth   int `json:"target_width"`
		TargetHeight  int `json:"target_height"`
	}
	if err := client.Call(captureContext(t), "capture_frame", nil, &frame); err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.DecodeConfig(bytes.NewReader(frame.JPEG))
	if err != nil || frame.Width != 160 || frame.Height != 88 || decoded.Width != frame.Width || decoded.Height != frame.Height {
		t.Fatalf("cropped frame = %dx%d, decoded %+v, %v", frame.Width, frame.Height, decoded, err)
	}
	if frame.TargetWidth != 1920 || frame.TargetHeight != 1080 {
		t.Fatalf("cropped snapshot changed configured target dimensions: %+v", frame)
	}
	var info captureInfoWire
	if err := client.Call(captureContext(t), "get_device_info", nil, &info); err != nil || info.Capture.Format == nil || info.Capture.Format.Width != 160 || info.Capture.Width != 1920 || info.Capture.Height != 1080 {
		t.Fatalf("requested/actual info = %+v, %v", info, err)
	}
}

func TestCaptureAPIPendingSnapshotChangesGeneration(t *testing.T) {
	for _, method := range []string{"set_capture_device", "set_capture_resolution"} {
		t.Run(method, func(t *testing.T) {
			old, next := newCaptureSource(128, 96), newCaptureSource(160, 120)
			manager := captureManager(t, capture.Options{
				Device: "/dev/video0", Width: 128, Height: 96,
				OpenSource: func(_ context.Context, path string, options v4l2.Options) (capture.Source, error) {
					if path == "/dev/video0" && options.Width == 128 {
						return old, nil
					}
					select {
					case <-old.closed:
					default:
						t.Error("next source opened before old source close completed")
					}
					return next, nil
				},
			})
			discover, _ := captureDiscovery(t, true)
			client, _ := captureAPI(t, manager, discover)
			pending := make(chan error, 1)
			go func() { pending <- client.Call(captureContext(t), "capture_frame", nil, nil) }()
			captureWait(t, old.started)
			var args any = map[string]string{"device": "/dev/video2"}
			if method == "set_capture_resolution" {
				args = map[string]int{"width": 160, "height": 120}
			}
			if err := client.Call(captureContext(t), method, args, nil); err != nil {
				t.Fatal(err)
			}
			if err := <-pending; !errors.Is(err, apiwire.ErrCaptureChanged) {
				t.Fatalf("pending snapshot = %v, want capture changed", err)
			}
			next.reads <- sourceFrame(captureJPEG(t, 160, 120, false), 2000)
			var frame struct{ Width, Height int }
			if err := client.Call(captureContext(t), "capture_frame", nil, &frame); err != nil || frame.Width != 160 || frame.Height != 120 {
				t.Fatalf("new-generation snapshot = %+v, %v", frame, err)
			}
		})
	}
}

func feedCapture(t *testing.T, source *captureSource, jpeg []byte) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				select {
				case source.reads <- sourceFrame(jpeg, 2000):
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func TestCaptureAPIFailedResolutionCanBeRepaired(t *testing.T) {
	for _, subscribed := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "subscribed"}[subscribed], func(t *testing.T) {
			source := newCaptureSource(128, 96)
			failure := errors.New("unsupported requested size")
			manager := captureManager(t, capture.Options{
				Device: "/dev/video0", Width: 1, Height: 96,
				OpenSource: func(_ context.Context, _ string, options v4l2.Options) (capture.Source, error) {
					if options.Width != 128 {
						return nil, failure
					}
					return source, nil
				},
			})
			if subscribed {
				subscription, err := manager.Subscribe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = subscription.Close() })
			}
			discover, _ := captureDiscovery(t, true)
			client, _ := captureAPI(t, manager, discover)
			if err := client.Call(captureContext(t), "capture_frame", nil, nil); !errors.Is(err, apiwire.ErrUnavailable) {
				t.Fatalf("initial failed size = %v", err)
			}
			err := client.Call(captureContext(t), "set_capture_resolution", map[string]int{"width": 2, "height": 96}, nil)
			if subscribed && !errors.Is(err, apiwire.ErrUnavailable) || !subscribed && err != nil {
				t.Fatalf("second failed setting = %v", err)
			}
			if !subscribed {
				if err := client.Call(captureContext(t), "capture_frame", nil, nil); !errors.Is(err, apiwire.ErrUnavailable) {
					t.Fatalf("stored failed size = %v", err)
				}
			}
			var info captureInfoWire
			if err := client.Call(captureContext(t), "get_device_info", nil, &info); err != nil || info.Capture.Device != "/dev/video0" || info.Capture.Width != 2 || info.Capture.Error != "unavailable" || !strings.Contains(info.Capture.Detail, failure.Error()) {
				t.Fatalf("failed settings were not retained: %+v, %v", info, err)
			}
			if err := client.Call(captureContext(t), "set_capture_resolution", map[string]int{"width": 128, "height": 96}, nil); err != nil {
				t.Fatal("resolution repair failed", err)
			}
			feedCapture(t, source, captureJPEG(t, 128, 96, false))
			var frame struct{ Width, Height int }
			if err := client.Call(captureContext(t), "capture_frame", nil, &frame); err != nil || frame.Width != 128 || frame.Height != 96 {
				t.Fatalf("repaired snapshot = %+v, %v", frame, err)
			}
		})
	}
}

func TestCaptureAPITotalDeadlineIncludesStartupAndCorruptWarmup(t *testing.T) {
	source := newCaptureSource(128, 96)
	source.reads <- sourceFrame([]byte("corrupt warmup"), 2000)
	manager := captureManager(t, capture.Options{
		Device: "/dev/video0", Width: 128, Height: 96,
		OpenSource: func(ctx context.Context, _ string, _ v4l2.Options) (capture.Source, error) {
			select {
			case <-time.After(1500 * time.Millisecond):
				return source, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	discover, _ := captureDiscovery(t, true)
	client, _ := captureAPI(t, manager, discover)
	start := time.Now()
	err := client.Call(captureContext(t), "capture_frame", nil, nil)
	if elapsed := time.Since(start); !errors.Is(err, apiwire.ErrTimeout) || elapsed < 4900*time.Millisecond || elapsed > 6*time.Second {
		t.Fatalf("total snapshot acquisition = %v after %s", err, elapsed)
	}
}

func TestCaptureAPIEarlierCancellationAndDiscoveryErrors(t *testing.T) {
	source := newCaptureSource(128, 96)
	manager := captureManager(t, capture.Options{
		Device: "/dev/video0", Width: 128, Height: 96,
		OpenSource: func(context.Context, string, v4l2.Options) (capture.Source, error) { return source, nil },
	})
	client, _ := captureAPI(t, manager, func(context.Context) ([]devices.VideoDevice, error) { return nil, syscall.EACCES })
	ctx, cancel := context.WithCancel(captureContext(t))
	pending := make(chan error, 1)
	go func() { pending <- client.Call(ctx, "capture_frame", nil, nil) }()
	captureWait(t, source.started)
	cancel()
	if err := <-pending; !errors.Is(err, context.Canceled) {
		t.Fatalf("earlier cancellation identity lost: %v", err)
	}
	if err := client.Call(captureContext(t), "list_capture_devices", nil, nil); !errors.Is(err, apiwire.ErrUnavailable) {
		t.Fatalf("discovery permission error = %v", err)
	}
	var info captureInfoWire
	if err := client.Call(captureContext(t), "get_device_info", nil, &info); err != nil || info.Capture.Error != "unavailable" || !strings.Contains(info.Capture.Detail, "permission denied") {
		t.Fatalf("incomplete capture info hid discovery error: %+v, %v", info, err)
	}
	if err := client.Call(captureContext(t), "set_capture_resolution", map[string]int{"width": 160, "height": 120}, nil); err != nil {
		t.Fatal("discovery error blocked repair setter", err)
	}
	if err := client.Call(captureContext(t), "mouse_move", map[string]any{"x": 1, "y": 1, "relative": true}, nil); err != nil {
		t.Fatal("discovery error blocked input", err)
	}
}

func TestCaptureAPIClosedManagerStillReportsSelectedSettings(t *testing.T) {
	manager := captureManager(t, capture.Options{Device: "/dev/video0", Width: 128, Height: 96})
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	discover, _ := captureDiscovery(t, true)
	client, _ := captureAPI(t, manager, discover)
	for _, tc := range []struct {
		method string
		params any
	}{
		{"capture_frame", nil},
		{"set_capture_device", map[string]string{"device": "/dev/video2"}},
		{"set_capture_resolution", map[string]int{"width": 160, "height": 120}},
	} {
		if err := client.Call(captureContext(t), tc.method, tc.params, nil); !errors.Is(err, apiwire.ErrUnavailable) {
			t.Fatalf("%s after close = %v", tc.method, err)
		}
	}
	var info captureInfoWire
	if err := client.Call(captureContext(t), "get_device_info", nil, &info); err != nil || info.Capture.Device != "/dev/video0" || info.Capture.Width != 128 || info.Capture.Running || info.Capture.Error != "unavailable" {
		t.Fatalf("closed capture info = %+v, %v", info, err)
	}
}
