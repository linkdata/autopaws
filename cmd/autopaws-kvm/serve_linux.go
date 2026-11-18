//go:build linux && (amd64 || arm64)

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"

	"github.com/linkdata/autopaws/api"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/devices"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/internal/config"
	"github.com/linkdata/autopaws/internal/token"
	"github.com/linkdata/autopaws/serial"
	"github.com/linkdata/autopaws/web"
)

type runtimeFactories struct {
	newTransport    func(serial.Options) (hid.Transport, error)
	listen          func(context.Context, string, string) (net.Listener, error)
	newCapture      func(capture.Options) (*capture.Manager, error)
	discoverCapture func(context.Context) ([]devices.VideoDevice, error)
	audioOptions    func(string) *web.AudioOptions
}

func serveRuntime(ctx context.Context, cfg config.Config, output io.Writer) error {
	return serveWith(ctx, cfg, output, runtimeFactories{
		newTransport: func(options serial.Options) (hid.Transport, error) { return serial.New(options) },
		listen:       (&net.ListenConfig{}).Listen,
		newCapture:   capture.New,
		audioOptions: func(device string) *web.AudioOptions { return &web.AudioOptions{Device: device} },
		discoverCapture: func(ctx context.Context) ([]devices.VideoDevice, error) {
			return devices.DiscoverVideo(ctx, devices.Options{})
		},
	})
}

// serveWith owns the shared input service. Listener shutdown must not wait for
// handlers before Close begins: active input cleanup shares its shutdown budget.
func serveWith(ctx context.Context, cfg config.Config, output io.Writer, factories runtimeFactories) error {
	if ctx.Err() != nil {
		return nil
	}
	if cfg.API == "" && cfg.Web == "" {
		return fmt.Errorf("%w: at least one listener must be enabled", config.ErrInvalid)
	}
	secret, err := token.ReadFile(cfg.TokenFile)
	if err != nil {
		return err
	}
	layout, err := hid.LoadLayout(cfg.TargetLayout, cfg.LayoutsDir)
	if err != nil {
		return fmt.Errorf("load target-layout: %w", err)
	}
	logger, keys := cfg.Loggers(output)
	transport, err := factories.newTransport(serial.Options{Path: cfg.SerialDevice, Baud: cfg.SerialBaud, Logger: keys})
	if err != nil {
		return fmt.Errorf("create serial transport: %w", err)
	}
	controller := hid.NewController(transport)
	keyboard := hid.NewKeyboard(controller, layout)
	mouse, err := hid.NewMouse(controller, hid.MouseOptions{
		Width: cfg.ScreenWidth, Height: cfg.ScreenHeight, ButtonMode: cfg.MouseButtonMode,
	})
	if err != nil {
		return errors.Join(err, controller.Close())
	}
	var video *capture.Manager
	closeOwners := func() error {
		// Launch both owners before waiting for either synchronous kernel call.
		videoClosed := make(chan error, 1)
		go func() {
			if video != nil {
				videoClosed <- video.Close()
			} else {
				videoClosed <- nil
			}
		}()
		return errors.Join(controller.Close(), <-videoClosed)
	}
	if factories.newCapture != nil {
		selected := cfg.CaptureDevice
		if selected == "" && factories.discoverCapture != nil {
			found, err := factories.discoverCapture(ctx)
			if err != nil {
				logger.Warn("capture discovery failed", "error", err)
			} else if device, err := devices.SelectVideo(found); err == nil {
				selected = device.Path
			}
		}
		video, err = factories.newCapture(capture.Options{
			Device: selected, Width: uint32(cfg.CaptureWidth), Height: uint32(cfg.CaptureHeight), Autocrop: cfg.Autocrop,
		})
		if err != nil {
			return errors.Join(fmt.Errorf("create capture manager: %w", err), closeOwners())
		}
	}
	runtimeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var apiServer *api.Server
	if cfg.API != "" {
		apiServer = api.New(keyboard, mouse, api.Options{
			Token:        secret,
			SerialDevice: cfg.SerialDevice, SerialBaud: cfg.SerialBaud,
			ScreenWidth: cfg.ScreenWidth, ScreenHeight: cfg.ScreenHeight,
			KeyboardLayout: cfg.TargetLayout, LayoutsDir: cfg.LayoutsDir, ButtonMode: cfg.MouseButtonMode,
			Logger: logger, KeyLogger: keys,
			Capture: video, DiscoverCapture: factories.discoverCapture,
		})
	}
	var viewer *web.Server
	var httpServer *http.Server
	if cfg.Web != "" {
		var audio *web.AudioOptions
		if factories.audioOptions != nil {
			audio = factories.audioOptions(cfg.AudioDevice)
		}
		viewer, err = web.New(controller, mouse, video, web.Options{Token: secret, FPS: cfg.WebFPS, Logger: logger, KeyLogger: keys, Audio: audio})
		if err != nil {
			return errors.Join(fmt.Errorf("create web viewer: %w", err), closeOwners())
		}
		httpServer = &http.Server{Handler: viewer}
	}
	var bound []net.Listener
	shutdown := func() error {
		cancel()
		if viewer != nil {
			viewer.Close() // net/http.Close does not close hijacked WebSockets.
		}
		// Network close can retain ownership too. It must not delay either
		// hardware owner or let an existing input cleanup budget expire first.
		networkClosed := make(chan error, 1)
		go func() {
			var result error
			if httpServer != nil {
				result = httpServer.Close()
			}
			for _, listener := range bound {
				if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
					result = errors.Join(result, err)
				}
			}
			networkClosed <- result
		}()
		result := errors.Join(closeOwners(), <-networkClosed)
		if viewer != nil {
			viewer.Wait() // Also join pre-bind audio control/owned close on startup errors.
		}
		return result
	}
	var apiListener, webListener net.Listener
	if cfg.API != "" {
		apiListener, err = factories.listen(runtimeCtx, "tcp", cfg.API)
		if err != nil {
			return errors.Join(fmt.Errorf("listen API: %w", err), shutdown())
		}
		bound = append(bound, apiListener)
	}
	if cfg.Web != "" {
		webListener, err = factories.listen(runtimeCtx, "tcp", cfg.Web)
		if err != nil {
			return errors.Join(fmt.Errorf("listen web: %w", err), shutdown())
		}
		bound = append(bound, webListener)
	}
	// All requested addresses are bound before either service begins accepting.
	served := make(chan error, len(bound))
	if apiServer != nil {
		warnExposed(logger, "API", apiListener.Addr(), secret)
		logger.Info("API listening", "address", apiListener.Addr().String())
		go func() { served <- apiServer.Serve(runtimeCtx, apiListener) }()
	}
	if httpServer != nil {
		warnExposed(logger, "Web", webListener.Addr(), secret)
		logger.Info("Web listening", "address", webListener.Addr().String())
		go func() {
			err := httpServer.Serve(webListener)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			served <- err
		}()
	}
	remaining := len(bound)
	var serveErr error
	select {
	case serveErr = <-served:
		remaining--
	case <-ctx.Done():
	}
	closeErr := shutdown()
	for range remaining {
		serveErr = errors.Join(serveErr, <-served)
	}
	if apiServer != nil {
		apiServer.Wait()
	}
	return errors.Join(serveErr, closeErr)
}

// Inspect the address actually bound by Listen, without another DNS lookup.
func warnExposed(logger *slog.Logger, service string, address net.Addr, secret string) {
	if secret != "" {
		return
	}
	if tcp, ok := address.(*net.TCPAddr); ok && tcp.IP.IsLoopback() {
		return
	}
	logger.Warn("listener exposed without authentication", "service", service, "address", address.String())
}
