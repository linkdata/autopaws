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
		got, err := ParseServe(vector.args)
		if err != nil || got.TokenFile != vector.want {
			t.Fatalf("token file setting = %q, %v; want %q", got.TokenFile, err, vector.want)
		}
	}
	if _, err := ParseServe([]string{"-token-file", "missing", "-help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help read token file: %v", err)
	}
	var usage bytes.Buffer
	WriteServeUsage(&usage)
	if !strings.Contains(usage.String(), "-token-file") {
		t.Fatal("token file omitted from usage")
	}
}
