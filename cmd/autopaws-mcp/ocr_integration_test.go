package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/apiwire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOCREnabledToolsGolden(t *testing.T) {
	server := newServer(t.Context(), mcpOptions{TesseractPath: "resolved-at-startup"}, unitCaller(func(context.Context, string, any, any) error {
		t.Error("tools/list called API")
		return nil
	}))
	session := sdkConnect(t, server)
	result, err := session.client.ListTools(sdkContext(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(result.Tools, func(a, b *mcp.Tool) int { return cmp.Compare(a.Name, b.Name) })
	if len(result.Tools) != 14 {
		t.Fatalf("OCR tool count=%d", len(result.Tools))
	}
	for _, tool := range result.Tools {
		readOnly := slices.Contains([]string{"capture_screen", "get_screen_text", "get_device_info", "list_capture_devices"}, tool.Name)
		if (tool.Annotations != nil && tool.Annotations.ReadOnlyHint) != readOnly {
			t.Errorf("%s annotations=%+v", tool.Name, tool.Annotations)
		}
	}
	encoded, err := json.MarshalIndent(result.Tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := "testdata/tools-ocr.golden.json"
	if *sdkUpdateGolden {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, want) {
		t.Fatalf("OCR tools/list changed; inspect then regenerate with -update-tools-golden\n%s", encoded)
	}
}

func TestOCRToolsUseFreshCaptureAndOneRawCommand(t *testing.T) {
	path := fakeTesseract(t, "success")
	capture := sdkCapture{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	var typedAt atomic.Pointer[time.Time]
	fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
		if request.Method == "type_text" {
			now := time.Now()
			typedAt.Store(&now)
		}
		if typed := typedAt.Load(); request.Method == "capture_frame" && typed != nil {
			if elapsed := time.Since(*typed); elapsed < 20*time.Millisecond {
				t.Errorf("capture preceded requested output wait: %s", elapsed)
			}
		}
		return sdkDefaultReply(request, capture)
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{TesseractPath: path}, apiclient.New(fake.listener.Addr().String())))
	result := sdkTool(t, session, "get_screen_text", map[string]any{}, false)
	if text := strings.TrimSpace(sdkText(result)); text != "result line\n\n\nsecond line" {
		t.Fatalf("screen OCR=%q", text)
	}
	result = sdkTool(t, session, "execute_and_read", map[string]any{"command": "printf '{literal}'", "wait_seconds": 0.02}, false)
	if strings.TrimSpace(sdkText(result)) != "result line\n\n\nsecond line" {
		t.Fatalf("command OCR=%s", sdkText(result))
	}
	requests := fake.snapshot()
	var methods []string
	for _, request := range requests {
		methods = append(methods, request.Method)
	}
	wantMethods := []string{"capture_frame", "get_device_info", "type_text", "capture_frame", "get_device_info"}
	if !slices.Equal(methods, wantMethods) {
		t.Fatalf("OCR API sequence=%v", methods)
	}
	sdkEqualParams(t, requests[2], map[string]any{"text": "printf '{literal}'\n", "raw": true})
	for _, part := range result.Content {
		if _, ok := part.(*mcp.ImageContent); ok {
			t.Fatal("OCR returned an image")
		}
	}
}

func TestOCRDoesNotEstablishOrReplaceScreenshotCoordinates(t *testing.T) {
	path := fakeTesseract(t, "success")
	images := []sdkCapture{
		{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7},
		{jpeg: sdkJPEG(t, 2, 2), width: 2, height: 2, targetWidth: 20, targetHigh: 30},
	}
	var current atomic.Int32
	fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
		return sdkDefaultReply(request, images[current.Load()])
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{TesseractPath: path}, apiclient.New(fake.listener.Addr().String())))
	for _, tool := range []string{"get_screen_text", "execute_and_read"} {
		args := map[string]any{}
		if tool == "execute_and_read" {
			args = map[string]any{"command": "hello", "wait_seconds": 0}
		}
		sdkTool(t, session, tool, args, false)
		sdkTool(t, session, "mouse_move", map[string]any{"x": 0, "y": 0}, true)
	}
	sdkTool(t, session, "capture_screen", map[string]any{}, false)
	current.Store(1)
	for _, tool := range []string{"get_screen_text", "execute_and_read"} {
		args := map[string]any{}
		if tool == "execute_and_read" {
			args = map[string]any{"command": "hello", "wait_seconds": 0}
		}
		sdkTool(t, session, tool, args, false)
		sdkTool(t, session, "mouse_move", map[string]any{"x": 4, "y": 2}, false)
	}
	var moves []apiwire.Request
	for _, request := range fake.snapshot() {
		if request.Method == "mouse_move" {
			moves = append(moves, request)
		}
	}
	if len(moves) != 2 {
		t.Fatalf("OCR established coordinates before a screenshot: %v", moves)
	}
	for _, request := range moves {
		sdkEqualParams(t, request, map[string]any{"x": 8, "y": 6})
	}
}

func TestOCRFailuresAreToolErrorsAndInvalidWaitSendsNoInput(t *testing.T) {
	path := fakeTesseract(t, "fail")
	capture := sdkCapture{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	var unavailable atomic.Bool
	fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
		if unavailable.Load() {
			return sdkReply{code: apiwire.ErrUnavailable, detail: "capture missing"}
		}
		return sdkDefaultReply(request, capture)
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{TesseractPath: path}, apiclient.New(fake.listener.Addr().String())))
	for _, value := range []float64{-0.01, 60.01} {
		sdkTool(t, session, "execute_and_read", map[string]any{"command": "do not type", "wait_seconds": value}, true)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatal("invalid wait sent API input")
	}
	result := sdkTool(t, session, "get_screen_text", map[string]any{}, true)
	if !strings.Contains(sdkText(result), "fake Tesseract failure") || strings.Contains(sdkText(result), "[OCR Error:") {
		t.Fatalf("OCR failure=%s", sdkText(result))
	}
	unavailable.Store(true)
	result = sdkTool(t, session, "get_screen_text", map[string]any{}, true)
	if !strings.Contains(sdkText(result), "capture missing") {
		t.Fatalf("capture failure=%s", sdkText(result))
	}
	sdkTool(t, session, "execute_and_read", map[string]any{"command": "hello", "wait_seconds": 0}, true)
}

func TestOCRRuntimeAvailabilityAndStderrWarning(t *testing.T) {
	for _, mode := range []string{"missing override", "automatic", "explicit executable"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"-api", "127.0.0.1:1"}
			available := false
			switch mode {
			case "missing override":
				args = append(args, "-tesseract", filepath.Join(t.TempDir(), "missing"))
			case "explicit executable":
				path, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				args = append(args, "-tesseract", path)
				available = true
			default:
				_, err := resolveTesseract("")
				available = err == nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			var stdout, stderr bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- run(ctx, args, serverTransport, &stdout, &stderr) }()
			client, err := mcp.NewClient(&mcp.Implementation{Name: "ocr-runtime", Version: "test"}, nil).Connect(sdkContext(t), clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			tools, err := client.ListTools(sdkContext(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			want := 12
			if available {
				want = 14
			}
			if len(tools.Tools) != want {
				t.Fatalf("available%t tools%d want%d", available, len(tools.Tools), want)
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			if code := <-done; code != 0 || stdout.Len() != 0 || strings.Contains(stderr.String(), "OCR tools disabled") == available {
				t.Fatalf("runtime OCR availability: exit%d stdout%q stderr%q", code, stdout.String(), stderr.String())
			}
		})
	}
}
