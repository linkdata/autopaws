//go:build linux && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/internal/config"
	"github.com/linkdata/autopaws/serial"
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
	port.write = func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
			pressOnce.Do(func() { close(pressed) })
		} else if bytes.Equal(frame, ch9329.Keyboard(0)) {
			if deadline, cleanup := ctx.Deadline(); cleanup {
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
	if len(deadlines) != 2 || deadlines[0].IsZero() || deadlines[0] != deadlines[1] {
		t.Fatalf("runtime introduced another cleanup budget: %v", deadlines)
	}
	if _, closes, _ := port.snapshot(); closes != 1 {
		t.Fatalf("transport closed %d times", closes)
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
	if err := serveRuntime(context.Background(), cfg, io.Discard); err == nil {
		t.Fatal("occupied port unexpectedly started a daemon")
	}
}
