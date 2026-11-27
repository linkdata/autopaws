//go:build (linux || darwin || windows) && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/internal/config"
	"github.com/linkdata/autopaws/serial"
	"github.com/linkdata/autopaws/v4l2"
)

type runtimeTransport struct {
	mu       sync.Mutex
	frames   [][]byte
	prepares int
	closes   int
	closeErr error
	write    func(context.Context, []byte) error
}

func (port *runtimeTransport) Prepare(ctx context.Context) (uint64, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.prepares++
	return 1, ctx.Err()
}

func (port *runtimeTransport) WriteFrame(ctx context.Context, frame []byte) error {
	port.mu.Lock()
	port.frames = append(port.frames, bytes.Clone(frame))
	hook := port.write
	port.mu.Unlock()
	if hook != nil {
		return hook(ctx, frame)
	}
	return ctx.Err()
}

func (port *runtimeTransport) Close() error {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.closes++
	return port.closeErr
}

func (port *runtimeTransport) snapshot() (int, int, [][]byte) {
	port.mu.Lock()
	defer port.mu.Unlock()
	return port.prepares, port.closes, slices.Clone(port.frames)
}

func runtimeConfig() config.Config {
	cfg := config.Defaults()
	cfg.API, cfg.SerialDevice = "127.0.0.1:0", "/not-a-real-serial-device"
	cfg.ScreenWidth, cfg.ScreenHeight, cfg.MouseButtonMode = 1280, 720, "relative"
	return cfg
}

func receiveRuntime[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case result := <-channel:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("controlled runtime test did not complete")
		var empty T
		return empty
	}
}

func runtimeCall(client *apiclient.Client, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.Call(ctx, method, params, result)
}

// startRuntime returns the actual bound listener, so callers need not guess an
// ephemeral port or race on a sleep while waiting for daemon startup.
func startRuntime(t *testing.T, port *runtimeTransport) (context.CancelFunc, <-chan error, net.Listener) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	bound := make(chan net.Listener, 1)
	done := make(chan error, 1)
	factories := runtimeFactories{
		newTransport: func(options serial.Options) (hid.Transport, error) {
			if options.Path != "/not-a-real-serial-device" || options.Baud != 9600 || options.Logger == nil {
				t.Error("runtime did not forward serial settings and the independent key logger")
			}
			return port, nil
		},
		listen: func(ctx context.Context, network, address string) (net.Listener, error) {
			if network != "tcp" || address != "127.0.0.1:0" {
				t.Errorf("runtime changed listen settings: %q %q", network, address)
			}
			listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
			if err == nil {
				bound <- listener
			}
			return listener, err
		},
	}
	cfg := runtimeConfig()
	cfg.DebugKeys = true
	go func() { done <- serveWith(ctx, cfg, io.Discard, factories) }()
	listener := receiveRuntime(t, bound)
	t.Cleanup(cancel)
	return cancel, done, listener
}

