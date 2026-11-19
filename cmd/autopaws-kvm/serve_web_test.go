//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/devices"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/internal/config"
	"github.com/linkdata/autopaws/serial"
	"github.com/linkdata/autopaws/v4l2"
	"github.com/linkdata/autopaws/web"
)

type viewerRuntime struct {
	cancel  context.CancelFunc
	done    chan struct{}
	err     error // Read after done.
	api     net.Listener
	web     net.Listener
	manager *capture.Manager
}

func startViewerRuntime(t *testing.T, cfg config.Config, port hid.Transport, source *runtimeVideoSource, wrap func(string, net.Listener) net.Listener) *viewerRuntime {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	run := &viewerRuntime{cancel: cancel, done: make(chan struct{})}
	t.Cleanup(func() { cancel(); receiveRuntime(t, run.done) })
	type endpoint struct {
		kind     string
		listener net.Listener
	}
	bound := make(chan endpoint, 2)
	go func() {
		listens := 0
		run.err = serveWith(ctx, cfg, io.Discard, runtimeFactories{
			newTransport: func(serial.Options) (hid.Transport, error) { return port, nil },
			newCapture: func(options capture.Options) (*capture.Manager, error) {
				options.Device = "/dev/fake"
				options.OpenSource = func(context.Context, string, v4l2.Options) (capture.Source, error) { return source, nil }
				var err error
				run.manager, err = capture.New(options)
				return run.manager, err
			},
			discoverCapture: func(context.Context) ([]devices.VideoDevice, error) {
				return []devices.VideoDevice{{Path: "/dev/fake", Name: "Fixture HDMI"}}, nil
			},
			listen: func(ctx context.Context, network, address string) (net.Listener, error) {
				kind := "web"
				if listens == 0 && cfg.API != "" {
					kind = "API"
				}
				listens++
				listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
				if err == nil {
					if wrap != nil {
						listener = wrap(kind, listener)
					}
					bound <- endpoint{kind, listener}
				}
				return listener, err
			},
		})
		close(run.done)
	}()
	count := 0
	if cfg.API != "" {
		count++
	}
	if cfg.Web != "" {
		count++
	}
	for range count {
		select {
		case endpoint := <-bound:
			if endpoint.kind == "API" {
				run.api = endpoint.listener
			} else {
				run.web = endpoint.listener
			}
		case <-run.done:
			t.Fatalf("runtime failed before all listeners bound: %v", run.err)
		}
	}
	return run
}

func stopViewerRuntime(t *testing.T, run *viewerRuntime) error {
	t.Helper()
	run.cancel()
	receiveRuntime(t, run.done)
	return run.err
}

func dialRuntimeViewer(t *testing.T, listener net.Listener) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+listener.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	kind, data, err := conn.Read(ctx)
	if err != nil || kind != websocket.MessageText || !bytes.Contains(data, []byte(`"type":"config"`)) {
		t.Fatalf("runtime viewer config=%s,%v", data, err)
	}
	return conn
}

