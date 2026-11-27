package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "serve.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaults(t *testing.T) {
	want := Config{
		API:        "127.0.0.1:9329",
		SerialBaud: 9600, ScreenWidth: 1920, ScreenHeight: 1080,
		TargetLayout: "us104", MouseButtonMode: "relative",
		CaptureWidth: 1920, CaptureHeight: 1080,
		WebFPS: 20, LogLevel: "info",
	}
	if got := Defaults(); got != want {
		t.Fatalf("Defaults = %+v; want %+v", got, want)
	}
	if err := want.Validate(); err != nil {
		t.Fatalf("default configuration = %v", err)
	}
	got, _, err := Parse(nil)
	if err != nil || got != want {
		t.Fatalf("flag defaults = %+v, %v; want %+v", got, err, want)
	}
}

func TestListenerAndButtonDefaultsCanBeOverridden(t *testing.T) {
	for _, vector := range []struct {
		name, yaml string
		args       []string
		api, web   string
		mode       string
	}{
		{"file", "api: ''\nweb: 127.0.0.1:9330\nmouse-button-mode: absolute\n", nil, "", "127.0.0.1:9330", "absolute"},
		{"flags", "api: 127.0.0.1:1234\nmouse-button-mode: relative\n", []string{"-api", "", "-web", "127.0.0.1:9330", "-mouse-button-mode", "absolute"}, "", "127.0.0.1:9330", "absolute"},
		{"disable default API", "{}", []string{"-api=", "-web", ":0"}, "", ":0", "relative"},
		{"restore file-disabled API", "api: ''\nmouse-button-mode: absolute\n", []string{"-api", "127.0.0.1:9329", "-mouse-button-mode", "relative"}, "127.0.0.1:9329", "", "relative"},
		{"bare web overrides file", "web: ':1234'\n", []string{"-web"}, "127.0.0.1:9329", "127.0.0.1:9330", "relative"},
		{"empty web disables file", "web: ':1234'\n", []string{"-web="}, "127.0.0.1:9329", "", "relative"},
	} {
		t.Run(vector.name, func(t *testing.T) {
			path := writeConfig(t, vector.yaml)
			args := append([]string{"-config", path}, vector.args...)
			got, _, err := Parse(args)
			if err != nil || got.API != vector.api || got.Web != vector.web || got.MouseButtonMode != vector.mode {
				t.Fatalf("settings = %+v, %v; want API %q, web %q, mode %q", got, err, vector.api, vector.web, vector.mode)
			}
		})
	}
	for _, args := range [][]string{{"-api="}, {"-api=", "-web="}, {"-config", writeConfig(t, "api: ''\n")}} {
		if _, _, err := Parse(args); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "at least one") {
			t.Fatalf("both listeners disabled by %v: %v", args, err)
		}
	}
}

func TestOptionalWebAddress(t *testing.T) {
	for _, vector := range []struct {
		args []string
		web  string
	}{
		{[]string{"-web"}, "127.0.0.1:9330"},
		{[]string{"--web"}, "127.0.0.1:9330"},
		{[]string{"-web", "-web-fps", "30"}, "127.0.0.1:9330"},
		{[]string{"-web", "-autocrop=false"}, "127.0.0.1:9330"},
		{[]string{"-autocrop", "-web"}, "127.0.0.1:9330"},
		{[]string{"-web", "--"}, "127.0.0.1:9330"},
		{[]string{"-web", "127.0.0.1:1234"}, "127.0.0.1:1234"},
		{[]string{"--web=[::1]:1234"}, "[::1]:1234"},
		{[]string{"-web", ""}, ""},
		{[]string{"-web="}, ""},
		{[]string{"-web", "-web", ":1234"}, ":1234"},
		{[]string{"-web=:1234", "-web"}, "127.0.0.1:9330"},
		{[]string{"-token-file", "-web"}, ""},
		{[]string{"-serial-device", "--web"}, ""},
		{[]string{"-token-file=-web", "-web"}, "127.0.0.1:9330"},
	} {
		t.Run(fmt.Sprint(vector.args), func(t *testing.T) {
			original := append([]string(nil), vector.args...)
			got, action, err := Parse(vector.args)
			if err != nil || action != Daemon || got.Web != vector.web {
				t.Fatalf("Parse(%q) = %+v, %v, %v; want web %q", vector.args, got, action, err, vector.web)
			}
			if !reflect.DeepEqual(vector.args, original) {
				t.Fatalf("Parse changed arguments: %q; want %q", vector.args, original)
			}
		})
	}
	for _, args := range [][]string{{"--", "-web"}, {"word", "-web"}, {"-web", "--version"}} {
		if _, _, err := Parse(args); !errors.Is(err, ErrUsage) {
			t.Errorf("Parse(%q) = %v; want ErrUsage", args, err)
		}
	}
}

