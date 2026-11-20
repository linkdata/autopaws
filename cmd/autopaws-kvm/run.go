//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/linkdata/autopaws/internal/config"
)

// Release builds can set this with -ldflags=-X=main.version=VERSION.
var version = "devel"

type commandHooks struct {
	serve       func(context.Context, config.Config) error
	listDevices func(io.Writer) error
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, hooks commandHooks) int {
	cfg, action, err := config.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		config.WriteUsage(stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		if errors.Is(err, config.ErrUsage) {
			config.WriteUsage(stderr)
		}
		return 2
	}
	switch action {
	case config.Version:
		fmt.Fprintln(stdout, "autopaws-kvm", version)
		return 0
	case config.ListDevices:
		if hooks.listDevices == nil {
			hooks.listDevices = func(output io.Writer) error {
				return listDevicesRuntime(ctx, output)
			}
		}
		err = hooks.listDevices(stdout)
	default:
		if hooks.serve == nil {
			hooks.serve = func(ctx context.Context, cfg config.Config) error {
				return serveRuntime(ctx, cfg, stderr)
			}
		}
		err = hooks.serve(ctx, cfg)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
