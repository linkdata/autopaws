package config

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestStandaloneActions(t *testing.T) {
	for _, vector := range []struct {
		args []string
		want Action
	}{
		{[]string{"--version"}, Version},
		{[]string{"-version=true"}, Version},
		{[]string{"--list-devices"}, ListDevices},
		{[]string{"-list-devices=true"}, ListDevices},
	} {
		cfg, action, err := Parse(vector.args)
		if err != nil || cfg != (Config{}) || action != vector.want {
			t.Fatalf("Parse(%v) = %+v, %v, %v", vector.args, cfg, action, err)
		}
	}
	for _, args := range [][]string{
		{"--version", "--list-devices"},
		{"--version", "--list-devices=false"},
		{"--version", "-config", filepath.Join(t.TempDir(), "missing")},
		{"--list-devices", "-token-file", "missing"},
		{"-api=", "--version"},
		{"serve"},
		{"version"},
		{"list-devices"},
	} {
		if _, _, err := Parse(args); !errors.Is(err, ErrUsage) || errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Parse(%v) = %v; want usage error before file access", args, err)
		}
	}
	for _, args := range [][]string{{"--version=false"}, {"--list-devices=false"}, {"--version=false", "--list-devices=false"}} {
		cfg, action, err := Parse(args)
		if err != nil || action != Daemon || cfg != Defaults() {
			t.Fatalf("false selectors changed daemon defaults: %+v, %v, %v", cfg, action, err)
		}
	}
	for _, args := range [][]string{{"-config", "missing", "--help"}, {"--version", "-config", "missing", "--help"}} {
		if _, _, err := Parse(args); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("help loaded configuration: %v", err)
		}
	}
}

func TestActionNamesAsFlagValues(t *testing.T) {
	for _, name := range []string{"--version", "--list-devices"} {
		cfg, action, err := Parse([]string{"-serial-device", name, "-token-file", name})
		if err != nil || action != Daemon || cfg.SerialDevice != name || cfg.TokenFile != name {
			t.Fatalf("literal setting value became action: %+v, %v, %v", cfg, action, err)
		}
	}
	t.Chdir(t.TempDir())
	if err := os.WriteFile("--version", []byte("serial-baud: 19200\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, action, err := Parse([]string{"-config", "--version"})
	if err != nil || action != Daemon || cfg.SerialBaud != 19200 {
		t.Fatalf("literal config path became action: %+v, %v, %v", cfg, action, err)
	}
}
