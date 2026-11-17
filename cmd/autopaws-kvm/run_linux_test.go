//go:build linux && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linkdata/autopaws/internal/config"
)

func TestHelpAndUsageErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	for _, vector := range []struct {
		args []string
		code int
		text string
	}{
		{nil, 2, "Usage:"},
		{[]string{"unknown"}, 2, "unknown command"},
		{[]string{"-help"}, 0, "Usage:"},
		{[]string{"--help"}, 0, "Usage:"},
		{[]string{"-h"}, 0, "Usage:"},
		{[]string{"serve", "-config", missing, "-help"}, 0, "-serial-baud"},
		{[]string{"serve", "-no-such-flag"}, 2, "flag provided but not defined"},
		{[]string{"serve", "-api"}, 2, "flag needs an argument"},
		{[]string{"serve", "word"}, 2, "unexpected arguments"},
		{[]string{"serve"}, 2, "at least one of api or web"},
		{[]string{"serve", "-config", missing}, 2, "read"},
		{[]string{"list-devices", "-help"}, 0, "list-devices"},
		{[]string{"list-devices", "-api", ":1"}, 2, "flag provided but not defined"},
		{[]string{"list-devices", "word"}, 2, "unexpected arguments"},
		{[]string{"version", "-help"}, 0, "version"},
		{[]string{"version", "-config", missing}, 2, "flag provided but not defined"},
		{[]string{"version", "word"}, 2, "unexpected arguments"},
	} {
		t.Run(fmt.Sprint(vector.args), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			hooks := commandHooks{
				serve: func(context.Context, config.Config) error {
					t.Fatal("help/invalid invocation started serve")
					return nil
				},
				listDevices: func(io.Writer) error {
					t.Fatal("help/invalid invocation probed devices")
					return nil
				},
			}
			if code := run(context.Background(), vector.args, &stdout, &stderr, hooks); code != vector.code {
				t.Fatalf("exit = %d; want %d; stdout %s; stderr %s", code, vector.code, stdout.String(), stderr.String())
			}
			output, other := &stderr, &stdout
			if vector.code == 0 {
				output, other = &stdout, &stderr
			}
			if !strings.Contains(output.String(), vector.text) || other.Len() != 0 {
				t.Fatalf("unexpected output streams: stdout %q, stderr %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &stdout, &stderr, commandHooks{}); code != 0 || stdout.String() != "autopaws-kvm "+version+"\n" || stderr.Len() != 0 {
		t.Fatalf("version = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestCommandHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.yaml")
	if err := os.WriteFile(path, []byte("api: ':0'\nserial-baud: 19200\nautocrop: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "passed")
	for _, failure := range []bool{false, true} {
		for _, command := range []string{"serve", "list-devices"} {
			t.Run(fmt.Sprint(command, "/failure=", failure), func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				called := false
				var result error
				if failure {
					result = errors.New("runtime failed")
				}
				hooks := commandHooks{
					serve: func(gotCtx context.Context, cfg config.Config) error {
						called = true
						want := config.Defaults()
						want.API, want.SerialBaud, want.Autocrop = ":0", 19200, false
						if gotCtx.Value(contextKey{}) != "passed" || cfg != want {
							t.Fatalf("serve received ctx %v, cfg %+v; want %+v", gotCtx, cfg, want)
						}
						return result
					},
					listDevices: func(output io.Writer) error {
						called = true
						fmt.Fprintln(output, "device listing")
						return result
					},
				}
				args := []string{command}
				if command == "serve" {
					args = append(args, "-config", path)
				}
				code := run(ctx, args, &stdout, &stderr, hooks)
				wantCode := 0
				if failure {
					wantCode = 1
				}
				if !called || code != wantCode || strings.Contains(stderr.String(), "runtime failed") != failure {
					t.Fatalf("called %v, exit %d; stdout %q, stderr %q", called, code, stdout.String(), stderr.String())
				}
				if command == "list-devices" && stdout.String() != "device listing\n" {
					t.Fatalf("listing did not use stdout: %q", stdout.String())
				}
			})
		}
	}
}
