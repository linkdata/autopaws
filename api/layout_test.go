//go:build (linux || darwin) && (amd64 || arm64)

package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/hid"
)

func TestKeyboardLayoutSelectionAndDiscovery(t *testing.T) {
	directory := t.TempDir()
	server, _, transport := bareServer(t, Options{KeyboardLayout: "us104", LayoutsDir: directory})
	call := func(method, data string) any {
		t.Helper()
		result, err := server.handlers[method](context.Background(), json.RawMessage(data))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	active := func(want string) {
		t.Helper()
		info := call("get_device_info", `{}`).(map[string]any)
		if got := info["config"].(map[string]any)["keyboard_layout"]; got != want {
			t.Fatalf("active name = %v; want %s", got, want)
		}
	}
	// Probe complete-request validation with an already canceled context, so
	// supported characters stop before preparing disconnected hardware.
	probe := func(text string, want error) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := server.keyboard.TypeText(ctx, text, hid.TypeOptions{Raw: true}); !errors.Is(err, want) {
			t.Fatalf("text %q: %v; want %v", text, err, want)
		}
	}
	active("us104")
	probe("å", hid.ErrUnsupported)
	if result := call("set_keyboard_layout", `{"layout":"se105"}`).(map[string]string); result["keyboard_layout"] != "se105" {
		t.Fatalf("wrong selected name: %v", result)
	}
	active("se105")
	probe("å", context.Canceled)
	call("set_keyboard_layout", `{"layout":"us104"}`)
	active("us104")
	probe("å", hid.ErrUnsupported)
	path := filepath.Join(directory, "se105.yaml")
	for _, text := range []string{"é", "µ"} {
		if err := os.WriteFile(path, []byte("overrides:\n  \""+text+"\": [none, 4]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		call("set_keyboard_layout", `{"layout":"se105"}`)
		probe(text, context.Canceled)
		probe("å", hid.ErrUnsupported) // Custom file uses the US base.
	}
	probe("é", hid.ErrUnsupported) // Same-name selection reloaded the file.
	if err := os.WriteFile(path, []byte("overrides: [broken]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "absent"), filepath.Join(directory, "unreadable.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		params string
		code   apiwire.Code
	}{
		{`{}`, apiwire.ErrInvalidParams},
		{`{"layout":null}`, apiwire.ErrInvalidParams},
		{`{"layout":4}`, apiwire.ErrInvalidParams},
		{`{"layout":""}`, apiwire.ErrInvalidParams},
		{`{"layout":"../us104"}`, apiwire.ErrInvalidParams},
		{`{"layout":"a\u0000"}`, apiwire.ErrInvalidParams},
		{`{"layout":"` + strings.Repeat("x", 256) + `"}`, apiwire.ErrInvalidParams},
		{`{"layout":"unknown"}`, apiwire.ErrInvalidParams},
		{`{"layout":"se105"}`, apiwire.ErrInvalidParams},
		{`{"layout":"unreadable"}`, apiwire.ErrUnavailable},
		{`{"layout":"us104","extra":true}`, apiwire.ErrInvalidParams},
	} {
		_, err := server.handlers["set_keyboard_layout"](context.Background(), json.RawMessage(test.params))
		if err == nil {
			t.Fatalf("accepted invalid selection %s", test.params)
		}
		if code, _ := classifyError(err); code != test.code {
			t.Fatalf("%s: %v; want %s", test.params, err, test.code)
		}
		active("se105")
		probe("µ", context.Canceled)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.handlers["set_keyboard_layout"](ctx, json.RawMessage(`{"layout":"us104"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled switch: %v", err)
	}
	active("se105")
	listed, err := server.handlers["list_keyboard_layouts"](context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		Name    string           `json:"keyboard_layout"`
		Layouts []hid.LayoutInfo `json:"layouts"`
	}
	if err := json.Unmarshal(encoded, &inventory); err != nil || inventory.Name != "se105" || len(inventory.Layouts) != 7 {
		t.Fatalf("inventory: %s; %v", encoded, err)
	}
	active("se105")
	probe("µ", context.Canceled) // Listing a changed file did not replace it.
	if transport.prepares != 0 || transport.writes != 0 {
		t.Fatal("layout discovery, selection or rejected input touched hardware")
	}
	if server.options.KeyboardLayout != "us104" {
		t.Fatal("runtime selection rewrote the startup default")
	}
}

func TestKeyboardLayoutDirectoryReadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{path, filepath.Join(t.TempDir(), strings.Repeat("x", 256))} {
		server, _, transport := bareServer(t, Options{LayoutsDir: directory})
		_, err := server.handlers["list_keyboard_layouts"](context.Background(), json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("accepted an unreadable layout directory")
		}
		if code, _ := classifyError(err); code != apiwire.ErrUnavailable || transport.prepares != 0 || transport.writes != 0 {
			t.Fatalf("unreadable directory: %v", err)
		}
	}
}
