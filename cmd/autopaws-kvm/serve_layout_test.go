//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/hid"
)

func TestRuntimeKeyboardLayoutDirectoryAndRestart(t *testing.T) {
	cfg := runtimeConfig()
	cfg.LayoutsDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.LayoutsDir, "test-target.yaml"), []byte("overrides:\n  \"é\": [none, 4]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for runIndex := range 2 {
		port := &runtimeTransport{}
		run := startCaptureRuntime(t, cfg, port, nil, nil)
		var info struct {
			Config struct {
				Layout string `json:"keyboard_layout"`
			} `json:"config"`
		}
		if err := runtimeCall(run.client, "get_device_info", nil, &info); err != nil || info.Config.Layout != cfg.TargetLayout {
			t.Fatalf("startup %d layout = %s; %v", runIndex, info.Config.Layout, err)
		}
		if runIndex == 1 {
			if err := runtimeCall(run.client, "send_key", map[string]string{"key": "é"}, nil); !errors.Is(err, apiwire.ErrUnsupported) {
				t.Fatalf("restart retained the runtime map: %v", err)
			}
		}
		var inventory struct {
			Layouts []hid.LayoutInfo `json:"layouts"`
		}
		if err := runtimeCall(run.client, "list_keyboard_layouts", nil, &inventory); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range inventory.Layouts {
			if entry.Name == "test-target" {
				found = entry.Source == "custom" && entry.Error == ""
			}
		}
		if !found {
			t.Fatalf("configured custom layout missing: %+v", inventory.Layouts)
		}
		if runIndex == 0 {
			if err := runtimeCall(run.client, "set_keyboard_layout", map[string]string{"layout": "test-target"}, nil); err != nil {
				t.Fatal(err)
			}
			if prepares, _, frames := port.snapshot(); prepares != 0 || len(frames) != 0 {
				t.Fatal("runtime selection prepared serial hardware")
			}
			if err := runtimeCall(run.client, "send_key", map[string]string{"key": "é"}, nil); err != nil {
				t.Fatal(err)
			}
			_, _, frames := port.snapshot()
			if len(frames) != 2 || !bytes.Equal(frames[0], ch9329.Keyboard(0, 4)) || !bytes.Equal(frames[1], ch9329.Keyboard(0)) {
				t.Fatalf("custom map was not used: %x", frames)
			}
		}
		if err := stopCaptureRuntime(t, run); err != nil {
			t.Fatal(err)
		}
	}
}
