//go:build linux && (amd64 || arm64)

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/linkdata/autopaws/api"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/internal/config"
	"github.com/linkdata/autopaws/serial"
)

type runtimeFactories struct {
	newTransport func(serial.Options) (hid.Transport, error)
	listen       func(context.Context, string, string) (net.Listener, error)
}

func serveRuntime(ctx context.Context, cfg config.Config, output io.Writer) error {
	return serveWith(ctx, cfg, output, runtimeFactories{
		newTransport: func(options serial.Options) (hid.Transport, error) { return serial.New(options) },
		listen:       (&net.ListenConfig{}).Listen,
	})
}

// serveWith owns the shared input service. Listener shutdown must not wait for
// handlers before Close begins: active input cleanup shares its shutdown budget.
func serveWith(ctx context.Context, cfg config.Config, output io.Writer, factories runtimeFactories) error {
	if ctx.Err() != nil {
		return nil
	}
	if cfg.Web != "" {
		return errors.New("web viewer is not implemented yet (issue #37)")
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
	runtimeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := factories.listen(runtimeCtx, "tcp", cfg.API)
	if err != nil {
		return errors.Join(fmt.Errorf("listen API: %w", err), controller.Close())
	}
	server := api.New(keyboard, mouse, api.Options{
		SerialDevice: cfg.SerialDevice, SerialBaud: cfg.SerialBaud,
		ScreenWidth: cfg.ScreenWidth, ScreenHeight: cfg.ScreenHeight,
		KeyboardLayout: cfg.TargetLayout, ButtonMode: cfg.MouseButtonMode,
		Logger: logger, KeyLogger: keys,
	})
	logger.Info("API listening", "address", listener.Addr().String())
	served := make(chan error, 1)
	go func() { served <- server.Serve(runtimeCtx, listener) }()
	var serveErr error
	finished := false
	select {
	case serveErr = <-served:
		finished = true
	case <-ctx.Done():
	}
	cancel()
	closeErr := controller.Close()
	if !finished {
		serveErr = <-served
	}
	server.Wait()
	return errors.Join(serveErr, closeErr)
}
