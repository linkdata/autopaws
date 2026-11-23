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
	"github.com/linkdata/autopaws/internal/token"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Release builds set this through -ldflags=-X=main.version=VERSION.
var version = "devel"

type mcpOptions struct {
	APIAddress    string
	CaptureLogDir string
	Version       string
	Logger        *slog.Logger
	TesseractPath string
	Vision        *visionClient
}

func run(ctx context.Context, args []string, transport mcp.Transport, stdout, stderr io.Writer) int {
	options := mcpOptions{Version: version}
	flags := flag.NewFlagSet("autopaws-mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.APIAddress, "api", "127.0.0.1:9329", "KVM API host:port (numeric port 1..65535)")
	flags.StringVar(&options.CaptureLogDir, "capture-log", "", "Save unchanged screenshot JPEGs in DIR; empty disables logging")
	var tokenFile string
	flags.StringVar(&tokenFile, "token-file", "", "Shared-secret UTF-8 file for the KVM API; empty disables authentication")
	var requestedTesseract string
	flags.StringVar(&requestedTesseract, "tesseract", "", "Tesseract executable; empty searches PATH and Windows install directories")
	var vision visionConfig
	var visionTokenFile string
	flags.StringVar(&vision.API, "vision-api", "", "OpenAI-compatible base URL or chat/completions endpoint for wait_for; empty disables vision")
	flags.StringVar(&vision.Model, "vision-model", "", "Vision model name for wait_for")
	flags.StringVar(&visionTokenFile, "vision-token-file", "", "Bearer-token UTF-8 file for the vision endpoint; empty sends no Authorization header")
	flags.IntVar(&vision.MaxWidth, "vision-max-width", 960, "Maximum screenshot width sent to the vision model, 64..4096; returned screenshots keep their original size")
	flags.StringVar(&vision.RequestJSON, "vision-request-json", "", "Extra OpenAI request JSON options, e.g. {\"reasoning_effort\":\"none\"}; model, messages and stream cannot be overridden")
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
	secret, err := token.ReadFile(tokenFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	options.Logger = slog.New(slog.NewTextHandler(stderr, nil))
	options.TesseractPath, err = resolveTesseract(requestedTesseract)
	if err != nil {
		options.Logger.Warn("OCR tools disabled: Tesseract unavailable", "error", err)
	}
	vision.Token, err = token.ReadFile(visionTokenFile)
	if err != nil {
		fmt.Fprintln(stderr, "vision token:", err)
		return 1
	}
	options.Vision, err = newVisionClient(vision)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	server := newServer(ctx, options, apiclient.NewWithToken(options.APIAddress, secret))
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
