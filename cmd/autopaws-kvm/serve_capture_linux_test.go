//go:build linux && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/devices"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/internal/config"
	"github.com/linkdata/autopaws/serial"
	"github.com/linkdata/autopaws/v4l2"
)

type runtimeVideoSource struct {
	frames       []v4l2.Frame
	next         int // One source producer owns reads.
	started      chan struct{}
	closeStarted chan struct{}
	closed       chan struct{}
	readOnce     sync.Once
	closeOnce    sync.Once
	closeHook    func()
	closeErr     error
	closes       atomic.Int32
}

func newRuntimeVideoSource(frames ...v4l2.Frame) *runtimeVideoSource {
	return &runtimeVideoSource{frames: frames, started: make(chan struct{}), closeStarted: make(chan struct{}), closed: make(chan struct{})}
}

func (*runtimeVideoSource) Format() v4l2.Format {
	return v4l2.Format{Width: 128, Height: 96, FourCC: v4l2.PixFmtMJPEG, SizeImage: 1 << 20}
}

func (source *runtimeVideoSource) ReadFrame(ctx context.Context) (v4l2.Frame, error) {
	source.readOnce.Do(func() { close(source.started) })
	if err := ctx.Err(); err != nil {
		return v4l2.Frame{}, err
	}
	if source.next < len(source.frames) {
		frame := source.frames[source.next]
		source.next++
		return frame, nil
	}
	<-ctx.Done()
	return v4l2.Frame{}, ctx.Err()
}

func (source *runtimeVideoSource) Close() error {
	source.closeOnce.Do(func() {
		close(source.closeStarted)
		if source.closeHook != nil {
			source.closeHook()
		}
		source.closes.Add(1)
		close(source.closed)
	})
	return source.closeErr
}

