package config

import (
	"errors"
	"testing"
)

func TestMouseStartupMode(t *testing.T) {
	for _, test := range []struct {
		file string
		args []string
		want string
	}{
		{"{}", nil, ""},
		{"mouse-mode: extended\n", nil, "extended"},
		{"mouse-mode: extended\n", []string{"-mouse-mode", "legacy"}, "legacy"},
		{"mouse-mode: legacy\n", []string{"-mouse-mode", "extended"}, "extended"},
	} {
		args := append([]string{"-config", writeConfig(t, test.file)}, test.args...)
		cfg, _, err := Parse(args)
		if err != nil || cfg.MouseMode != test.want {
			t.Fatalf("mouse mode %q/%v = %q, %v; want %q", test.file, test.args, cfg.MouseMode, err, test.want)
		}
	}
	if _, _, err := Parse([]string{"-mouse-mode", "guess"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid mouse mode accepted: %v", err)
	}
}
