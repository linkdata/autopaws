//go:build linux && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/serial"
	"github.com/linkdata/autopaws/v4l2"
)

type retainedRuntimeListener struct {
	net.Listener
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (listener *retainedRuntimeListener) Close() error {
	err := listener.Listener.Close()
	listener.once.Do(func() { close(listener.entered) })
	<-listener.release // Model a close retaining ownership after cancellation.
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func TestRuntimeRetainedListenerCloseDoesNotDelayHardwareShutdown(t *testing.T) {
	for _, retainedKind := range []string{"API", "web"} {
		t.Run(retainedKind, func(t *testing.T) {
			finishNetwork := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(finishNetwork) })
			defer unblock()
			pressed := make(chan struct{})
			var pressedOnce sync.Once
			port := &runtimeClosingTransport{runtimeTransport: &runtimeTransport{}, closed: make(chan struct{})}
			port.write = func(ctx context.Context, frame []byte) error {
				if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
					pressedOnce.Do(func() { close(pressed) })
				}
				return ctx.Err()
			}
			source := newRuntimeVideoSource()
			cfg := runtimeConfig()
			cfg.Web, cfg.CaptureDevice = "127.0.0.1:0", "/dev/fake"
			var retained *retainedRuntimeListener
			run := startViewerRuntime(t, cfg, port, source, func(kind string, listener net.Listener) net.Listener {
				if kind == retainedKind {
					retained = &retainedRuntimeListener{Listener: listener, entered: make(chan struct{}), release: finishNetwork}
					return retained
				}
				return listener
			})
			conn := dialRuntimeViewer(t, run.web)
			runtimeViewerInput(t, conn, `{"type":"keydown","code":"KeyA"}`)
			receiveRuntime(t, pressed)
			run.cancel()
			receiveRuntime(t, retained.entered)
			// Both hardware owners must finish even while a requested listener's
			// close is retained. Sequential network-first shutdown deadlocks here.
			receiveRuntime(t, port.closed)
			receiveRuntime(t, source.closed)
			_, closes, frames := port.snapshot()
			if closes != 1 || len(frames) < 3 || !bytes.Equal(frames[len(frames)-2], ch9329.Keyboard(0)) || !bytes.Equal(frames[len(frames)-1], ch9329.MouseRelative(0, 0, 0, 0)) {
				t.Fatalf("retained listener delayed serial release/close: %x", frames)
			}
			select {
			case <-run.done:
				t.Fatal("runtime abandoned retained network close")
			default:
			}
			unblock()
			if err := stopViewerRuntime(t, run); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimePartialBindFailureClosesOwnersWhileListenerCloseIsRetained(t *testing.T) {
	finishNetwork := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(finishNetwork) })
	defer unblock()
	port := &runtimeClosingTransport{runtimeTransport: &runtimeTransport{}, closed: make(chan struct{})}
	source := newRuntimeVideoSource()
	failure := errors.New("second address unavailable")
	entered := make(chan struct{})
	var first *acceptObservedListener
	done := make(chan error, 1)
	cfg := runtimeConfig()
	cfg.Web = "127.0.0.1:0"
	go func() {
		binds := 0
		done <- serveWith(t.Context(), cfg, io.Discard, runtimeFactories{
			newTransport: func(serial.Options) (hid.Transport, error) { return port, nil },
			newCapture: func(options capture.Options) (*capture.Manager, error) {
				options.Device = "/dev/fake"
				options.OpenSource = func(context.Context, string, v4l2.Options) (capture.Source, error) { return source, nil }
				manager, err := capture.New(options)
				if err != nil {
					return nil, err
				}
				if _, err := manager.Subscribe(); err != nil {
					return manager, err
				}
				<-source.started
				return manager, nil
			},
			listen: func(ctx context.Context, network, address string) (net.Listener, error) {
				binds++
				if binds == 2 {
					return nil, failure
				}
				listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
				if err != nil {
					return nil, err
				}
				first = &acceptObservedListener{Listener: listener}
				return &retainedRuntimeListener{Listener: first, entered: entered, release: finishNetwork}, nil
			},
		})
	}()
	receiveRuntime(t, entered)
	receiveRuntime(t, port.closed)
	receiveRuntime(t, source.closed)
	if first.accepts.Load() != 0 {
		t.Fatal("partial bind failure admitted a service")
	}
	select {
	case err := <-done:
		t.Fatalf("startup failure abandoned retained listener: %v", err)
	default:
	}
	unblock()
	if err := receiveRuntime(t, done); !errors.Is(err, failure) {
		t.Fatalf("partial bind failure lost original error: %v", err)
	}
}
