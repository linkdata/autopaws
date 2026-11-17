// Package config parses and validates the KVM server's portable configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/linkdata/autopaws/hid"
)

var (
	// ErrUsage indicates malformed command-line flags or positional arguments.
	ErrUsage = errors.New("invalid command line")
	// ErrInvalid indicates an invalid configuration file or resolved setting.
	ErrInvalid = errors.New("invalid configuration")
)

// Config contains the resolved serve settings. Empty listener addresses disable
// their listeners; empty device paths select automatic discovery.
type Config struct {
	API             string
	Web             string
	TokenFile       string
	SerialDevice    string
	SerialBaud      int
	ScreenWidth     int
	ScreenHeight    int
	TargetLayout    string
	LayoutsDir      string
	MouseButtonMode string
	CaptureDevice   string
	CaptureWidth    int
	CaptureHeight   int
	Autocrop        bool
	WebFPS          int
	AudioDevice     string
	LogLevel        string
	DebugKeys       bool
}

// Defaults returns the built-in settings without enabling a listener.
func Defaults() Config {
	return Config{
		SerialBaud: 9600, ScreenWidth: 1920, ScreenHeight: 1080,
		TargetLayout: "us104", MouseButtonMode: "absolute",
		CaptureWidth: 1920, CaptureHeight: 1080, Autocrop: true,
		WebFPS: 20, LogLevel: "info",
	}
}

// Validate reports all independent semantic errors in stable setting order.
// It checks a selected layout file, but never opens devices or network sockets.
func (cfg Config) Validate() error {
	var problems []error
	if cfg.API == "" && cfg.Web == "" {
		problems = append(problems, errors.New("at least one of api or web must be set"))
	}
	for _, listener := range []struct{ name, address string }{{"api", cfg.API}, {"web", cfg.Web}} {
		if listener.address != "" {
			if err := validateAddress(listener.address); err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", listener.name, err))
			}
		}
	}
	switch cfg.SerialBaud {
	case 1200, 1800, 2400, 4800, 9600, 19200, 38400, 57600, 115200:
	default:
		problems = append(problems, fmt.Errorf("serial-baud: unsupported rate %d", cfg.SerialBaud))
	}
	for _, dimension := range []struct {
		name  string
		value int
	}{{"screen-width", cfg.ScreenWidth}, {"screen-height", cfg.ScreenHeight}} {
		if dimension.value <= 0 {
			problems = append(problems, fmt.Errorf("%s: must be positive", dimension.name))
		}
	}
	if _, err := hid.LoadLayout(cfg.TargetLayout, cfg.LayoutsDir); err != nil {
		problems = append(problems, fmt.Errorf("target-layout: %w", err))
	}
	if cfg.MouseButtonMode != "absolute" && cfg.MouseButtonMode != "relative" {
		problems = append(problems, fmt.Errorf("mouse-button-mode: expected absolute or relative, got %q", cfg.MouseButtonMode))
	}
	if numericDevice(cfg.CaptureDevice) {
		problems = append(problems, errors.New("capture-device: expected a device path, not a numeric index"))
	}
	for _, dimension := range []struct {
		name  string
		value int
	}{{"capture-width", cfg.CaptureWidth}, {"capture-height", cfg.CaptureHeight}} {
		if dimension.value <= 0 || uint64(dimension.value) > math.MaxUint32 {
			problems = append(problems, fmt.Errorf("%s: must be in 1..%d", dimension.name, uint64(math.MaxUint32)))
		}
	}
	if cfg.WebFPS < 1 || cfg.WebFPS > 120 {
		problems = append(problems, errors.New("web-fps: must be in 1..120"))
	}
	if numericDevice(cfg.AudioDevice) {
		problems = append(problems, errors.New("audio-device: expected a PCM device path, off, or an empty value"))
	}
	if _, ok := logLevel(cfg.LogLevel); !ok {
		problems = append(problems, errors.New("log-level: expected debug, info, warn, or error"))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrInvalid, errors.Join(problems...))
}

func validateAddress(address string) error {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if port == "" || strings.Trim(port, "0123456789") != "" {
		return errors.New("expected a numeric port in 0..65535")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return errors.New("expected a numeric port in 0..65535")
	}
	return nil
}

func numericDevice(value string) bool {
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		value = value[1:]
	}
	return value != "" && strings.Trim(value, "0123456789") == ""
}

func logLevel(value string) (slog.Level, bool) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}

// Loggers returns the ordinary logger and an independent debug-key logger sharing
// output. The key logger is nil when disabled. Call after successful validation.
func (cfg Config) Loggers(output io.Writer) (ordinary, keys *slog.Logger) {
	level, _ := logLevel(cfg.LogLevel)
	shared := &logWriter{output: output}
	ordinary = slog.New(slog.NewTextHandler(shared, &slog.HandlerOptions{Level: level}))
	if cfg.DebugKeys {
		keys = slog.New(slog.NewTextHandler(shared, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return ordinary, keys
}

type logWriter struct {
	mu     sync.Mutex
	output io.Writer
}

func (output *logWriter) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.output.Write(data)
}
