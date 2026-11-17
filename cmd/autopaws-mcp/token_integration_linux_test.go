//go:build linux && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linkdata/autopaws/api"
	"github.com/linkdata/autopaws/hid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCommandTokenFileAgainstAuthenticatedAPI(t *testing.T) {
	port := &realMCPRecordingTransport{}
	controller := hid.NewController(port)
	layout, err := hid.LoadLayout("us104", "")
	if err != nil {
		t.Fatal(err)
	}
	mouse, err := hid.NewMouse(controller, hid.MouseOptions{Width: 1280, Height: 720})
	if err != nil {
		t.Fatal(err)
	}
	server := api.New(hid.NewKeyboard(controller, layout), mouse, api.Options{Token: "a+;å% secret"})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	apiCtx, stopAPI := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- server.Serve(apiCtx, listener) }()
	t.Cleanup(func() {
		stopAPI()
		if err := <-served; err != nil {
			t.Error(err)
		}
		if err := controller.Close(); err != nil {
			t.Error(err)
		}
		server.Wait()
	})
	for _, secret := range []string{"", "wrong", "a+;å% secret"} {
		t.Run(map[string]string{"": "missing", "wrong": "wrong", "a+;å% secret": "correct"}[secret], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			args := []string{"-api", listener.Addr().String(), "-tesseract", filepath.Join(t.TempDir(), "absent-tesseract")}
			path := filepath.Join(t.TempDir(), "token")
			if secret != "" {
				if err := os.WriteFile(path, []byte(" \n"+secret+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "-token-file", path)
			}
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			var stdout, stderr bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- run(ctx, args, serverTransport, &stdout, &stderr) }()
			client := mcp.NewClient(&mcp.Implementation{Name: "token-test", Version: "test"}, nil)
			session, err := client.Connect(ctx, clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if secret != "" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "send_key", Arguments: map[string]any{"key": "a"}})
				if err != nil || result == nil || result.IsError != (secret != "a+;å% secret") {
					t.Fatalf("authenticated MCP result = %+v, %v", result, err)
				}
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-done:
				if code != 0 || stdout.Len() != 0 || strings.Contains(stderr.String(), "a+;å% secret") {
					t.Fatalf("MCP exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
				}
			case <-ctx.Done():
				t.Fatal("MCP did not finish")
			}
			frames, prepares, _ := port.snapshot()
			if secret != "a+;å% secret" && (prepares != 0 || len(frames) != 0) {
				t.Fatal("rejected MCP token touched HID")
			}
		})
	}
	frames, _, _ := port.snapshot()
	if len(frames) != 4 {
		t.Fatalf("correct token did not execute exactly two keys: %x", frames)
	}
}