func runtimeVideoJPEG(t *testing.T, brightness byte) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 128, 96))
	for i := range img.Pix {
		img.Pix[i] = brightness
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func runtimeVideoFrame(data []byte, micros int64) v4l2.Frame {
	return v4l2.Frame{Data: bytes.Clone(data), Timestamp: v4l2.Timestamp{Microseconds: micros}, Flags: v4l2.TimestampMonotonic}
}

type captureRuntime struct {
	client *apiclient.Client
	cancel context.CancelFunc
	done   chan struct{}
	err    error // Read only after done closes.
}

func startCaptureRuntime(t *testing.T, cfg config.Config, port hid.Transport, factory func(capture.Options) (*capture.Manager, error), discover func(context.Context) ([]devices.VideoDevice, error)) *captureRuntime {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	run := &captureRuntime{cancel: cancel, done: make(chan struct{})}
	bound := make(chan net.Listener, 1)
	go func() {
		run.err = serveWith(ctx, cfg, io.Discard, runtimeFactories{
			newTransport: func(serial.Options) (hid.Transport, error) { return port, nil },
			newCapture:   factory, discoverCapture: discover,
			listen: func(ctx context.Context, network, address string) (net.Listener, error) {
				listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
				if err == nil {
					bound <- listener
				}
				return listener, err
			},
		})
		close(run.done)
	}()
	select {
	case listener := <-bound:
		run.client = apiclient.New(listener.Addr().String())
	case <-run.done:
		t.Fatalf("capture runtime failed before listening: %v", run.err)
	}
	t.Cleanup(func() {
		cancel()
		receiveRuntime(t, run.done)
	})
	return run
}

func stopCaptureRuntime(t *testing.T, run *captureRuntime) error {
	t.Helper()
	run.cancel()
	receiveRuntime(t, run.done)
	return run.err
}

func TestRuntimeCaptureSelectionConfigAndLazyFreshAPI(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "explicit"}[explicit], func(t *testing.T) {
			cfg := runtimeConfig()
			cfg.CaptureWidth, cfg.CaptureHeight, cfg.Autocrop = 320, 240, !explicit
			wantDevice := "/dev/video2"
			if explicit {
				cfg.CaptureDevice = "/dev/v4l/by-id/selected-card"
				wantDevice = cfg.CaptureDevice
			}
			stale, fresh := runtimeVideoJPEG(t, 80), runtimeVideoJPEG(t, 180)
			source := newRuntimeVideoSource(runtimeVideoFrame(stale, 1000), runtimeVideoFrame(fresh, 2000))
			port := &runtimeTransport{}
			var discoveries, opens atomic.Int32
			var selected capture.Options
			var manager *capture.Manager
			run := startCaptureRuntime(t, cfg, port, func(options capture.Options) (*capture.Manager, error) {
				selected = options
				wantDiscoveries := int32(1)
				if explicit {
					wantDiscoveries = 0
				}
				if discoveries.Load() != wantDiscoveries {
					t.Errorf("startup discovery calls=%d, want%d", discoveries.Load(), wantDiscoveries)
				}
				options.MonotonicNow = func() (int64, error) { return 1_000_000, nil }
				options.OpenSource = func(_ context.Context, path string, requested v4l2.Options) (capture.Source, error) {
					opens.Add(1)
					if path != wantDevice || requested.Width != 320 || requested.Height != 240 {
						t.Errorf("source options=%q %+v", path, requested)
					}
					return source, nil
				}
				var err error
				manager, err = capture.New(options)
				return manager, err
			}, func(context.Context) ([]devices.VideoDevice, error) {
				discoveries.Add(1)
				return []devices.VideoDevice{{Path: "/dev/video0", Name: "Integrated Webcam"}, {Path: "/dev/video2", Name: "HDMI"}}, nil
			})
			if selected.Device != wantDevice || selected.Width != 320 || selected.Height != 240 || selected.Autocrop != cfg.Autocrop {
				t.Fatalf("capture configuration was not forwarded: %+v", selected)
			}
			var info struct {
				Capture struct {
					Device  string
					Running bool
					Error   string
				}
			}
			if err := runtimeCall(run.client, "get_device_info", nil, &info); err != nil || info.Capture.Device != wantDevice || info.Capture.Running || info.Capture.Error != "" {
				t.Fatalf("idle capture info=%+v,%v", info, err)
			}
			if prepares, _, _ := port.snapshot(); prepares != 0 || opens.Load() != 0 {
				t.Fatalf("information query opened hardware: prepares=%d videoOpens=%d", prepares, opens.Load())
			}
			var snapshot struct {
				JPEG          []byte `json:"jpeg_b64"`
				Width, Height int
			}
			if err := runtimeCall(run.client, "capture_frame", nil, &snapshot); err != nil || snapshot.Width != 128 || snapshot.Height != 96 || !bytes.Equal(snapshot.JPEG, fresh) {
				t.Fatalf("runtime did not return a fresh unmodified JPEG: %dx%d %v", snapshot.Width, snapshot.Height, err)
			}
			if err := stopCaptureRuntime(t, run); err != nil {
				t.Fatal(err)
			}
			if opens.Load() != 1 || source.closes.Load() != 1 || !errors.Is(manager.Info().Err, capture.ErrClosed) {
				t.Fatalf("runtime lost capture ownership: opens=%d closes=%d info=%+v", opens.Load(), source.closes.Load(), manager.Info())
			}
		})
	}
}