func TestPrecedenceAndAllSettings(t *testing.T) {
	path := writeConfig(t, `api: 127.0.0.1:1001
web: "[::1]:1002"
serial-device: /dev/serial/by-id/testing
serial-baud: 19200
screen-width: 1280
screen-height: 720
target-layout: se105
layouts-dir: ""
mouse-button-mode: relative
capture-device: /dev/video7
capture-width: 640
capture-height: 480
autocrop: true
web-fps: 30
audio-device: off
log-level: WARN
debug-keys: true
`)
	want := Config{
		API: "127.0.0.1:1001", Web: "[::1]:1002", SerialDevice: "/dev/serial/by-id/testing",
		SerialBaud: 19200, ScreenWidth: 1280, ScreenHeight: 720, TargetLayout: "se105",
		MouseButtonMode: "relative", CaptureDevice: "/dev/video7", CaptureWidth: 640,
		CaptureHeight: 480, Autocrop: true, WebFPS: 30, AudioDevice: "off", LogLevel: "WARN", DebugKeys: true,
	}
	got, _, err := Parse([]string{"-config", path})
	if err != nil || got != want {
		t.Fatalf("YAML settings = %+v, %v; want %+v", got, err, want)
	}

	want.API, want.Web, want.Autocrop, want.DebugKeys = "", ":0", false, false
	want.SerialDevice, want.AudioDevice, want.WebFPS = "", "", 1
	got, _, err = Parse([]string{
		"-config", path, "-api=", "-web", ":0", "-autocrop=false", "-debug-keys=false",
		"-serial-device=", "-audio-device=", "-web-fps", "1",
	})
	if err != nil || got != want {
		t.Fatalf("explicit zero/false/empty overlay = %+v, %v; want %+v", got, err, want)
	}
	if _, _, err := Parse([]string{"-config", path, "-api=", "-web="}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("flags did not disable both YAML listeners: %v", err)
	}
}

func TestEveryFlagOverridesFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "custom.yaml"), []byte("overrides: {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, "api: ':1'\nweb-fps: 0\n")
	args := []string{
		"-config", path, "-api", "localhost:22", "-web", "[::]:9330",
		"-serial-device", "/dev/ttyUSB4", "-serial-baud", "115200",
		"-screen-width", "1", "-screen-height", "2", "-target-layout", "custom",
		"-layouts-dir", directory, "-mouse-button-mode", "relative",
		"-capture-device", "/dev/video2", "-capture-width", "3", "-capture-height", "4",
		"-autocrop", "-web-fps", "120", "-audio-device", "/dev/snd/pcmC1D0c",
		"-log-level", "debug", "-debug-keys",
	}
	want := Config{
		API: "localhost:22", Web: "[::]:9330", SerialDevice: "/dev/ttyUSB4", SerialBaud: 115200,
		ScreenWidth: 1, ScreenHeight: 2, TargetLayout: "custom", LayoutsDir: directory,
		MouseButtonMode: "relative", CaptureDevice: "/dev/video2", CaptureWidth: 3,
		CaptureHeight: 4, Autocrop: true, WebFPS: 120, AudioDevice: "/dev/snd/pcmC1D0c", LogLevel: "debug", DebugKeys: true,
	}
	got, _, err := Parse(args)
	if err != nil || got != want {
		t.Fatalf("all flags = %+v, %v; want %+v", got, err, want)
	}
}

