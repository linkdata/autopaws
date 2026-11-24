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

	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/internal/config"
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
	var cfg config.MCP
	var err error
	if cfg, err = config.ParseMCP(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			config.WriteMCPUsage(stdout)
			return 0
		}
		fmt.Fprintln(stderr, err)
		if errors.Is(err, config.ErrUsage) {
			config.WriteMCPUsage(stderr)
		}
		return 2
	}
	options := mcpOptions{Version: version, APIAddress: cfg.API, CaptureLogDir: cfg.CaptureLog}
	if err := validateAPIAddress(options.APIAddress); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if ctx.Err() != nil {
		return 0
	}
	var secret string
	secret, err = token.Resolve(cfg.Token, cfg.TokenFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	options.Logger = slog.New(slog.NewTextHandler(stderr, nil))
	options.TesseractPath, err = resolveTesseract(cfg.Tesseract)
	if err != nil {
		options.Logger.Warn("OCR tools disabled: Tesseract unavailable", "error", err)
	}
	vision := visionConfig{API: cfg.VisionAPI, Model: cfg.VisionModel, MaxWidth: cfg.VisionMaxWidth, RequestJSON: cfg.VisionRequestJSON}
	vision.Token, err = token.ReadFile(cfg.VisionTokenFile)
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
