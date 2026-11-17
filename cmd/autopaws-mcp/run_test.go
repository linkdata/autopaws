package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type failedMCPTransport struct{ failure error }

func (transport failedMCPTransport) Connect(context.Context) (mcp.Connection, error) {
	return nil, transport.failure
}

func TestCommandHelpAndArguments(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "not-created")
	for _, vector := range []struct {
		args []string
		code int
		text string
	}{
		{[]string{"-help"}, 0, "127.0.0.1:9329"},
		{[]string{"-h"}, 0, "-capture-log"},
		{[]string{"-capture-log", logPath, "--help"}, 0, "Usage:"},
		{[]string{"-unknown"}, 2, "flag provided but not defined"},
		{[]string{"-api"}, 2, "flag needs an argument"},
		{[]string{"extra"}, 2, "unexpected arguments"},
		{[]string{"-api="}, 2, "-api:"},
		{[]string{"-api", "localhost"}, 2, "-api:"},
		{[]string{"-api", "localhost:"}, 2, "1..65535"},
		{[]string{"-api", "localhost:0"}, 2, "1..65535"},
		{[]string{"-api", "localhost:65536"}, 2, "1..65535"},
		{[]string{"-api", "localhost:http"}, 2, "1..65535"},
	} {
		t.Run(strings.Join(vector.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), vector.args, nil, &stdout, &stderr)
			if code != vector.code {
				t.Fatalf("exit %d; want %d: %s %s", code, vector.code, stdout.String(), stderr.String())
			}
			output, other := &stderr, &stdout
			if vector.code == 0 {
				output, other = &stdout, &stderr
			}
			if !strings.Contains(output.String(), vector.text) || other.Len() != 0 {
				t.Fatalf("wrong command output: stdout %q stderr %q", stdout.String(), stderr.String())
			}
		})
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("help created a capture directory: %v", err)
	}
}

func TestCommandAddressValidationAndTransportFailure(t *testing.T) {
	failure := errors.New("transport failure")
	for _, address := range []string{"localhost:1", "[::1]:65535", ":9329"} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"-api", address}, failedMCPTransport{failure}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), failure.Error()) {
			t.Fatalf("runtime failure at %q: exit %d stdout %q stderr %q", address, code, stdout.String(), stderr.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := run(ctx, nil, nil, &stdout, &stderr); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("pre-canceled command touched stdio/transport: %d %q %q", code, stdout.String(), stderr.String())
	}
}

func TestCommandSDKRunAndShutdown(t *testing.T) {
	for _, signal := range []bool{false, true} {
		t.Run(map[bool]string{false: "client exit", true: "process context"}[signal], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			var stdout, stderr bytes.Buffer
			done := make(chan int, 1)
			missingTesseract := filepath.Join(t.TempDir(), "missing-tesseract")
			go func() {
				done <- run(ctx, []string{"-api", "127.0.0.1:1", "-tesseract", missingTesseract}, serverTransport, &stdout, &stderr)
			}()
			callCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			client := mcp.NewClient(&mcp.Implementation{Name: "command-test", Version: "test"}, nil)
			session, err := client.Connect(callCtx, clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			tools, err := session.ListTools(callCtx, nil)
			if err != nil || len(tools.Tools) != 12 {
				t.Fatalf("command did not start the MCP tools: %v, %v", tools, err)
			}
			if signal {
				cancel()
			} else if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-done:
				if code != 0 || stdout.Len() != 0 {
					t.Fatalf("SDK command exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
				}
			case <-callCtx.Done():
				t.Fatal("MCP command failed to stop")
			}
		})
	}
}