func TestStrictYAML(t *testing.T) {
	for name, contents := range map[string]string{
		"empty": "", "syntax": "api: [", "invalid UTF-8": "\xff", "root scalar": "word",
		"root null": "null", "root sequence": "[]", "numeric key": "1: value",
		"complex key": "[api]: value", "unknown key": "api-host: localhost",
		"nested config": "config: another.yaml", "version action": "version: true", "list action": "list-devices: true", "null string": "api: null",
		"null bool": "autocrop: null", "null int": "web-fps: null",
		"string number": "api: 9329", "string bool": "api: true",
		"quoted bool": "autocrop: 'false'", "legacy bool": "autocrop: yes",
		"quoted int": "web-fps: '20'", "float": "web-fps: 20.0",
		"huge int":       "web-fps: 999999999999999999999999999999999",
		"bad tagged int": "web-fps: !!int bad", "bad tagged bool": "autocrop: !!bool wrong",
		"numeric tagged bool": "autocrop: !!bool 1", "short tagged bool": "autocrop: !!bool t",
		"mapping value": "api: {}", "sequence value": "api: []",
		"alias":               "api: &address ':9329'\nweb: *address",
		"duplicate":           "api: ':9329'\napi: ':9330'",
		"merge":               "<<: {api: ':9329'}",
		"second document":     "api: ':9329'\n---\nweb: ':9330'",
		"bad second document": "api: ':9329'\n---\n[",
		"null MCP width":      "vision-max-width: null", "MCP width string": "vision-max-width: '960'",
		"MCP JSON mapping": "vision-request-json: {reasoning_effort: none}",
		"MCP duplicate":    "vision-model: one\nvision-model: two",
		"null token":       "token: null", "numeric token": "token: 1234",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, contents)
			cfg, _, err := Parse([]string{"-config", path, "-api", ":9329"})
			if cfg != (Config{}) || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), path) {
				t.Fatalf("Parse = %+v, %v; want invalid file with path", cfg, err)
			}
			var mcp MCP
			if mcp, err = ParseMCP([]string{"-config", path}); mcp != (MCP{}) || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), path) {
				t.Fatalf("ParseMCP = %+v, %v; want invalid file with path", mcp, err)
			}
		})
	}
	path := writeConfig(t, "api: ':9329'\nweb-fps: 044\nautocrop: TRUE\n")
	cfg, _, err := Parse([]string{"-config", path})
	if err != nil || cfg.WebFPS != 36 || !cfg.Autocrop {
		t.Fatalf("typed YAML values = %+v, %v", cfg, err)
	}
	path = writeConfig(t, "{}")
	if _, _, err := Parse([]string{"-config", path, "-api", ":9329"}); err != nil {
		t.Fatalf("empty mapping: %v", err)
	}
}

func TestFileAndArgumentErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, path := range []string{missing, t.TempDir(), ""} {
		if _, _, err := Parse([]string{"-config", path, "-api", ":9329"}); !errors.Is(err, ErrInvalid) {
			t.Errorf("selected file %q: %v", path, err)
		}
	}
	if _, _, err := Parse([]string{"-config", missing, "-api", ":9329"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file cause not preserved: %v", err)
	}
	for _, args := range [][]string{{"-unknown"}, {"-api"}, {"-web-fps", "bad"}, {"-autocrop=maybe"}, {"word", "-api", ":1"}} {
		if _, _, err := Parse(args); !errors.Is(err, ErrUsage) {
			t.Errorf("args %v: %v; want ErrUsage", args, err)
		}
	}
	if _, _, err := Parse([]string{"-config", missing, "-help"}); err != flag.ErrHelp {
		t.Fatalf("help read config or validated listeners: %v", err)
	}
}

