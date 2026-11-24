package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linkdata/autopaws/apiwire"
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
		{[]string{"-config", filepath.Join(t.TempDir(), "missing.yaml"), "--help"}, 0, "-config"},
		{[]string{"-unknown"}, 2, "flag provided but not defined"},
		{[]string{"-api"}, 2, "flag needs an argument"},
		{[]string{"extra"}, 2, "unexpected arguments: extra"},
		{[]string{"-api="}, 2, "-api:"},
		{[]string{"-api", "localhost"}, 2, "-api:"},
		{[]string{"-api", "localhost:"}, 2, "1..65535"},
		{[]string{"-api", "localhost:0"}, 2, "1..65535"},
		{[]string{"-api", "localhost:65536"}, 2, "1..65535"},
		{[]string{"-api", "localhost:http"}, 2, "1..65535"},
		{[]string{"-vision-api", "http://localhost:11434/v1"}, 2, "configured together"},
		{[]string{"-vision-model", "model"}, 2, "configured together"},
		{[]string{"-vision-api", "file:///tmp/vision", "-vision-model", "model"}, 2, "http(s) URL"},
		{[]string{"-vision-api", "http://localhost:11434/v1", "-vision-model", "model", "-vision-request-json", "{\"stream\":true}"}, 2, "cannot override stream"},
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

func TestCommandConfigAndOverrides(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "autopaws.yaml")
	missing := filepath.Join(directory, "missing")
	for _, vector := range []struct {
		name, contents string
		args           []string
		code           int
		text           string
	}{
		{"shared file", "api: localhost:9329\ntarget-layout: absent\nvision-api: http://localhost:11434/v1\nvision-model: model\nvision-max-width: 1280\nvision-request-json: '{\"reasoning_effort\":\"none\"}'", nil, 1, "transport failure"},
		{"invalid API", "api: localhost:0", nil, 2, "1..65535"},
		{"disabled API", "api: ''\nweb: localhost:9330", nil, 2, "-api:"},
		{"API override", "api: localhost:0", []string{"-api=localhost:9329"}, 1, "transport failure"},
		{"vision pair", "vision-model: model", nil, 2, "configured together"},
		{"vision width", "vision-api: http://localhost/v1\nvision-model: model\nvision-max-width: 0", nil, 2, "64..4096"},
		{"vision JSON", "vision-api: http://localhost/v1\nvision-model: model\nvision-request-json: '{\"stream\":true}'", nil, 2, "cannot override stream"},
		{"vision override", "vision-model: model", []string{"-vision-model="}, 1, "transport failure"},
		{"API token file", "token-file: " + missing, nil, 1, missing},
		{"inline token", "token: '  a+;å% secret  '", nil, 1, "transport failure"},
		{"invalid token", "token: '  '", nil, 1, "token"},
		{"conflicting tokens", "token: secret\ntoken-file: " + missing, nil, 1, "token"},
		{"vision token file", "vision-token-file: " + missing, nil, 1, "vision token:"},
		{"invalid file", "unknown: true", nil, 2, "unknown setting"},
	} {
		t.Run(vector.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(vector.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"-config", path, "-tesseract", missing}, vector.args...)
			var stdout, stderr bytes.Buffer
			code := run(t.Context(), args, failedMCPTransport{errors.New("transport failure")}, &stdout, &stderr)
			if code != vector.code || stdout.Len() != 0 || !strings.Contains(stderr.String(), vector.text) {
				t.Fatalf("config exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
			}
		})
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
			fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
				if request.Token != "a+;å% secret" {
					t.Error("configured API token was not sent")
				}
				return sdkReply{result: map[string]bool{"fixture": true}}
			})
			args := []string{"-api", fake.listener.Addr().String(), "-tesseract", missingTesseract, "-token", "a+;å% secret"}
			if signal {
				path := filepath.Join(t.TempDir(), "autopaws.yaml")
				contents := []byte("api: " + fake.listener.Addr().String() + "\ntoken: '  a+;å% secret  '\nvision-api: http://localhost:11434/v1\nvision-model: model\n")
				if err := os.WriteFile(path, contents, 0o600); err != nil {
					t.Fatal(err)
				}
				args = []string{"-config", path, "-tesseract", missingTesseract}
			}
			go func() {
				done <- run(ctx, args, serverTransport, &stdout, &stderr)
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
			if err != nil || len(tools.Tools) != 14 {
				t.Fatalf("command did not start the MCP tools: %v, %v", tools, err)
			}
			for _, tool := range tools.Tools {
				if tool.Name == "send_key" {
					var schema []byte
					if schema, err = json.Marshal(tool.InputSchema); err != nil || bytes.Contains(schema, []byte("wait_for")) != signal {
						t.Fatalf("configured vision schema is wrong: %s, %v", schema, err)
					}
				}
			}
			var result *mcp.CallToolResult
			if result, err = session.CallTool(callCtx, &mcp.CallToolParams{Name: "get_device_info"}); err != nil || result == nil || result.IsError {
				t.Fatalf("configured API call failed: %+v, %v", result, err)
			}
			if signal {
				cancel()
			} else if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-done:
				if code != 0 || stdout.Len() != 0 || strings.Contains(stderr.String(), "a+;å% secret") {
					t.Fatalf("SDK command exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
				}
			case <-callCtx.Done():
				t.Fatal("MCP command failed to stop")
			}
		})
	}
}
