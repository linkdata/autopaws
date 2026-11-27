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

// Action selects the daemon or a standalone informational command.
type Action uint8

const (
	// Daemon starts the KVM service.
	Daemon Action = iota
	// Version prints the build version without loading configuration.
	Version
	// ListDevices lists attached devices without loading configuration.
	ListDevices
)

type argumentsError struct{ args []string }

func (err *argumentsError) Error() string {
	return ErrUsage.Error() + ": unexpected arguments: " + strings.Join(err.args, " ")
}

func (*argumentsError) Unwrap() error { return ErrUsage }

// Parse resolves explicit flags over the selected YAML file over defaults.
//
// Inline tokens are YAML-only.
// Help and standalone informational actions do not load configuration files.
// True informational selectors cannot be combined with other actions or settings.
func Parse(args []string) (Config, Action, error) {
	flags := Defaults()
	var path string
	parsed := daemonFlags(&flags, &path, io.Discard)
	var version, listDevices bool
	actionFlags(parsed, &version, &listDevices)
	if err := parsed.Parse(webArguments(args, parsed)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, Daemon, flag.ErrHelp
		}
		return Config{}, Daemon, fmt.Errorf("%w: %w", ErrUsage, err)
	}
	if parsed.NArg() != 0 {
		return Config{}, Daemon, &argumentsError{args: parsed.Args()}
	}
	if version || listDevices {
		if parsed.NFlag() != 1 {
			return Config{}, Daemon, fmt.Errorf("%w: --version and --list-devices must be used alone", ErrUsage)
		}
		if version {
			return Config{}, Version, nil
		}
		return Config{}, ListDevices, nil
	}

	resolved := Defaults()
	var unusedPath string
	settings := daemonFlags(&resolved, &unusedPath, io.Discard)
	// Inline tokens are accepted only from YAML, never from process arguments.
	settings.StringVar(&resolved.Token, "token", resolved.Token, "")
	ignored := MCPDefaults()
	if err := resolveSettings(parsed, path, settings, mcpFlags(&ignored, &unusedPath, io.Discard)); err != nil {
		return Config{}, Daemon, err
	}
	if err := resolved.Validate(); err != nil {
		return Config{}, Daemon, err
	}
	return resolved, Daemon, nil
}

// Expand optional web addresses without changing flag values or positional arguments.
func webArguments(args []string, settings *flag.FlagSet) []string {
	args = append([]string(nil), args...)
	for i := 0; i < len(args); i++ {
		argument := args[i]
		if argument == "--" || argument == "-" || !strings.HasPrefix(argument, "-") {
			break
		}
		name, _, assigned := strings.Cut(strings.TrimPrefix(argument[1:], "-"), "=")
		setting := settings.Lookup(name)
		if setting == nil {
			break
		}
		if name == "web" && !assigned && (i+1 == len(args) || strings.HasPrefix(args[i+1], "-")) {
			args[i] = argument + "=127.0.0.1:9330"
		} else if !assigned {
			boolean, ok := setting.Value.(interface{ IsBoolFlag() bool })
			if !ok || !boolean.IsBoolFlag() {
				i++
			}
		}
	}
	return args
}

func resolveSettings(parsed *flag.FlagSet, path string, settings, other *flag.FlagSet) (err error) {
	var selectedFile bool
	parsed.Visit(func(value *flag.Flag) {
		if value.Name == "config" {
			selectedFile = true
		}
	})
	if selectedFile {
		err = loadFile(path, settings, other)
	}
	if err == nil {
		parsed.Visit(func(value *flag.Flag) {
			if value.Name != "config" && settings.Lookup(value.Name) != nil {
				// The first parse already validated each explicit flag's syntax.
				_ = settings.Set(value.Name, value.Value.String())
			}
		})
	}
	return
}

// WriteUsage writes the command flags with their built-in defaults.
func WriteUsage(output io.Writer) {
	cfg := Defaults()
	var path string
	settings := daemonFlags(&cfg, &path, output)
	var version, listDevices bool
	actionFlags(settings, &version, &listDevices)
	fmt.Fprintln(output, "Usage: autopaws-kvm [flags]")
	fmt.Fprintln(output, "       autopaws-kvm --version")
	fmt.Fprintln(output, "       autopaws-kvm --list-devices")
	settings.PrintDefaults()
}