func TestAggregatedValidation(t *testing.T) {
	cfg := Defaults()
	cfg.API, cfg.Web = "missing-port", ":65536"
	cfg.SerialBaud, cfg.ScreenWidth, cfg.ScreenHeight = 1, 0, -1
	cfg.TargetLayout, cfg.MouseButtonMode = "missing-layout", "wrong"
	cfg.CaptureDevice, cfg.CaptureWidth, cfg.CaptureHeight = "0", 0, -1
	cfg.WebFPS, cfg.AudioDevice, cfg.LogLevel = 0, "2", "verbose"
	err := cfg.Validate()
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	last := -1
	for _, setting := range []string{"api:", "web:", "serial-baud:", "screen-width:", "screen-height:", "target-layout:", "mouse-button-mode:", "capture-device:", "capture-width:", "capture-height:", "web-fps:", "audio-device:", "log-level:"} {
		position := strings.Index(err.Error(), setting)
		if position <= last {
			t.Fatalf("missing/out-of-order %s in %v", setting, err)
		}
		last = position
	}
	if _, _, err := Parse([]string{"-api", ":9329", "-serial-baud", "0", "-screen-width", "0", "-web-fps", "121"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "screen-width:") || !strings.Contains(err.Error(), "web-fps:") {
		t.Fatalf("merged validation = %v", err)
	}
}

func TestSupportedValuesAndBounds(t *testing.T) {
	for _, baud := range []int{1200, 1800, 2400, 4800, 9600, 19200, 38400, 57600, 115200} {
		cfg := Defaults()
		cfg.API, cfg.SerialBaud = "localhost:9329", baud
		if err := cfg.Validate(); err != nil {
			t.Errorf("supported baud %d: %v", baud, err)
		}
	}
	for _, address := range []string{"127.0.0.1:9329", ":0", "[::1]:65535", "localhost:1"} {
		cfg := Defaults()
		cfg.Web = address
		cfg.ScreenWidth, cfg.ScreenHeight = 1, int(^uint(0)>>1)
		maxCapture := uint64(math.MaxUint32)
		cfg.CaptureWidth, cfg.CaptureHeight = int(maxCapture), 1
		if err := cfg.Validate(); err != nil {
			t.Errorf("supported address/dimensions %q: %v", address, err)
		}
		got, _, err := Parse([]string{"-web", address})
		if err != nil || got.Web != address {
			t.Errorf("address changed: %+v, %v", got, err)
		}
	}
	for _, address := range []string{"localhost:", "::1:9329", ":-1", ":65536", ":http", ":+1", ":99999999999999999999999"} {
		cfg := Defaults()
		cfg.API = address
		if err := cfg.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid address %q: %v", address, err)
		}
	}
	tooLarge := uint64(math.MaxUint32) + 1
	if _, _, err := Parse([]string{"-api", ":1", "-capture-width", strconv.FormatUint(tooLarge, 10)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("capture dimension did not enforce uint32: %v", err)
	}
	for _, device := range []string{"0", "+0", "-1", "99999999999999999999999999999"} {
		for _, setting := range []string{"capture-device", "audio-device"} {
			if _, _, err := Parse([]string{"-api", ":1", "-" + setting + "=" + device}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), setting) {
				t.Errorf("%s index %q: %v", setting, device, err)
			}
		}
	}
	for _, device := range []string{"/dev/video0", "relative-device", "./0", "+", "-"} {
		if _, _, err := Parse([]string{"-api", ":1", "-capture-device=" + device}); err != nil {
			t.Errorf("device path %q rejected: %v", device, err)
		}
	}
}

func TestNoEnvironmentOrAutomaticFiles(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	t.Setenv("XDG_CONFIG_HOME", directory)
	t.Setenv("AUTOPAWS_CONFIG", filepath.Join(directory, "missing.yaml"))
	t.Setenv("AUTOPAWS_API", "1")
	t.Setenv("AUTOPAWS_SERIAL_BAUD", "1")
	if err := os.WriteFile("autopaws.yaml", []byte("api: ':1'\nserial-baud: 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, err := Parse([]string{"-web", ":9330"})
	want := Defaults()
	want.Web = ":9330"
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("environment/default files changed settings: %+v, %v", got, err)
	}
}

func TestUsageAndLoggers(t *testing.T) {
	var usage bytes.Buffer
	WriteUsage(&usage)
	for _, setting := range []string{"config", "api", "web", "serial-device", "serial-baud", "screen-width", "screen-height", "target-layout", "layouts-dir", "mouse-button-mode", "mouse-absolute-refresh", "capture-device", "capture-width", "capture-height", "autocrop", "web-fps", "audio-device", "log-level", "debug-keys"} {
		if !strings.Contains(usage.String(), "-"+setting) {
			t.Errorf("usage omitted %s: %s", setting, usage.String())
		}
	}
	for _, level := range []string{"debug", "info", "warn", "error"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprint(level, "/keys=", enabled), func(t *testing.T) {
				var output bytes.Buffer
				cfg := Defaults()
				cfg.LogLevel, cfg.DebugKeys = level, enabled
				ordinary, keys := cfg.Loggers(&output)
				ordinary.Debug("ordinary debug")
				ordinary.Error("ordinary error")
				if enabled {
					keys.Debug("typed secret")
				} else if keys != nil {
					t.Fatal("disabled key logger is not nil")
				}
				text := output.String()
				if strings.Contains(text, "ordinary debug") != (level == "debug") || !strings.Contains(text, "ordinary error") || strings.Contains(text, "typed secret") != enabled {
					t.Fatalf("incorrect log thresholds: %s", text)
				}
			})
		}
	}
	var output bytes.Buffer
	cfg := Defaults()
	cfg.DebugKeys = true
	ordinary, keys := cfg.Loggers(&output)
	var workers sync.WaitGroup
	for _, logger := range []interface{ Info(string, ...any) }{ordinary, keys} {
		workers.Go(func() {
			for range 20 {
				logger.Info("concurrent record")
			}
		})
	}
	workers.Wait()
	if strings.Count(output.String(), "concurrent record") != 40 {
		t.Fatalf("log records lost or corrupted: %s", output.String())
	}
}
