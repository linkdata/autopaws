package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"

	"github.com/linkdata/autopaws/apiclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Release builds set this through -ldflags=-X=main.version=VERSION.
var version = "devel"

type mcpOptions struct {
	APIAddress    string
	CaptureLogDir string
	Version       string
	Logger        *slog.Logger
}

func run(ctx context.Context, args []string, transport mcp.Transport, stdout, stderr io.Writer) int {
	options := mcpOptions{Version: version}
	flags := flag.NewFlagSet("autopaws-mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.APIAddress, "api", "127.0.0.1:9329", "KVM API host:port (numeric port 1..65535)")
	flags.StringVar(&options.CaptureLogDir, "capture-log", "", "Save unchanged screenshot JPEGs in DIR; empty disables logging")
	usage := func(output io.Writer) {
		fmt.Fprintln(output, "Usage: autopaws-mcp [flags]")
		flags.SetOutput(output)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return 0
		}
		fmt.Fprintln(stderr, err)
		usage(stderr)
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected arguments:", strings.Join(flags.Args(), " "))
		usage(stderr)
		return 2
	}
	if err := validateAPIAddress(options.APIAddress); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if ctx.Err() != nil {
		return 0
	}
	options.Logger = slog.New(slog.NewTextHandler(stderr, nil))
	server := newServer(ctx, options, apiclient.New(options.APIAddress))
	if err := server.Run(ctx, transport); err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func validateAPIAddress(address string) error {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("-api: %w", err)
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return errors.New("-api: expected a numeric port in 1..65535")
	}
	return nil
}
