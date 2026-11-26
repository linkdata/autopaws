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
	if strings.Contains(usage.String(), "\n  -token ") {
		t.Fatal("inline token flag advertised in usage")
	}
}

func TestInlineTokenSharedYAMLSetting(t *testing.T) {
	for _, vector := range []struct {
		yaml string
		want string
	}{
		{"token: '  a+;å% secret  '\n", "  a+;å% secret  "},
		{"token: ''\n", ""},
	} {
		args := []string{"-config", writeConfig(t, vector.yaml)}
		var cfg Config
		var mcp MCP
		var err error
		if cfg, _, err = Parse(args); err != nil || cfg.Token != vector.want {
			t.Fatalf("KVM direct token setting failed: %v", err)
		}
		if mcp, err = ParseMCP(args); err != nil || mcp.Token != vector.want {
			t.Fatalf("MCP direct token setting failed: %v", err)
		}
	}
}

func TestInlineTokenFlagRejected(t *testing.T) {
	path := writeConfig(t, "token: yaml-secret\n")
	for _, args := range [][]string{
		{"-token", "cli-secret"},
		{"--token", "cli-secret"},
		{"-token=cli-secret"},
		{"--token=cli-secret"},
		{"-token="},
		{"--token="},
		{"-token"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			args := append([]string{"-config", path}, args...)
			if _, _, err := Parse(args); !errors.Is(err, ErrUsage) || strings.Contains(err.Error(), "cli-secret") {
				t.Fatalf("KVM inline token flag: %v; want ErrUsage without secret", err)
			}
			if _, err := ParseMCP(args); !errors.Is(err, ErrUsage) || strings.Contains(err.Error(), "cli-secret") {
				t.Fatalf("MCP inline token flag: %v; want ErrUsage without secret", err)
			}
		})
	}
}