func TestRuntimeCaptureDiscoveryFailureKeepsInputAndSetters(t *testing.T) {
	for _, discoveryErr := range []error{nil, errors.New("discovery permission failure")} {
		name := "no video"
		if discoveryErr != nil {
			name = "discovery error"
		}
		t.Run(name, func(t *testing.T) {
			port := &runtimeTransport{}
			var opens atomic.Int32
			run := startCaptureRuntime(t, runtimeConfig(), port, func(options capture.Options) (*capture.Manager, error) {
				if options.Device != "" {
					t.Errorf("failed discovery selected %q", options.Device)
				}
				options.OpenSource = func(context.Context, string, v4l2.Options) (capture.Source, error) {
					opens.Add(1)
					return nil, errors.New("source opening is not needed for these calls")
				}
				return capture.New(options)
			}, func(context.Context) ([]devices.VideoDevice, error) { return nil, discoveryErr })
			if err := runtimeCall(run.client, "capture_frame", nil, nil); !errors.Is(err, apiwire.ErrUnavailable) {
				t.Fatalf("capture without selected video=%v", err)
			}
			if err := runtimeCall(run.client, "send_key", map[string]string{"key": "a"}, nil); err != nil {
				t.Fatal("capture failure disabled HID", err)
			}
			if err := runtimeCall(run.client, "set_capture_device", map[string]string{"device": "/dev/repair"}, nil); err != nil {
				t.Fatal("device setter could not repair missing video", err)
			}
			if err := runtimeCall(run.client, "set_capture_resolution", map[string]int{"width": 640, "height": 480}, nil); err != nil {
				t.Fatal("resolution setter could not repair idle selection", err)
			}
			var info struct {
				Capture struct {
					Device string
					Width  int `json:"requested_width"`
					Height int `json:"requested_height"`
				}
			}
			if err := runtimeCall(run.client, "get_device_info", nil, &info); err != nil || info.Capture.Device != "/dev/repair" || info.Capture.Width != 640 || info.Capture.Height != 480 || opens.Load() != 0 {
				t.Fatalf("idle repair settings=%+v opens=%d error=%v", info, opens.Load(), err)
			}
			if err := stopCaptureRuntime(t, run); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeCaptureStartupFailureJoinsOwnedCleanup(t *testing.T) {
	startupErr, serialCloseErr, videoCloseErr := errors.New("startup failure"), errors.New("serial close failure"), errors.New("video close failure")
	for _, stage := range []string{"constructor", "listen", "serve"} {
		t.Run(stage, func(t *testing.T) {
			port := &runtimeTransport{closeErr: serialCloseErr}
			source := newRuntimeVideoSource()
			source.closeErr = videoCloseErr
			var manager *capture.Manager
			var subscribed *capture.Subscription
			listened := false
			err := serveWith(t.Context(), runtimeConfig(), io.Discard, runtimeFactories{
				newTransport: func(serial.Options) (hid.Transport, error) { return port, nil },
				newCapture: func(options capture.Options) (*capture.Manager, error) {
					if stage == "constructor" {
						return nil, startupErr
					}
					// Inject an owner that already has an active source, so startup
					// failure must join real manager/source cleanup rather than just
					// close an unopened placeholder.
					options.Device = "/dev/fake"
					options.OpenSource = func(context.Context, string, v4l2.Options) (capture.Source, error) { return source, nil }
					var err error
					manager, err = capture.New(options)
					if err != nil {
						return nil, err
					}
					subscribed, err = manager.Subscribe()
					if err != nil {
						return nil, err
					}
					receiveRuntime(t, source.started)
					return manager, nil
				},
				listen: func(context.Context, string, string) (net.Listener, error) {
					listened = true
					if stage == "listen" {
						return nil, startupErr
					}
					return failedListener{startupErr}, nil
				},
			})
			if !errors.Is(err, startupErr) || !errors.Is(err, serialCloseErr) {
				t.Fatalf("startup/serial cleanup error lost: %v", err)
			}
			if stage == "constructor" {
				if listened || manager != nil {
					t.Fatal("constructor failure started a listener or created a manager")
				}
			} else {
				if !listened || !errors.Is(err, videoCloseErr) || source.closes.Load() != 1 || !errors.Is(manager.Info().Err, capture.ErrClosed) {
					t.Fatalf("startup lost video ownership: closes=%d info=%+v error=%v", source.closes.Load(), manager.Info(), err)
				}
				_ = subscribed.Close()
			}
			if prepares, closes, _ := port.snapshot(); prepares != 0 || closes != 1 {
				t.Fatalf("startup failure prepared serial or failed to close it: prepares=%d closes=%d", prepares, closes)
			}
		})
	}
}

type runtimeClosingTransport struct {
	*runtimeTransport
	closed chan struct{}
	once   sync.Once
}

func (port *runtimeClosingTransport) Close() error {
	err := port.runtimeTransport.Close()
	port.once.Do(func() { close(port.closed) })
	return err
}

func TestRuntimeShutdownStartsBothOwnersAndJoinsBoth(t *testing.T) {
	for _, releaseSerialFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "video closes first", true: "serial closes first"}[releaseSerialFirst], func(t *testing.T) {
			serialReleaseStarted := make(chan struct{})
			finishSerial, finishVideo := make(chan struct{}), make(chan struct{})
			var releaseSerialOnce, releaseVideoOnce, serialStartedOnce sync.Once
			releaseSerial := func() { releaseSerialOnce.Do(func() { close(finishSerial) }) }
			releaseVideo := func() { releaseVideoOnce.Do(func() { close(finishVideo) }) }
			defer releaseSerial()
			defer releaseVideo()
			port := &runtimeClosingTransport{runtimeTransport: &runtimeTransport{}, closed: make(chan struct{})}
			port.write = func(ctx context.Context, frame []byte) error {
				if bytes.Equal(frame, ch9329.Keyboard(0)) {
					serialStartedOnce.Do(func() { close(serialReleaseStarted) })
					<-finishSerial // model a synchronous serial cleanup kernel call
				}
				return ctx.Err()
			}
			data := runtimeVideoJPEG(t, 180)
			source := newRuntimeVideoSource(runtimeVideoFrame(data, 2000))
			source.closeHook = func() { <-finishVideo } // model a synchronous video close
			run := startCaptureRuntime(t, runtimeConfig(), port, func(options capture.Options) (*capture.Manager, error) {
				options.Device = "/dev/fake"
				options.MonotonicNow = func() (int64, error) { return 1_000_000, nil }
				options.OpenSource = func(context.Context, string, v4l2.Options) (capture.Source, error) { return source, nil }
				return capture.New(options)
			}, nil)
			if err := runtimeCall(run.client, "mouse_down", map[string]string{"button": "right"}, nil); err != nil {
				t.Fatal(err)
			}
			if err := runtimeCall(run.client, "capture_frame", nil, nil); err != nil {
				t.Fatal(err)
			}
			run.cancel()
			// Neither owner can complete until the test releases its fake kernel
			// operation. Sequential shutdown in either order deadlocks this pair.
			receiveRuntime(t, serialReleaseStarted)
			receiveRuntime(t, source.closeStarted)
			select {
			case <-run.done:
				t.Fatal("runtime abandoned blocked hardware cleanup")
			default:
			}
			if releaseSerialFirst {
				releaseSerial()
				receiveRuntime(t, port.closed)
			} else {
				releaseVideo()
				receiveRuntime(t, source.closed)
			}
			select {
			case <-run.done:
				t.Fatal("runtime returned with the other hardware owner still blocked")
			default:
			}
			releaseSerial()
			releaseVideo()
			if err := stopCaptureRuntime(t, run); err != nil {
				t.Fatal(err)
			}
			if source.closes.Load() != 1 {
				t.Fatalf("source closed %d times", source.closes.Load())
			}
			_, closes, frames := port.snapshot()
			reset := ch9329.ReleaseAll()
			if closes != 1 || len(frames) < 3 || !bytes.Equal(frames[len(frames)-2], reset[0]) || !bytes.Equal(frames[len(frames)-1], reset[1]) {
				t.Fatalf("serial release/close incomplete: closes=%d frames=%x", closes, frames)
			}
		})
	}
}
