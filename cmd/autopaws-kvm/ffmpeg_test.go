//go:build (darwin || windows) && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/linkdata/autopaws/internal/config"
)

func TestServeRuntimeRequiresFFmpegBeforeCreatingOwners(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cfg := config.Defaults()
	cfg.SerialBaud = 1 // A transport must not be constructed before this check.
	err := serveRuntime(t.Context(), cfg, io.Discard)
	hint := "brew install ffmpeg"
	if runtime.GOOS == "windows" {
		hint = "docs/windows-setup.md"
	}
	if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), hint) {
		t.Fatalf("missing FFmpeg startup error = %v", err)
	}
}

func TestHelpAndVersionDoNotRequireFFmpeg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{{"--help"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr, commandHooks{}); code != 0 || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatalf("%v without FFmpeg: exit %d, stdout %q, stderr %q", args, code, stdout.String(), stderr.String())
		}
	}
}
