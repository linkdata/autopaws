//go:build (linux || darwin) && (amd64 || arm64)

package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/linkdata/autopaws/apiwire"
)

func TestKeyboardLayoutSelectionDanglingSymlink(t *testing.T) {
	directory := t.TempDir()
	if err := os.Symlink(filepath.Join(directory, "absent"), filepath.Join(directory, "unreadable.yaml")); err != nil {
		t.Fatal(err)
	}
	server, _, transport := bareServer(t, Options{KeyboardLayout: "us104", LayoutsDir: directory})
	_, err := server.handlers["set_keyboard_layout"](context.Background(), json.RawMessage(`{"layout":"unreadable"}`))
	if err == nil {
		t.Fatal("accepted a dangling layout symlink")
	}
	if code, _ := classifyError(err); code != apiwire.ErrUnavailable {
		t.Fatalf("dangling layout symlink: %v; want %s", err, apiwire.ErrUnavailable)
	}
	if server.layoutName != "us104" || transport.prepares != 0 || transport.writes != 0 {
		t.Fatal("failed layout selection changed the active layout or touched hardware")
	}
}
