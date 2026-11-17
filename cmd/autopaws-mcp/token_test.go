package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandTokenHelpCancellationAndReadErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-token")
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		args := []string{"-token-file", missing, "-help"}
		if canceled {
			cancel()
			args = args[:2]
		}
		var stdout, stderr bytes.Buffer
		if code := run(ctx, args, nil, &stdout, &stderr); code != 0 || stderr.Len() != 0 || !canceled && !strings.Contains(stdout.String(), "-token-file") {
			t.Fatalf("help/cancellation read token: exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
		}
		cancel()
	}
	for _, path := range []string{missing, filepath.Join(t.TempDir(), "empty-token")} {
		if path != missing {
			if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), []string{"-token-file", path}, nil, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), path) {
			t.Fatalf("token read error = exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
		}
	}
}