func daemonFlags(cfg *Config, path *string, output io.Writer) *flag.FlagSet {
	settings := flag.NewFlagSet("autopaws-kvm", flag.ContinueOnError)
	settings.SetOutput(output)
	settings.StringVar(path, "config", "", "Read only this YAML configuration file")
	settings.StringVar(&cfg.API, "api", cfg.API, "API listen host:port; empty disables it (example 127.0.0.1:9329)")
	settings.StringVar(&cfg.Web, "web", cfg.Web, "Web listen `[host:port]`; no address uses 127.0.0.1:9330; empty disables it")
	settings.StringVar(&cfg.TokenFile, "token-file", cfg.TokenFile, "Shared-secret UTF-8 file; alternative to YAML token")
	settings.StringVar(&cfg.SerialDevice, "serial-device", cfg.SerialDevice, "Serial path or Windows COMn; empty auto-detects one CH340/CH341")
	settings.IntVar(&cfg.SerialBaud, "serial-baud", cfg.SerialBaud, "Serial baud rate")
	settings.IntVar(&cfg.ScreenWidth, "screen-width", cfg.ScreenWidth, "Target screen width in pixels")
	settings.IntVar(&cfg.ScreenHeight, "screen-height", cfg.ScreenHeight, "Target screen height in pixels")
	settings.StringVar(&cfg.TargetLayout, "target-layout", cfg.TargetLayout, "Target keyboard layout name")
	settings.StringVar(&cfg.LayoutsDir, "layouts-dir", cfg.LayoutsDir, "Directory for custom keyboard layouts")
	settings.StringVar(&cfg.MouseButtonMode, "mouse-button-mode", cfg.MouseButtonMode, "Default mouse button mode: relative or absolute")
	settings.StringVar(&cfg.MouseMode, "mouse-mode", cfg.MouseMode, "Mouse mapping startup mode: legacy (default) or extended (requires display geometry)")
	settings.StringVar(&cfg.CaptureDevice, "capture-device", cfg.CaptureDevice, "Capture path, macOS avfoundation:NAME or Windows dshow:NAME; empty auto-detects (no numeric indexes)")
	settings.IntVar(&cfg.CaptureWidth, "capture-width", cfg.CaptureWidth, "Requested capture width in pixels")
	settings.IntVar(&cfg.CaptureHeight, "capture-height", cfg.CaptureHeight, "Requested capture height in pixels")
	settings.BoolVar(&cfg.Autocrop, "autocrop", cfg.Autocrop, "Crop black bars matching the target output aspect")
	settings.IntVar(&cfg.WebFPS, "web-fps", cfg.WebFPS, "Web viewer frame rate in 1..120")
	settings.StringVar(&cfg.AudioDevice, "audio-device", cfg.AudioDevice, "Audio PCM path, macOS avfoundation:NAME or Windows dshow:NAME; empty auto-detects on Linux, off disables audio")
	settings.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Ordinary log level: debug, info, warn, or error")
	settings.BoolVar(&cfg.DebugKeys, "debug-keys", cfg.DebugKeys, "Log typed text and HID input at debug level regardless of log-level")
	return settings
}

func actionFlags(settings *flag.FlagSet, version, listDevices *bool) {
	settings.BoolVar(version, "version", false, "Print the version and exit; use alone")
	settings.BoolVar(listDevices, "list-devices", false, "List devices and exit; use alone")
}

func loadFile(path string, settings ...*flag.FlagSet) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: read %q: %w", ErrInvalid, path, err)
	}
	if err := applyYAML(data, settings...); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	return nil
}

func applyYAML(data []byte, settings ...*flag.FlagSet) error {
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
		var setting *flag.Flag
		for _, group := range settings {
			if setting == nil {
				setting = group.Lookup(key.Value)
			}
		}
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
