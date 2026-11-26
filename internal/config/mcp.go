package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// MCP contains the MCP settings from the shared YAML file and command line.
type MCP struct {
	API               string
	Token             string
	TokenFile         string
	CaptureLog        string
	Tesseract         string
	VisionAPI         string
	VisionModel       string
	VisionTokenFile   string
	VisionMaxWidth    int
	VisionRequestJSON string
}

// MCPDefaults returns the MCP settings with vision and capture logging disabled.
func MCPDefaults() MCP {
	return MCP{API: Defaults().API, VisionMaxWidth: 960}
}

// ParseMCP resolves explicit MCP flags over the shared YAML file over defaults.
//
// Inline tokens are YAML-only.
// KVM-only settings are type-checked but do not configure or validate hardware.
func ParseMCP(args []string) (cfg MCP, err error) {
	flags := MCPDefaults()
	var path string
	parsed := mcpFlags(&flags, &path, io.Discard)
	if err = parsed.Parse(args); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			err = errors.Join(ErrUsage, err)
		}
	} else if parsed.NArg() != 0 {
		err = &argumentsError{args: parsed.Args()}
	} else {
		cfg = MCPDefaults()
		var unusedPath string
		settings := mcpFlags(&cfg, &unusedPath, io.Discard)
		// Inline tokens are accepted only from YAML, never from process arguments.
		settings.StringVar(&cfg.Token, "token", cfg.Token, "")
		ignored := Defaults()
		if err = resolveSettings(parsed, path, settings, daemonFlags(&ignored, &unusedPath, io.Discard)); err != nil {
			cfg = MCP{}
		}
	}
	return
}

// WriteMCPUsage writes the MCP flags with their built-in defaults.
func WriteMCPUsage(output io.Writer) {
	cfg := MCPDefaults()
	var path string
	settings := mcpFlags(&cfg, &path, output)
	fmt.Fprintln(output, "Usage: autopaws-mcp [flags]")
	settings.PrintDefaults()
}

func mcpFlags(cfg *MCP, path *string, output io.Writer) (settings *flag.FlagSet) {
	settings = flag.NewFlagSet("autopaws-mcp", flag.ContinueOnError)
	settings.SetOutput(output)
	settings.StringVar(path, "config", "", "Read only this YAML configuration file (shared with autopaws-kvm)")
	settings.StringVar(&cfg.API, "api", cfg.API, "KVM API host:port (numeric port 1..65535)")
	settings.StringVar(&cfg.CaptureLog, "capture-log", cfg.CaptureLog, "Save unchanged screenshot JPEGs in DIR; empty disables logging")
	settings.StringVar(&cfg.TokenFile, "token-file", cfg.TokenFile, "Shared-secret UTF-8 file for the KVM API; alternative to YAML token")
	settings.StringVar(&cfg.Tesseract, "tesseract", cfg.Tesseract, "Tesseract executable; empty searches PATH and Windows install directories")
	settings.StringVar(&cfg.VisionAPI, "vision-api", cfg.VisionAPI, "OpenAI-compatible base URL or chat/completions endpoint for wait_for; empty disables vision")
	settings.StringVar(&cfg.VisionModel, "vision-model", cfg.VisionModel, "Vision model name for wait_for")
	settings.StringVar(&cfg.VisionTokenFile, "vision-token-file", cfg.VisionTokenFile, "Bearer-token UTF-8 file for the vision endpoint; empty sends no Authorization header")
	settings.IntVar(&cfg.VisionMaxWidth, "vision-max-width", cfg.VisionMaxWidth, "Maximum screenshot width sent to the vision model, 64..4096; returned screenshots keep their original size")
	settings.StringVar(&cfg.VisionRequestJSON, "vision-request-json", cfg.VisionRequestJSON, "OpenAI request JSON options (default reasoning_effort=none); an explicit object replaces defaults, {} omits reasoning control; model, messages and stream cannot be overridden")
	return
}