func TestRuntimeInfoIsLazyAndShutdownClosesIdleConnection(t *testing.T) {
	port := &runtimeTransport{}
	cancel, done, listener := startRuntime(t, port)
	client := apiclient.New(listener.Addr().String())
	var info struct {
		Serial struct {
			Device string `json:"device"`
			Baud   int    `json:"baud"`
		} `json:"serial"`
		Config struct {
			Width  int `json:"screen_width"`
			Height int `json:"screen_height"`
		} `json:"config"`
	}
	if err := runtimeCall(client, "get_device_info", nil, &info); err != nil {
		t.Fatal(err)
	}
	if info.Serial.Device != runtimeConfig().SerialDevice || info.Serial.Baud != 9600 || info.Config.Width != 1280 || info.Config.Height != 720 {
		t.Fatalf("runtime info settings were not forwarded: %+v", info)
	}
	if prepares, _, _ := port.snapshot(); prepares != 0 {
		t.Fatal("device info opened serial hardware")
	}
	idle, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	if _, err := idle.Write([]byte(`{"id":`)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := receiveRuntime(t, done); err != nil {
		t.Fatal(err)
	}
	if err := idle.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, readErr := idle.Read(make([]byte, 1))
	var networkErr net.Error
	if readErr == nil || errors.As(readErr, &networkErr) && networkErr.Timeout() {
		t.Fatalf("shutdown left a partial-line connection open: %v", readErr)
	}
	if prepares, closes, frames := port.snapshot(); prepares != 0 || closes != 1 || len(frames) != 0 {
		t.Fatalf("unused shutdown touched hardware or failed to close: %d/%d %x", prepares, closes, frames)
	}
}

func TestRuntimeMouseModeAndCaptureBindingWithoutDemand(t *testing.T) {
	for _, mode := range []string{"legacy", "extended"} {
		t.Run(mode, func(t *testing.T) {
			cfg := runtimeConfig()
			cfg.MouseMode, cfg.CaptureDevice = mode, "/dev/test-capture"
			port := &runtimeTransport{}
			var opens atomic.Int32
			var observe func(string, capture.Metadata)
			run := startCaptureRuntime(t, cfg, port, func(options capture.Options) (*capture.Manager, error) {
				observe = options.OnFrame
				options.OpenSource = func(context.Context, string, v4l2.Options) (capture.Source, error) {
					opens.Add(1)
					return nil, errors.New("unexpected capture demand")
				}
				return capture.New(options)
			}, nil)
			var mapping hid.MappingState
			if err := runtimeCall(run.client, "get_mouse_mapping", nil, &mapping); err != nil || mapping.Mode != mode || mapping.Ready != (mode == "legacy") || mapping.Revision != 1 {
				t.Fatalf("startup mapping=%+v error=%v", mapping, err)
			}
			if mode == "extended" {
				if err := runtimeCall(run.client, "mouse_move", map[string]int{"x": 10, "y": 10}, nil); !errors.Is(err, apiwire.ErrMappingUnavailable) {
					t.Fatalf("unconfigured extended movement=%v", err)
				}
			}
			if observe == nil {
				t.Fatal("runtime omitted the frame observer")
			}
			metadata := capture.Metadata{Width: 128, Height: 96, Generation: 1, DeliverySequence: 10}
			observe(cfg.CaptureDevice, metadata)
			if err := runtimeCall(run.client, "get_mouse_mapping", nil, &mapping); err != nil || mapping.Capture.Width != 128 || mapping.Capture.Height != 96 || mapping.Capture.Sequence != 10 || mapping.Mode != mode {
				t.Fatalf("frame context not shared with API: %+v %v", mapping, err)
			}
			revision := mapping.Revision
			metadata.DeliverySequence++
			observe(cfg.CaptureDevice, metadata)
			if err := runtimeCall(run.client, "get_mouse_mapping", nil, &mapping); err != nil || mapping.Revision != revision || mapping.Capture.Sequence != 11 || mapping.Capture.Width != 128 {
				t.Fatalf("ordinary frame or metadata watcher invalidated context: %+v %v", mapping, err)
			}
			if err := stopCaptureRuntime(t, run); err != nil {
				t.Fatal(err)
			}
			if prepares, closes, frames := port.snapshot(); prepares != 0 || closes != 1 || len(frames) != 0 || opens.Load() != 0 {
				t.Fatalf("passive mapping opened hardware or lost ownership: prepares%d closes%d frames%x opens%d", prepares, closes, frames, opens.Load())
			}
		})
	}
}

func TestMouseMappingWatcherInvalidatesSettingsWithoutCaptureDemand(t *testing.T) {
	port := &runtimeTransport{}
	controller := hid.NewController(port)
	t.Cleanup(func() { _ = controller.Close() })
	mouse, err := hid.NewMouse(controller, hid.MouseOptions{Width: 128, Height: 96, MappingMode: "extended"})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	video, err := capture.New(capture.Options{
		Device: "/dev/first", Width: 128, Height: 96,
		OpenSource: func(context.Context, string, v4l2.Options) (capture.Source, error) {
			opens.Add(1)
			return nil, errors.New("unexpected capture demand")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = video.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		watchMouseMapping(ctx, mouse, video, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	t.Cleanup(func() { cancel(); receiveRuntime(t, stopped) })
	await := func(generation uint64, width int) hid.MappingState {
		t.Helper()
		for {
			state, changed := mouse.WatchMapping()
			if state.Capture.Generation == generation && state.Capture.Width == width {
				return state
			}
			receiveRuntime(t, changed)
		}
	}
	await(1, 0)
	frame := hid.MappingCapture{Device: "/dev/first", Width: 128, Height: 96, Generation: 1, Sequence: 10}
	if err := mouse.SetMappingCapture(t.Context(), frame); err != nil {
		t.Fatal(err)
	}
	before := await(1, 128)
	if err := video.SetDevice(t.Context(), "/dev/second"); err != nil {
		t.Fatal(err)
	}
	changed := await(2, 0)
	if changed.Capture.Device != "/dev/second" || changed.Mode != "extended" || changed.Ready || changed.Revision <= before.Revision {
		t.Fatalf("idle device change retained old mapping: %+v", changed)
	}
	// A late old-source observer must not replace the new selection barrier.
	frame.Sequence++
	if err := mouse.SetMappingCapture(t.Context(), frame); err != nil {
		t.Fatal(err)
	}
	if got := mouse.Mapping(); got.Capture != changed.Capture || got.Revision != changed.Revision {
		t.Fatalf("old-source frame replaced the selection barrier: %+v", got)
	}
	frame.Device, frame.Generation = "/dev/second", 2
	if err := mouse.SetMappingCapture(t.Context(), frame); err != nil {
		t.Fatal(err)
	}
	formatted := await(2, 128)
	if formatted.Capture.Sequence != 11 || formatted.Revision != changed.Revision {
		t.Fatalf("formatted frame did not complete the new context: %+v", formatted)
	}
	cancel()
	receiveRuntime(t, stopped)
	if opens.Load() != 0 {
		t.Fatal("metadata watcher subscribed to capture")
	}
	if prepares, _, frames := port.snapshot(); prepares != 0 || len(frames) != 0 {
		t.Fatal("passive context changes opened input hardware")
	}
}

func TestMouseMappingWatcherBindsContextDespiteReleaseFailure(t *testing.T) {
	port := &runtimeTransport{}
	controller := hid.NewController(port)
	t.Cleanup(func() { _ = controller.Close() })
	mouse, err := hid.NewMouse(controller, hid.MouseOptions{Width: 128, Height: 96})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	video, err := capture.New(capture.Options{
		Device: "/dev/first", Width: 128, Height: 96,
		OpenSource: func(context.Context, string, v4l2.Options) (capture.Source, error) {
			opens.Add(1)
			return nil, errors.New("unexpected capture demand")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = video.Close() })
	if err := mouse.SetMappingCapture(t.Context(), hid.MappingCapture{Device: "/dev/first", Width: 128, Height: 96, Generation: 1, Sequence: 1}); err != nil {
		t.Fatal(err)
	}
	if err := mouse.Down(t.Context(), "left", hid.ButtonOptions{}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("transient release failure")
	var failed atomic.Bool
	port.write = func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0)) && failed.CompareAndSwap(false, true) {
			return failure
		}
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		watchMouseMapping(ctx, mouse, video, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	t.Cleanup(func() { cancel(); receiveRuntime(t, stopped) })
	if err := video.SetDevice(t.Context(), "/dev/second"); err != nil {
		t.Fatal(err)
	}
	for {
		state, changed := mouse.WatchMapping()
		if state.Capture.Generation == 2 {
			if state.Capture.Device != "/dev/second" || !state.Ready || state.Message == "" || !failed.Load() {
				t.Fatalf("watcher did not recover the selection: %+v", state)
			}
			break
		}
		receiveRuntime(t, changed)
	}
	if video.Info().Running || opens.Load() != 0 {
		t.Fatal("retry opened capture without demand")
	}
}

func TestRuntimeShutdownReleasesKeyboardAndEarlierMouseDown(t *testing.T) {
	port := &runtimeTransport{}
	pressed := make(chan struct{})
	var once sync.Once
	port.write = func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
			once.Do(func() { close(pressed) })
		}
		return ctx.Err()
	}
	cancel, done, listener := startRuntime(t, port)
	client := apiclient.New(listener.Addr().String())
	if err := runtimeCall(client, "mouse_down", map[string]any{"button": "right"}, nil); err != nil {
		t.Fatal(err)
	}
	request := make(chan error, 1)
	go func() {
		request <- client.Call(context.Background(), "type_text", map[string]any{"text": "ab", "char_delay_ms": 60000}, nil)
	}()
	receiveRuntime(t, pressed)
	cancel()
	if err := receiveRuntime(t, done); err != nil {
		t.Fatal(err)
	}
	if err := receiveRuntime(t, request); err == nil {
		t.Fatal("shutdown completed an interrupted typing request")
	}
	_, closes, frames := port.snapshot()
	reset := ch9329.ReleaseAll()
	if closes != 1 || len(frames) < 4 || !bytes.Equal(frames[len(frames)-2], reset[0]) || !bytes.Equal(frames[len(frames)-1], reset[1]) {
		t.Fatalf("shutdown did not release both before closing: closes=%d frames=%x", closes, frames)
	}
}

func TestRuntimeShutdownSharesInFlightCleanupBudget(t *testing.T) {
	port := &runtimeTransport{}
	pressed, cleaning := make(chan struct{}), make(chan struct{})
	var pressOnce, cleanOnce sync.Once
	var deadlines []time.Time
	var cleanupStarted time.Time
	port.write = func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
			pressOnce.Do(func() { close(pressed) })
		} else if bytes.Equal(frame, ch9329.Keyboard(0)) {
			if deadline, cleanup := ctx.Deadline(); cleanup {
				cleanupStarted = time.Now()
				deadlines = append(deadlines, deadline)
				cleanOnce.Do(func() { close(cleaning) })
				<-ctx.Done()
			}
		} else if bytes.Equal(frame, ch9329.MouseRelative(0, 0, 0, 0)) {
			deadline, _ := ctx.Deadline()
			deadlines = append(deadlines, deadline)
		}
		return ctx.Err()
	}
	cancel, done, listener := startRuntime(t, port)
	client := apiclient.New(listener.Addr().String())
	ctx, stopRequest := context.WithCancel(context.Background())
	defer stopRequest()
	request := make(chan error, 1)
	go func() {
		request <- client.Call(ctx, "type_text", map[string]any{"text": "ab", "char_delay_ms": 60000}, nil)
	}()
	receiveRuntime(t, pressed)
	stopRequest()
	receiveRuntime(t, cleaning)
	cancel()
	if err := receiveRuntime(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runtime hid cleanup failure: %v", err)
	}
	if err := receiveRuntime(t, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("client cancellation identity lost: %v", err)
	}
	if len(deadlines) != 1 || !deadlines[0].After(cleanupStarted) || deadlines[0].Sub(cleanupStarted) > time.Second || time.Now().Before(deadlines[0]) {
		t.Fatalf("runtime introduced another cleanup budget: %v", deadlines)
	}
	if _, closes, frames := port.snapshot(); closes != 1 || len(frames) != 2 || !bytes.Equal(frames[1], ch9329.Keyboard(0)) {
		t.Fatalf("expired cleanup retried input or lost transport ownership: closes=%d frames=%x", closes, frames)
	}
}

func TestRuntimeRetainsSlowOperationUntilItReturns(t *testing.T) {
	port := &runtimeTransport{}
	entered, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	port.write = func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
			once.Do(func() { close(entered) })
			<-release // model a kernel operation that cannot be canceled
		}
		return ctx.Err()
	}
	cancel, done, listener := startRuntime(t, port)
	client := apiclient.New(listener.Addr().String())
	request := make(chan error, 1)
	go func() { request <- client.Call(context.Background(), "send_key", map[string]any{"key": "a"}, nil) }()
	receiveRuntime(t, entered)
	cancel()
	receiveRuntime(t, request) // network cancellation can finish independently
	if _, closes, _ := port.snapshot(); closes != 0 {
		t.Fatal("runtime closed a transport with an operation still executing")
	}
	select {
	case err := <-done:
		t.Fatalf("runtime abandoned its executing operation: %v", err)
	default:
	}
	releaseOnce.Do(func() { close(release) })
	if err := receiveRuntime(t, done); err != nil {
		t.Fatal(err)
	}
	if _, closes, _ := port.snapshot(); closes != 1 {
		t.Fatalf("transport closed %d times", closes)
	}
}