func runtimeViewerInput(t *testing.T, conn *websocket.Conn, data string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeOptionalListenersShareCaptureAndInput(t *testing.T) {
	for _, mode := range []string{"API", "web", "both"} {
		t.Run(mode, func(t *testing.T) {
			cfg := runtimeConfig()
			cfg.CaptureDevice, cfg.WebFPS = "/dev/fake", 120
			if mode == "web" {
				cfg.API = ""
			}
			if mode != "API" {
				cfg.Web = "127.0.0.1:0"
			}
			port := &runtimeTransport{}
			changes := make(chan []byte, 10)
			port.write = func(ctx context.Context, frame []byte) error { changes <- bytes.Clone(frame); return ctx.Err() }
			jpegData := runtimeVideoJPEG(t, 180)
			source := newRuntimeVideoSource(runtimeVideoFrame(jpegData, 2000))
			run := startViewerRuntime(t, cfg, port, source, nil)
			var client *apiclient.Client
			if run.api != nil {
				client = apiclient.New(run.api.Addr().String())
				if err := runtimeCall(client, "ping", nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			if run.web != nil {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+run.web.Addr().String()+"/manifest.json", nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-cache" {
					t.Fatalf("runtime asset status/header=%v", response)
				}
				conn := dialRuntimeViewer(t, run.web)
				kind, frame, err := conn.Read(ctx)
				if err != nil || kind != websocket.MessageBinary || len(frame) != 64+len(jpegData) || !bytes.Equal(frame[64:], jpegData) || binary.LittleEndian.Uint32(frame[32:]) != 128 {
					t.Fatalf("runtime did not share pipeline frame:%dbytes,%v", len(frame), err)
				}
				if client != nil {
					if err := runtimeCall(client, "mouse_down", map[string]string{"button": "right"}, nil); err != nil {
						t.Fatal(err)
					}
					if got := receiveRuntime(t, changes); !bytes.Equal(got, ch9329.MouseRelative(2, 0, 0, 0)) {
						t.Fatal("API did not use configured shared Mouse")
					}
				}
				runtimeViewerInput(t, conn, `{"type":"mousemove","x":1,"y":1,"buttons":0}`)
				button := byte(0)
				if client != nil {
					button = 2
				}
				if got := receiveRuntime(t, changes); !bytes.Equal(got, ch9329.MouseAbsolute(button, 1, 1, 0)) {
					t.Fatalf("web pointer was double-scaled or lost actual held bits:%x", got)
				}
				_ = conn.CloseNow()
			}
			if err := stopViewerRuntime(t, run); err != nil {
				t.Fatal(err)
			}
			if run.web == nil && source.closes.Load() != 0 || run.web != nil && source.closes.Load() != 1 {
				t.Fatal("runtime lost lazy/shared capture ownership")
			}
			if _, closes, _ := port.snapshot(); closes != 1 || !errors.Is(run.manager.Info().Err, capture.ErrClosed) {
				t.Fatal("runtime did not join both owners")
			}
		})
	}
}

type acceptObservedListener struct {
	net.Listener
	accepts atomic.Int32
}

func (listener *acceptObservedListener) Accept() (net.Conn, error) {
	listener.accepts.Add(1)
	return listener.Listener.Accept()
}

func TestRuntimeBindsEveryRequestedAddressBeforeAccepting(t *testing.T) {
	failure := errors.New("second bind failed")
	port := &runtimeTransport{}
	cfg := runtimeConfig()
	cfg.Web = "127.0.0.1:0"
	var first *acceptObservedListener
	var manager *capture.Manager
	binds := 0
	err := serveWith(t.Context(), cfg, io.Discard, runtimeFactories{
		newTransport: func(serial.Options) (hid.Transport, error) { return port, nil },
		newCapture: func(options capture.Options) (*capture.Manager, error) {
			var err error
			manager, err = capture.New(options)
			return manager, err
		},
		listen: func(ctx context.Context, network, address string) (net.Listener, error) {
			binds++
			if binds == 2 {
				return nil, failure
			}
			listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
			first = &acceptObservedListener{Listener: listener}
			return first, err
		},
	})
	if !errors.Is(err, failure) || binds != 2 || first.accepts.Load() != 0 {
		t.Fatalf("partial startup accepted clients: binds%d accepts%d error%v", binds, first.accepts.Load(), err)
	}
	if conn, err := net.DialTimeout("tcp", first.Addr().String(), time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("failed second bind left first address open")
	}
	if prepares, closes, frames := port.snapshot(); prepares != 0 || closes != 1 || len(frames) != 0 || !errors.Is(manager.Info().Err, capture.ErrClosed) {
		t.Fatal("partial bind failure opened hardware or lost owners")
	}
}

type closeFailedRuntimeListener struct {
	failedListener
	closeErr error
}

func (listener closeFailedRuntimeListener) Close() error { return listener.closeErr }

func TestRuntimeListenerCloseFailureIsJoined(t *testing.T) {
	serveFailure, closeFailure := errors.New("Accept failed"), errors.New("listener close failed")
	port := &runtimeTransport{}
	err := serveWith(t.Context(), runtimeConfig(), io.Discard, runtimeFactories{
		newTransport: func(serial.Options) (hid.Transport, error) { return port, nil },
		listen: func(context.Context, string, string) (net.Listener, error) {
			return closeFailedRuntimeListener{failedListener{serveFailure}, closeFailure}, nil
		},
	})
	if !errors.Is(err, serveFailure) || !errors.Is(err, closeFailure) {
		t.Fatalf("network shutdown error was lost: %v", err)
	}
	if _, closes, _ := port.snapshot(); closes != 1 {
		t.Fatal("listener error lost transport ownership")
	}
}

type runtimeFatalListener struct {
	net.Listener
	failure error
	failed  atomic.Bool
}

func (listener *runtimeFatalListener) Accept() (net.Conn, error) {
	conn, err := listener.Listener.Accept()
	if err != nil && listener.failed.Load() {
		return nil, listener.failure
	}
	return conn, err
}

func (listener *runtimeFatalListener) fail() {
	listener.failed.Store(true)
	_ = listener.Close()
}

func TestRuntimeAnyFatalListenerClosesOtherServiceAndHijackedWebSocket(t *testing.T) {
	for _, failedKind := range []string{"API", "web"} {
		t.Run(failedKind, func(t *testing.T) {
			failure := errors.New("controlled Accept failure")
			cfg := runtimeConfig()
			cfg.Web, cfg.CaptureDevice = "127.0.0.1:0", "/dev/fake"
			port := &runtimeTransport{}
			pressed := make(chan struct{})
			var once sync.Once
			port.write = func(ctx context.Context, frame []byte) error {
				if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
					once.Do(func() { close(pressed) })
				}
				return ctx.Err()
			}
			source := newRuntimeVideoSource()
			var fatal *runtimeFatalListener
			run := startViewerRuntime(t, cfg, port, source, func(kind string, listener net.Listener) net.Listener {
				if kind == failedKind {
					fatal = &runtimeFatalListener{Listener: listener, failure: failure}
					return fatal
				}
				return listener
			})
			conn := dialRuntimeViewer(t, run.web)
			runtimeViewerInput(t, conn, `{"type":"keydown","code":"KeyA"}`)
			receiveRuntime(t, pressed)
			fatal.fail()
			receiveRuntime(t, run.done)
			if !errors.Is(run.err, failure) {
				t.Fatalf("fatal Serve failure hidden: %v", run.err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if _, _, err := conn.Read(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("hijacked WebSocket survived other service failure:%v", err)
			}
			for _, listener := range []net.Listener{run.api, run.web} {
				if conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second); err == nil {
					_ = conn.Close()
					t.Fatal("fatal Serve failure left another listener open")
				}
			}
			_, closes, frames := port.snapshot()
			if closes != 1 || len(frames) < 3 || !bytes.Equal(frames[len(frames)-2], ch9329.Keyboard(0)) || !bytes.Equal(frames[len(frames)-1], ch9329.MouseRelative(0, 0, 0, 0)) || source.closes.Load() != 1 {
				t.Fatalf("fatal shutdown lost input/video ownership: frames%x closes%d", frames, closes)
			}
		})
	}
}

func TestRuntimeWebStartupErrorsAndCanceledProductionCommands(t *testing.T) {
	for _, mode := range []string{"web-only bind", "web constructor"} {
		t.Run(mode, func(t *testing.T) {
			port := &runtimeTransport{}
			cfg := runtimeConfig()
			cfg.API, cfg.Web = "", "127.0.0.1:0"
			failure := errors.New("web bind failed")
			if mode == "web constructor" {
				cfg.WebFPS = 121
			}
			err := serveWith(t.Context(), cfg, io.Discard, runtimeFactories{
				newTransport: func(serial.Options) (hid.Transport, error) { return port, nil },
				newCapture:   capture.New,
				listen: func(context.Context, string, string) (net.Listener, error) {
					if mode == "web constructor" {
						t.Error("invalid constructor reached listener")
					}
					return nil, failure
				},
			})
			want := failure
			if mode == "web constructor" {
				want = web.ErrOptions
			}
			if !errors.Is(err, want) {
				t.Fatalf("web startup error lost:%v", err)
			}
			if prepares, closes, frames := port.snapshot(); prepares != 0 || closes != 1 || len(frames) != 0 {
				t.Fatal("failed web startup touched input or lost ownership")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, args := range [][]string{nil, {"-api", "", "-web", ":0"}, {"-api", ":0", "-web", ":0"}} {
		var stdout, stderr bytes.Buffer
		if code := run(ctx, args, &stdout, &stderr, commandHooks{}); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("pre-canceled production startup performed work: exit%d stdout%q stderr%q", code, stdout.String(), stderr.String())
		}
	}
	cfg := runtimeConfig()
	cfg.API = ""
	if err := serveWith(t.Context(), cfg, io.Discard, runtimeFactories{}); !errors.Is(err, config.ErrInvalid) || !strings.Contains(err.Error(), "listener") {
		t.Fatalf("no-listener runtime did not reject before I/O:%v", err)
	}
}

func TestRuntimeShutdownSharesBrowserDisconnectCleanupBudget(t *testing.T) {
	port := &runtimeTransport{}
	pressed, cleaning := make(chan struct{}), make(chan struct{})
	var pressOnce, cleanOnce sync.Once
	var deadlines []time.Time
	port.write = func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
			pressOnce.Do(func() { close(pressed) })
		} else if bytes.Equal(frame, ch9329.Keyboard(0)) {
			deadline, _ := ctx.Deadline()
			deadlines = append(deadlines, deadline)
			cleanOnce.Do(func() { close(cleaning) })
			<-ctx.Done()
		} else if bytes.Equal(frame, ch9329.MouseRelative(0, 0, 0, 0)) {
			deadline, _ := ctx.Deadline()
			deadlines = append(deadlines, deadline)
		}
		return ctx.Err()
	}
	cfg := runtimeConfig()
	cfg.API, cfg.Web, cfg.CaptureDevice = "", "127.0.0.1:0", "/dev/fake"
	run := startViewerRuntime(t, cfg, port, newRuntimeVideoSource(), nil)
	conn := dialRuntimeViewer(t, run.web)
	runtimeViewerInput(t, conn, `{"type":"keydown","code":"KeyA"}`)
	receiveRuntime(t, pressed)
	_ = conn.CloseNow()
	receiveRuntime(t, cleaning)
	if err := stopViewerRuntime(t, run); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("disconnect cleanup failure lost: %v", err)
	}
	if len(deadlines) != 2 || deadlines[0].IsZero() || deadlines[0] != deadlines[1] {
		t.Fatalf("shutdown added another disconnect cleanup budget: %v", deadlines)
	}
	if _, closes, frames := port.snapshot(); closes != 1 || len(frames) != 3 {
		t.Fatalf("disconnect cleanup retried or lost ownership: closes%d frames%x", closes, frames)
	}
}

func TestRuntimeWebShutdownStartsBothOwnersBeforeWaitingForEither(t *testing.T) {
	finishSerial, finishVideo := make(chan struct{}), make(chan struct{})
	releaseSerial := sync.OnceFunc(func() { close(finishSerial) })
	releaseVideo := sync.OnceFunc(func() { close(finishVideo) })
	defer releaseSerial()
	defer releaseVideo()
	serialStarted, pressed := make(chan struct{}), make(chan struct{})
	var serialOnce, pressedOnce sync.Once
	port := &runtimeTransport{}
	port.write = func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
			pressedOnce.Do(func() { close(pressed) })
		} else if bytes.Equal(frame, ch9329.Keyboard(0)) {
			serialOnce.Do(func() { close(serialStarted) })
			<-finishSerial // Retained serial cleanup ownership.
		}
		return ctx.Err()
	}
	source := newRuntimeVideoSource()
	source.closeHook = func() { <-finishVideo } // Retained video close ownership.
	cfg := runtimeConfig()
	cfg.Web, cfg.CaptureDevice = "127.0.0.1:0", "/dev/fake"
	run := startViewerRuntime(t, cfg, port, source, nil)
	conn := dialRuntimeViewer(t, run.web)
	runtimeViewerInput(t, conn, `{"type":"keydown","code":"KeyA"}`)
	receiveRuntime(t, pressed)
	idle, err := net.Dial("tcp", run.api.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	if _, err := idle.Write([]byte(`{"id":`)); err != nil {
		t.Fatal(err)
	}
	run.cancel()
	receiveRuntime(t, serialStarted)
	receiveRuntime(t, source.closeStarted)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retained owner prevented hijacked socket closure: %v", err)
	}
	if err := idle.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := idle.Read(make([]byte, 1)); err == nil {
		t.Fatal("retained owner left API initial reader open")
	} else if networkErr, ok := err.(net.Error); ok && networkErr.Timeout() {
		t.Fatal("retained owner left API connection waiting for its deadline")
	}
	select {
	case <-run.done:
		t.Fatal("runtime abandoned retained hardware ownership")
	default:
	}
	releaseVideo()
	receiveRuntime(t, source.closed)
	select {
	case <-run.done:
		t.Fatal("runtime returned before serial cleanup completed")
	default:
	}
	releaseSerial()
	if err := stopViewerRuntime(t, run); err != nil {
		t.Fatal(err)
	}
	if _, closes, _ := port.snapshot(); closes != 1 || source.closes.Load() != 1 {
		t.Fatal("runtime did not join both owners")
	}
}
