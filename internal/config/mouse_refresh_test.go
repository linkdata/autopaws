package config

import (
	"errors"
	"testing"
)

func TestMouseAbsoluteRefresh(t *testing.T) {
	for _, test := range []struct {
		name, file string
		args       []string
		want       bool
	}{
		{"default", "{}", nil, false},
		{"file enabled", "mouse-absolute-refresh: true\n", nil, true},
		{"flag enabled", "mouse-absolute-refresh: false\n", []string{"-mouse-absolute-refresh"}, true},
		{"flag disabled", "mouse-absolute-refresh: true\n", []string{"-mouse-absolute-refresh=false"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeConfig(t, test.file)
			args := append([]string{"-config", path}, test.args...)
			cfg, _, err := Parse(args)
			if err != nil || cfg.MouseAbsoluteRefresh != test.want {
				t.Fatalf("absolute refresh = %t, %v; want %t", cfg.MouseAbsoluteRefresh, err, test.want)
			}
			if mcp, err := ParseMCP([]string{"-config", path}); err != nil || mcp != MCPDefaults() {
				t.Fatalf("MCP did not accept the shared KVM setting: %+v, %v", mcp, err)
			}
		})
	}
	if _, _, err := Parse([]string{"-mouse-absolute-refresh=maybe"}); !errors.Is(err, ErrUsage) {
		t.Fatalf("invalid refresh boolean accepted: %v", err)
	}
}
