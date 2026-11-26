//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linkdata/autopaws/apiclient"
)

func TestConcurrentKeyboardLayoutSelectionOverTCP(t *testing.T) {
	const custom = "custom-keyboard-layout"
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, custom+".yaml"), []byte("overrides:\n  a: [none, 5]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, controller, _ := bareServer(t, Options{KeyboardLayout: "us104", LayoutsDir: directory})
	address, _ := serveForTest(t, server, controller)
	client := apiclient.New(address)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start, done := make(chan struct{}), make(chan struct{})
	results := make(chan error, 3)
	run := func(work func() error) {
		go func() {
			<-start
			results <- work()
		}()
	}
	switchLayouts := func() error {
		for i := 0; ; i++ {
			select {
			case <-done:
				return nil
			default:
			}
			name := []string{"us104", custom}[i%2]
			var reply struct {
				Layout string `json:"keyboard_layout"`
			}
			if err := client.Call(ctx, "set_keyboard_layout", map[string]string{"layout": name}, &reply); err != nil {
				return fmt.Errorf("select layout: %w", err)
			}
			if reply.Layout != name {
				return fmt.Errorf("selection response does not name the requested layout")
			}
		}
	}
	run(switchLayouts)
	run(switchLayouts)
	run(func() error {
		defer close(done)
		for range 16 {
			for _, request := range []struct{ method, params string }{
				{"get_device_info", `{}`},
				{"list_keyboard_layouts", `{}`},
				{"type_text", `{"text":"a","char_delay_ms":0}`},
				{"send_key", `{"key":"a"}`},
				{"send_key_sequence", `{"steps":[{"key":"a","delay_ms":0}],"default_delay_ms":0}`},
			} {
				var reply struct {
					Layout string `json:"keyboard_layout"`
					Config struct {
						Layout string `json:"keyboard_layout"`
					} `json:"config"`
				}
				if err := client.Call(ctx, request.method, json.RawMessage(request.params), &reply); err != nil {
					return fmt.Errorf("%s: %w", request.method, err)
				}
				name := reply.Layout
				if request.method == "get_device_info" {
					name = reply.Config.Layout
				}
				if request.method == "get_device_info" || request.method == "list_keyboard_layouts" {
					if name != "us104" && name != custom {
						return fmt.Errorf("%s returned an unknown layout name", request.method)
					}
				}
			}
		}
		return nil
	})
	close(start)
	for range 3 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}
