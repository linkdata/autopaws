//go:build linux && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linkdata/autopaws/devices"
)

func TestListDevicesRuntime(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "class", "tty"), 0o755); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := listDevicesWith(t.Context(), &output, devices.Options{SysRoot: root}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "(none)") != 3 {
		t.Fatalf("empty hardware output = %q", output.String())
	}
	if err := listDevicesWith(t.Context(), &output, devices.Options{SysRoot: filepath.Join(root, "missing")}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("discovery failure lost: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := listDevicesRuntime(ctx, &output); !errors.Is(err, context.Canceled) {
		t.Fatalf("default discovery lost cancellation: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"--list-devices"}, &stdout, &stderr, commandHooks{}); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("production list hook: exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}
