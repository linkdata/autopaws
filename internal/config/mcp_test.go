package config

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPDefaultsAndSharedFile(t *testing.T) {
	want := MCP{API: "127.0.0.1:9329", VisionMaxWidth: 960}
	var got MCP
	var err error
	if got, err = ParseMCP(nil); err != nil || got != want || MCPDefaults() != want {
		t.Fatalf("MCP defaults = %+v, %v; want %+v", got, err, want)
	}
	path := writeConfig(t, `api: localhost:1234
token-file: api.token
serial-device: /dev/test
target-layout: us104
autocrop: true
capture-log: captures
tesseract: /test/tesseract
vision-api: http://localhost:11434/v1
vision-model: test-model
vision-token-file: vision.token
vision-max-width: 1280
vision-request-json: '{"reasoning_effort":"none"}'
`)
	want = MCP{
		API: "localhost:1234", TokenFile: "api.token", CaptureLog: "captures", Tesseract: "/test/tesseract",
		VisionAPI: "http://localhost:11434/v1", VisionModel: "test-model", VisionTokenFile: "vision.token",
		VisionMaxWidth: 1280, VisionRequestJSON: `{"reasoning_effort":"none"}`,
	}
	if got, err = ParseMCP([]string{"-config", path}); err != nil || got != want {
		t.Fatalf("MCP YAML = %+v, %v; want %+v", got, err, want)
	}
	var kvm Config
	if kvm, _, err = Parse([]string{"-config", path}); err != nil || kvm.API != want.API || kvm.TokenFile != want.TokenFile || kvm.SerialDevice != "/dev/test" || !kvm.Autocrop {
		t.Fatalf("KVM shared YAML = %+v, %v", kvm, err)
	}
	if got, err = ParseMCP([]string{
		"-config", path, "-api", "127.0.0.1:5678", "-token-file=", "-capture-log=", "-tesseract=",
		"-vision-api=", "-vision-model=", "-vision-token-file=", "-vision-max-width=0", "-vision-request-json=",
	}); err != nil || got != (MCP{API: "127.0.0.1:5678"}) {
		t.Fatalf("MCP explicit empty/zero overrides = %+v, %v", got, err)
	}
}

func TestMCPFileAndArgumentErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	for _, path := range []string{missing, t.TempDir(), ""} {
		if _, err := ParseMCP([]string{"-config", path}); !errors.Is(err, ErrInvalid) {
			t.Errorf("MCP selected file %q: %v", path, err)
		}
	}
	if _, err := ParseMCP([]string{"-config", missing}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("MCP missing file cause lost: %v", err)
	}
	for _, args := range [][]string{{"-unknown"}, {"-api"}, {"extra"}, {"-vision-max-width=bad"}, {"-target-layout=us104"}} {
		if _, err := ParseMCP(args); !errors.Is(err, ErrUsage) {
			t.Errorf("MCP args %v: %v; want ErrUsage", args, err)
		}
	}
	if _, err := ParseMCP([]string{"-config", missing, "-help"}); err != flag.ErrHelp {
		t.Fatalf("MCP help loaded configuration: %v", err)
	}
}

func TestSharedFileValidatesOnlyOwnRuntimeSettings(t *testing.T) {
	path := writeConfig(t, "target-layout: absent\nserial-baud: -1\n")
	if _, err := ParseMCP([]string{"-config", path}); err != nil {
		t.Fatalf("MCP validated KVM hardware settings: %v", err)
	}
	path = writeConfig(t, "vision-api: invalid-url\nvision-max-width: -1\n")
	if _, _, err := Parse([]string{"-config", path}); err != nil {
		t.Fatalf("KVM validated MCP vision settings: %v", err)
	}
}

func TestMCPUsageAndNoAutomaticFiles(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	t.Setenv("AUTOPAWS_CONFIG", filepath.Join(directory, "missing.yaml"))
	if err := os.WriteFile("autopaws.yaml", []byte("api: localhost:1\ncapture-log: captures\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ParseMCP(nil); err != nil || got != MCPDefaults() {
		t.Fatalf("MCP loaded implicit config: %+v, %v", got, err)
	}
	var usage bytes.Buffer
	WriteMCPUsage(&usage)
	for _, setting := range []string{"config", "api", "token-file", "capture-log", "tesseract", "vision-api", "vision-model", "vision-token-file", "vision-max-width", "vision-request-json"} {
		if !strings.Contains(usage.String(), "-"+setting) {
			t.Errorf("MCP usage omitted %s", setting)
		}
	}
	if strings.Contains(usage.String(), "\n  -token ") {
		t.Fatal("inline token flag advertised in MCP usage")
	}
}