type failedListener struct{ failure error }

func (listener failedListener) Accept() (net.Conn, error) { return nil, listener.failure }
func (listener failedListener) Close() error              { return nil }
func (listener failedListener) Addr() net.Addr            { return &net.TCPAddr{} }

func TestRuntimeStartupAndServeFailuresCloseOwnedTransport(t *testing.T) {
	failure := errors.New("listener failure")
	closeFailure := errors.New("transport close failure")
	for _, stage := range []string{"listen", "serve", "mouse"} {
		t.Run(stage, func(t *testing.T) {
			port := &runtimeTransport{closeErr: closeFailure}
			cfg := runtimeConfig()
			if stage == "mouse" {
				cfg.ScreenWidth = 0
			}
			err := serveWith(context.Background(), cfg, io.Discard, runtimeFactories{
				newTransport: func(serial.Options) (hid.Transport, error) { return port, nil },
				listen: func(context.Context, string, string) (net.Listener, error) {
					if stage == "listen" {
						return nil, failure
					}
					return failedListener{failure}, nil
				},
			})
			want := failure
			if stage == "mouse" {
				want = hid.ErrScreenSize
			}
			if !errors.Is(err, want) || !errors.Is(err, closeFailure) {
				t.Fatalf("lost startup/close failure: %v", err)
			}
			if prepares, closes, frames := port.snapshot(); prepares != 0 || closes != 1 || len(frames) != 0 {
				t.Fatalf("startup failure opened hardware or lost ownership: %d/%d %x", prepares, closes, frames)
			}
		})
	}
}

func TestRuntimePreStartupFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serveWith(ctx, runtimeConfig(), io.Discard, runtimeFactories{}); err != nil {
		t.Fatal(err)
	}
	cfg := runtimeConfig()
	cfg.TargetLayout = "unknown"
	if err := serveWith(context.Background(), cfg, io.Discard, runtimeFactories{}); !errors.Is(err, hid.ErrUnknownLayout) {
		t.Fatalf("layout error was lost: %v", err)
	}
	cfg = runtimeConfig()
	cfg.SerialBaud = 1
	if err := serveRuntime(context.Background(), cfg, io.Discard); !errors.Is(err, serial.ErrBaud) {
		t.Fatalf("transport construction error was lost: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg = runtimeConfig()
	cfg.API = listener.Addr().String()
	factories := runtimeFactories{
		newTransport: func(serial.Options) (hid.Transport, error) { return &runtimeTransport{}, nil },
		listen:       (&net.ListenConfig{}).Listen,
	}
	if err := serveWith(context.Background(), cfg, io.Discard, factories); err == nil {
		t.Fatal("occupied port unexpectedly started a daemon")
	}
}
