package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ParseServe resolves explicit flags over the selected YAML file over defaults.
// It does not search for files or read environment variables. Help returns
// flag.ErrHelp without reading a configuration file or validating settings.
func ParseServe(args []string) (Config, error) {
	flags := Defaults()
	var path string
	parsed := serveFlags(&flags, &path, io.Discard)
	if err := parsed.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, flag.ErrHelp
		}
		return Config{}, fmt.Errorf("%w: %w", ErrUsage, err)
	}
	if parsed.NArg() != 0 {
		return Config{}, fmt.Errorf("%w: unexpected arguments: %s", ErrUsage, strings.Join(parsed.Args(), " "))
	}

	resolved := Defaults()
	var unusedPath string
	settings := serveFlags(&resolved, &unusedPath, io.Discard)
	selectedFile := false
	parsed.Visit(func(value *flag.Flag) {
		if value.Name == "config" {
			selectedFile = true
		}
	})
	if selectedFile {
		if err := loadFile(path, settings); err != nil {
			return Config{}, err
		}
	}
	parsed.Visit(func(value *flag.Flag) {
		if value.Name != "config" {
			// Both flag sets have identical registrations, and the first parse
			// already validated each value's flag syntax.
			_ = settings.Set(value.Name, value.Value.String())
		}
	})
	if err := resolved.Validate(); err != nil {
		return Config{}, err
	}
	return resolved, nil
}

// WriteServeUsage writes the serve flags with their built-in defaults.
func WriteServeUsage(output io.Writer) {
	cfg := Defaults()
	var path string
	settings := serveFlags(&cfg, &path, output)
	fmt.Fprintln(output, "Usage: autopaws-kvm serve [flags]")
	settings.PrintDefaults()
}

func serveFlags(cfg *Config, path *string, output io.Writer) *flag.FlagSet {
	settings := flag.NewFlagSet("autopaws-kvm serve", flag.ContinueOnError)
	settings.SetOutput(output)
	settings.StringVar(path, "config", "", "Read only this YAML configuration file")
	settings.StringVar(&cfg.API, "api", cfg.API, "API listen host:port; empty disables it (example 127.0.0.1:9329)")
	settings.StringVar(&cfg.Web, "web", cfg.Web, "Web listen host:port; empty disables it (example 127.0.0.1:9330)")
	settings.StringVar(&cfg.SerialDevice, "serial-device", cfg.SerialDevice, "Serial device path; empty auto-detects one CH340/CH341")
	settings.IntVar(&cfg.SerialBaud, "serial-baud", cfg.SerialBaud, "Serial baud rate")
	settings.IntVar(&cfg.ScreenWidth, "screen-width", cfg.ScreenWidth, "Target screen width in pixels")
	settings.IntVar(&cfg.ScreenHeight, "screen-height", cfg.ScreenHeight, "Target screen height in pixels")
	settings.StringVar(&cfg.TargetLayout, "target-layout", cfg.TargetLayout, "Target keyboard layout name")
	settings.StringVar(&cfg.LayoutsDir, "layouts-dir", cfg.LayoutsDir, "Directory for custom keyboard layouts")
	settings.StringVar(&cfg.MouseButtonMode, "mouse-button-mode", cfg.MouseButtonMode, "Default mouse button mode: absolute or relative")
	settings.StringVar(&cfg.CaptureDevice, "capture-device", cfg.CaptureDevice, "Capture device path; empty auto-detects (numeric indexes are not accepted)")
	settings.IntVar(&cfg.CaptureWidth, "capture-width", cfg.CaptureWidth, "Requested capture width in pixels")
	settings.IntVar(&cfg.CaptureHeight, "capture-height", cfg.CaptureHeight, "Requested capture height in pixels")
	settings.BoolVar(&cfg.Autocrop, "autocrop", cfg.Autocrop, "Detect and crop capture black borders")
	settings.IntVar(&cfg.WebFPS, "web-fps", cfg.WebFPS, "Web viewer frame rate in 1..120")
	settings.StringVar(&cfg.AudioDevice, "audio-device", cfg.AudioDevice, "Audio PCM path; empty auto-detects, off disables audio")
	settings.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Ordinary log level: debug, info, warn, or error")
	settings.BoolVar(&cfg.DebugKeys, "debug-keys", cfg.DebugKeys, "Log typed text and HID input at debug level regardless of log-level")
	return settings
}

func loadFile(path string, settings *flag.FlagSet) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: read %q: %w", ErrInvalid, path, err)
	}
	if err := applyYAML(data, settings); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	return nil
}

func applyYAML(data []byte, settings *flag.FlagSet) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return errors.New("expected a configuration mapping")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("expected one YAML document")
	}
	seen := make(map[string]bool)
	var problems []error
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			problems = append(problems, fmt.Errorf("line %d: configuration keys must be strings", key.Line))
			continue
		}
		if seen[key.Value] {
			problems = append(problems, fmt.Errorf("line %d: duplicate setting %q", key.Line, key.Value))
			continue
		}
		seen[key.Value] = true
		setting := settings.Lookup(key.Value)
		if setting == nil || key.Value == "config" {
			problems = append(problems, fmt.Errorf("line %d: unknown setting %q", key.Line, key.Value))
			continue
		}
		if err := applyValue(value, setting); err != nil {
			problems = append(problems, fmt.Errorf("line %d: %s: %w", value.Line, key.Value, err))
		}
	}
	return errors.Join(problems...)
}

func applyValue(node *yaml.Node, setting *flag.Flag) error {
	if node.Kind != yaml.ScalarNode {
		return errors.New("expected a scalar setting value")
	}
	var value string
	switch setting.Value.(flag.Getter).Get().(type) {
	case string:
		if node.Tag != "!!str" {
			return errors.New("expected a string")
		}
		value = node.Value
	case int:
		var number int
		if node.Tag != "!!int" {
			return errors.New("expected an integer")
		}
		if err := node.Decode(&number); err != nil {
			return err
		}
		value = strconv.Itoa(number)
	case bool:
		if node.Tag != "!!bool" {
			return errors.New("expected true or false")
		}
		var boolean bool
		if err := node.Decode(&boolean); err != nil {
			return err
		}
		value = strconv.FormatBool(boolean)
	}
	return setting.Value.Set(value)
}
