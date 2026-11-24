package config

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestTokenFileSettingPrecedenceAndHelp(t *testing.T) {
	path := writeConfig(t, "api: ':0'\ntoken-file: /not-opened-at-parse-time\n")
	for _, vector := range []struct {
		args []string
		want string
	}{
		{[]string{"-config", path}, "/not-opened-at-parse-time"},
		{[]string{"-config", path, "-token-file", "another"}, "another"},
		{[]string{"-config", path, "-token-file="}, ""},
	} {
		got, _, err := Parse(vector.args)
		if err != nil || got.TokenFile != vector.want {
			t.Fatalf("token file setting = %q, %v; want %q", got.TokenFile, err, vector.want)
		}
	}
	if _, _, err := Parse([]string{"-token-file", "missing", "-help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help read token file: %v", err)
	}
	var usage bytes.Buffer
	WriteUsage(&usage)
	if !strings.Contains(usage.String(), "-token-file") {
		t.Fatal("token file omitted from usage")
	}
}

func TestInlineTokenSharedSettingAndOverrides(t *testing.T) {
	path := writeConfig(t, "token: '  a+;å% secret  '\n")
	for _, vector := range []struct {
		args []string
		want string
	}{
		{[]string{"-config", path}, "  a+;å% secret  "},
		{[]string{"-config", path, "-token", "another"}, "another"},
		{[]string{"-config", path, "-token="}, ""},
	} {
		var cfg Config
		var mcp MCP
		var err error
		if cfg, _, err = Parse(vector.args); err != nil || cfg.Token != vector.want {
			t.Fatalf("KVM direct token setting failed: %v", err)
		}
		if mcp, err = ParseMCP(vector.args); err != nil || mcp.Token != vector.want {
			t.Fatalf("MCP direct token setting failed: %v", err)
		}
	}
}
