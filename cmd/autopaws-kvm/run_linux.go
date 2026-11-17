//go:build linux && (amd64 || arm64)

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/linkdata/autopaws/internal/config"
)

// Release builds can set this with -ldflags=-X=main.version=VERSION.
var version = "devel"

type commandHooks struct {
	serve       func(context.Context, config.Config) error
	listDevices func(io.Writer) error
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, hooks commandHooks) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return 2
	}
	switch args[0] {
	case "-h", "-help", "--help":
		writeUsage(stdout)
		return 0
	case "serve":
		cfg, err := config.ParseServe(args[1:])
		if errors.Is(err, flag.ErrHelp) {
			config.WriteServeUsage(stdout)
			return 0
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			if errors.Is(err, config.ErrUsage) {
				config.WriteServeUsage(stderr)
			}
			return 2
		}
		if hooks.serve == nil {
			hooks.serve = func(ctx context.Context, cfg config.Config) error {
				return serveRuntime(ctx, cfg, stderr)
			}
		}
		if err := hooks.serve(ctx, cfg); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "list-devices", "version":
		if err := parseSimple(args[0], args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				writeSimpleUsage(stdout, args[0])
				return 0
			}
			fmt.Fprintln(stderr, err)
			writeSimpleUsage(stderr, args[0])
			return 2
		}
		if args[0] == "version" {
			fmt.Fprintln(stdout, "autopaws-kvm", version)
			return 0
		}
		if hooks.listDevices == nil {
			hooks.listDevices = func(output io.Writer) error {
				return listDevicesRuntime(ctx, output)
			}
		}
		if err := hooks.listDevices(stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		writeUsage(stderr)
		return 2
	}
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: autopaws-kvm <serve|list-devices|version> [flags]")
	fmt.Fprintln(output, "Run autopaws-kvm COMMAND -help for command usage.")
}

func writeSimpleUsage(output io.Writer, command string) {
	fmt.Fprintf(output, "Usage: autopaws-kvm %s\n", command)
}

func parseSimple(command string, args []string) error {
	settings := flag.NewFlagSet(command, flag.ContinueOnError)
	settings.SetOutput(io.Discard)
	if err := settings.Parse(args); err != nil {
		return err
	}
	if settings.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(settings.Args(), " "))
	}
	return nil
}
