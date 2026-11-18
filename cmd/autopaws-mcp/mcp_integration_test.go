package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/apiwire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var sdkUpdateGolden = flag.Bool("update-tools-golden", false, "update the MCP tools/list golden file")

type sdkReply struct {
	result any
	code   apiwire.Code
	detail string
}

type sdkFakeAPI struct {
	listener net.Listener
	mu       sync.Mutex
	requests []apiwire.Request
	conns    map[net.Conn]struct{}
}

// The MCP handlers use the actual portable client against this one-request TCP
// fixture. The fake never calls a private tool handler or owns target hardware.
func sdkNewAPI(t *testing.T, respond func(net.Conn, apiwire.Request) sdkReply) *sdkFakeAPI {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &sdkFakeAPI{listener: listener, conns: make(map[net.Conn]struct{})}
	accepted := make(chan struct{})
	var workers sync.WaitGroup
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					t.Errorf("fake API accept: %v", err)
				}
				return
			}
			fake.mu.Lock()
			fake.conns[conn] = struct{}{}
			fake.mu.Unlock()
			workers.Go(func() {
				defer func() {
					_ = conn.Close()
					fake.mu.Lock()
					delete(fake.conns, conn)
					fake.mu.Unlock()
				}()
				if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Errorf("fake API deadline: %v", err)
					return
				}
				line, err := apiwire.ReadLine(bufio.NewReader(conn), apiwire.MaxRequestLine)
				if err != nil {
					t.Errorf("fake API request: %v", err)
					return
				}
				var request apiwire.Request
				if err := json.Unmarshal(line, &request); err != nil {
					t.Errorf("fake API JSON: %v", err)
					return
				}
				fake.mu.Lock()
				fake.requests = append(fake.requests, request)
				fake.mu.Unlock()
				reply := respond(conn, request)
				response := apiwire.Response{ID: request.ID, OK: reply.code == "", Error: reply.code, Detail: reply.detail}
				if reply.code == "" {
					response.Result, err = json.Marshal(reply.result)
					if err != nil {
						t.Errorf("fake API result: %v", err)
						return
					}
				}
				// A cancelled client has already closed this connection.
				_ = json.NewEncoder(conn).Encode(response)
			})
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-accepted // No more worker additions before Wait.
		fake.mu.Lock()
		for conn := range fake.conns {
			_ = conn.Close()
		}
		fake.mu.Unlock()
		workers.Wait()
	})
	return fake
}

func (fake *sdkFakeAPI) snapshot() []apiwire.Request {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return slices.Clone(fake.requests)
}

type sdkCapture struct {
	jpeg                    []byte
	width, height           int
	targetWidth, targetHigh int
}

func sdkJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	frame := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			frame.SetRGBA(x, y, color.RGBA{R: uint8((x * 71) % 256), G: uint8((y * 113) % 256), B: 97, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, frame, &jpeg.Options{Quality: 73}); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func sdkDefaultReply(request apiwire.Request, capture sdkCapture) sdkReply {
	switch request.Method {
	case "capture_frame":
		return sdkReply{result: map[string]any{"jpeg_b64": capture.jpeg, "width": capture.width, "height": capture.height}}
	case "get_device_info":
		return sdkReply{result: map[string]any{"config": map[string]int{
			"screen_width": capture.targetWidth, "screen_height": capture.targetHigh,
		}}}
	case "list_capture_devices":
		return sdkReply{result: map[string]any{"devices": []map[string]string{{"device": "/dev/video-test", "name": "fake HDMI capture"}}}}
	case "list_keyboard_layouts":
		return sdkReply{result: map[string]any{"keyboard_layout": "us104", "layouts": []map[string]string{{"name": "us104", "source": "builtin"}, {"name": "custom-target", "source": "custom"}}}}
	case "type_text":
		return sdkReply{result: map[string]int{"chars_typed": 2}}
	case "send_key_sequence":
		return sdkReply{result: map[string]int{"steps_sent": 2}}
	default:
		return sdkReply{result: map[string]bool{"done": true}}
	}
}

type sdkObservedTransport struct {
	mcp.Transport
	connection mcp.Connection
}

func (transport *sdkObservedTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := transport.Transport.Connect(ctx)
	transport.connection = conn
	return conn, err
}

type sdkSession struct {
	client *mcp.ClientSession
	abort  func()
}

func sdkConnect(t *testing.T, server *mcp.Server) sdkSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	observed := &sdkObservedTransport{Transport: clientTransport}
	client, err := mcp.NewClient(&mcp.Implementation{Name: "autopaws-test", Version: "test"}, nil).Connect(
		sdkContext(t), observed, nil,
	)
	if err != nil {
		_ = serverSession.Close()
		t.Fatal(err)
	}
	abort := func() { _ = observed.connection.Close() }
	t.Cleanup(func() {
		abort()
		_ = client.Close()
		_ = serverSession.Close()
	})
	return sdkSession{client: client, abort: abort}
}

func sdkContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func sdkTool(t *testing.T, session sdkSession, name string, arguments any, wantError bool) *mcp.CallToolResult {
	t.Helper()
	result, err := session.client.CallTool(sdkContext(t), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s protocol error: %v", name, err)
	}
	if result.IsError != wantError || len(result.Content) == 0 {
		t.Fatalf("%s result = %+v, want isError %t and content", name, result, wantError)
	}
	return result
}

func sdkText(result *mcp.CallToolResult) string {
	var text strings.Builder
	for _, content := range result.Content {
		if part, ok := content.(*mcp.TextContent); ok {
			text.WriteString(part.Text)
			text.WriteByte('\n')
		}
	}
	return text.String()
}

func sdkCheckCapture(t *testing.T, result *mcp.CallToolResult, capture sdkCapture) {
	t.Helper()
	var images int
	for _, content := range result.Content {
		if part, ok := content.(*mcp.ImageContent); ok {
			images++
			if part.MIMEType != "image/jpeg" || !bytes.Equal(part.Data, capture.jpeg) {
				t.Fatalf("capture image changed: MIME %q, %d bytes", part.MIMEType, len(part.Data))
			}
			decoded, err := jpeg.DecodeConfig(bytes.NewReader(part.Data))
			if err != nil || decoded.Width != capture.width || decoded.Height != capture.height {
				t.Fatalf("returned JPEG dimensions = %+v, error %v", decoded, err)
			}
		}
	}
	if images != 1 || !strings.Contains(sdkText(result), fmt.Sprintf("%dx%d", capture.width, capture.height)) {
		t.Fatalf("capture must contain one unchanged image and its dimensions: %+v", result.Content)
	}
}

func sdkParams(t *testing.T, request apiwire.Request) map[string]any {
	t.Helper()
	var params map[string]any
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	return params
}

func sdkEqualParams(t *testing.T, request apiwire.Request, want any) {
	t.Helper()
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatal(err)
	}
	if got := sdkParams(t, request); !reflect.DeepEqual(got, normalized) {
		t.Fatalf("%s params = %s, want %s", request.Method, request.Params, encoded)
	}
}

func sdkInputRequests(requests []apiwire.Request) []apiwire.Request {
	var inputs []apiwire.Request
	for _, request := range requests {
		switch request.Method {
		case "capture_frame", "get_device_info", "list_capture_devices", "list_keyboard_layouts":
		default:
			inputs = append(inputs, request)
		}
	}
	return inputs
}

func TestSDKToolsGolden(t *testing.T) {
	fake := sdkNewAPI(t, func(_ net.Conn, _ apiwire.Request) sdkReply {
		t.Error("tools/list unexpectedly contacted API")
		return sdkReply{code: apiwire.ErrUnavailable}
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{Version: "test"}, apiclient.New(fake.listener.Addr().String())))
	result, err := session.client.ListTools(sdkContext(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(result.Tools, func(a, b *mcp.Tool) int { return cmp.Compare(a.Name, b.Name) })
	wantNames := []string{"capture_screen", "get_device_info", "list_capture_devices", "list_keyboard_layouts", "mouse_click", "mouse_drag", "mouse_move", "mouse_scroll", "send_key", "send_key_sequence", "set_capture_device", "set_capture_resolution", "set_keyboard_layout", "type_text"}
	var names []string
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
		readOnly := slices.Contains([]string{"capture_screen", "get_device_info", "list_capture_devices", "list_keyboard_layouts"}, tool.Name)
		if (tool.Annotations != nil && tool.Annotations.ReadOnlyHint) != readOnly {
			t.Errorf("%s readOnlyHint is wrong: %+v", tool.Name, tool.Annotations)
		}
		if annotation := tool.Annotations; annotation != nil && (annotation.DestructiveHint != nil || annotation.OpenWorldHint != nil || annotation.IdempotentHint || annotation.Title != "") {
			t.Errorf("%s overrides default annotations: %+v", tool.Name, annotation)
		}
	}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("tool names = %v, want %v", names, wantNames)
	}
	encoded, err := json.MarshalIndent(result.Tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("testdata", "tools.golden.json")
	if *sdkUpdateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, want) {
		t.Fatalf("tools/list metadata changed; inspect then regenerate with -update-tools-golden\ngot:\n%s", encoded)
	}
}

func TestSDKAllToolsCallTheAPI(t *testing.T) {
	capture := sdkCapture{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply { return sdkDefaultReply(request, capture) })
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{}, apiclient.New(fake.listener.Addr().String())))
	sdkCheckCapture(t, sdkTool(t, session, "capture_screen", map[string]any{}, false), capture)
	requests := fake.snapshot()
	if len(requests) != 2 || !slices.ContainsFunc(requests, func(request apiwire.Request) bool { return request.Method == "capture_frame" }) || !slices.ContainsFunc(requests, func(request apiwire.Request) bool { return request.Method == "get_device_info" }) {
		t.Fatalf("capture reads = %+v, want frame and target config", requests)
	}
	for _, tc := range []struct {
		name string
		args any
		want any
	}{
		{"type_text", map[string]any{"text": "ab", "raw": true, "char_delay_ms": 0}, map[string]any{"text": "ab", "raw": true, "char_delay_ms": 0}},
		{"send_key", map[string]any{"key": "enter", "modifiers": []string{"ctrl"}}, map[string]any{"key": "enter", "modifiers": []string{"ctrl"}}},
		{"send_key_sequence", map[string]any{"steps": []any{map[string]any{"key": "a", "modifiers": []string{"shift"}, "delay_ms": 0}, map[string]any{"key": "enter", "modifiers": []string{}, "delay_ms": 1}}, "default_delay_ms": 0}, map[string]any{"steps": []any{map[string]any{"key": "a", "modifiers": []string{"shift"}, "delay_ms": 0}, map[string]any{"key": "enter", "delay_ms": 1}}, "default_delay_ms": 0}},
		{"mouse_move", map[string]any{"x": 2, "y": 1, "relative": false}, map[string]any{"x": 4, "y": 3}},
		{"mouse_click", map[string]any{"button": "right", "x": 4, "y": 2, "button_mode": "relative"}, map[string]any{"button": "right", "x": 8, "y": 6, "button_mode": "relative"}},
		{"mouse_drag", map[string]any{"start_x": 0, "start_y": 2, "end_x": 4, "end_y": 0, "button": "middle", "button_mode": "absolute"}, map[string]any{"start_x": 0, "start_y": 6, "end_x": 8, "end_y": 0, "button": "middle", "button_mode": "absolute"}},
		{"mouse_scroll", map[string]any{"amount": -9}, map[string]any{"amount": -9}},
		{"get_device_info", map[string]any{}, map[string]any{}},
		{"list_capture_devices", map[string]any{}, map[string]any{}},
		{"list_keyboard_layouts", map[string]any{}, map[string]any{}},
		{"set_keyboard_layout", map[string]any{"layout": "custom-target"}, map[string]any{"layout": "custom-target"}},
		{"set_capture_device", map[string]any{"device": "/dev/video-test"}, map[string]any{"device": "/dev/video-test"}},
		{"set_capture_resolution", map[string]any{"width": 640, "height": 480}, map[string]any{"width": 640, "height": 480}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(fake.snapshot())
			result := sdkTool(t, session, tc.name, tc.args, false)
			if sdkText(result) == "" {
				t.Fatal("tool returned no text")
			}
			if (tc.name == "get_device_info" || tc.name == "list_capture_devices" || tc.name == "list_keyboard_layouts") && !json.Valid([]byte(sdkText(result))) {
				t.Fatalf("%s did not return JSON text: %s", tc.name, sdkText(result))
			}
			after := fake.snapshot()
			if len(after) != before+1 || after[before].Method != tc.name {
				t.Fatalf("%s made calls %+v, want exactly one matching API method", tc.name, after[before:])
			}
			sdkEqualParams(t, after[before], tc.want)
		})
	}
}

func TestSDKAbsoluteActionsRequireSnapshot(t *testing.T) {
	fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
		return sdkDefaultReply(request, sdkCapture{targetWidth: 1920, targetHigh: 1080})
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{}, apiclient.New(fake.listener.Addr().String())))
	// Reading target dimensions alone must not establish screenshot coordinates.
	sdkTool(t, session, "get_device_info", map[string]any{}, false)
	for _, tc := range []struct {
		name string
		args any
	}{
		{"mouse_move", map[string]any{"x": 0, "y": 0}},
		{"mouse_click", map[string]any{"x": 0, "y": 0}},
		{"mouse_drag", map[string]any{"start_x": 0, "start_y": 0, "end_x": 1, "end_y": 1}},
	} {
		result := sdkTool(t, session, tc.name, tc.args, true)
		if !strings.Contains(sdkText(result), "capture_screen") {
			t.Fatalf("missing initial screenshot instruction: %s", sdkText(result))
		}
	}
	if requests := sdkInputRequests(fake.snapshot()); len(requests) != 0 {
		t.Fatalf("absolute actions sent input before a screenshot: %+v", requests)
	}
	before := len(fake.snapshot())
	sdkTool(t, session, "mouse_move", map[string]any{"x": -127, "y": 81, "relative": true}, false)
	sdkTool(t, session, "mouse_click", map[string]any{"button": "left", "button_mode": "relative"}, false)
	requests := fake.snapshot()[before:]
	if len(requests) != 2 || requests[0].Method != "mouse_move" || requests[1].Method != "mouse_click" {
		t.Fatalf("snapshot-free input calls = %+v", requests)
	}
	sdkEqualParams(t, requests[0], map[string]any{"x": -127, "y": 81, "relative": true})
	params := sdkParams(t, requests[1])
	if params["x"] != nil || params["y"] != nil || params["button_mode"] != "relative" {
		t.Fatalf("unpositioned click params = %v", params)
	}
}

func TestSDKCoordinateCornersAndOnePixelAxes(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		width, height, tw, th int
		x, y, wantX, wantY    int
	}{
		{"top left", 5, 3, 9, 7, 0, 0, 0, 0},
		{"top right", 5, 3, 9, 7, 4, 0, 8, 0},
		{"bottom left", 5, 3, 9, 7, 0, 2, 0, 6},
		{"bottom right", 5, 3, 9, 7, 4, 2, 8, 6},
		{"scaled cropped center", 5, 3, 9, 7, 2, 1, 4, 3},
		{"round nearest", 4, 6, 6, 9, 1, 1, 2, 2},
		{"one image axis", 1, 3, 9, 5, 0, 2, 0, 4},
		{"one target axis", 3, 5, 1, 9, 2, 2, 0, 4},
		{"one image height", 3, 1, 5, 9, 2, 0, 4, 0},
		{"one target height", 5, 3, 9, 1, 2, 2, 4, 0},
		{"both one pixel", 1, 1, 1, 1, 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := sdkCapture{jpeg: sdkJPEG(t, tc.width, tc.height), width: tc.width, height: tc.height, targetWidth: tc.tw, targetHigh: tc.th}
			fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply { return sdkDefaultReply(request, capture) })
			session := sdkConnect(t, newServer(t.Context(), mcpOptions{}, apiclient.New(fake.listener.Addr().String())))
			sdkCheckCapture(t, sdkTool(t, session, "capture_screen", map[string]any{}, false), capture)
			sdkTool(t, session, "mouse_move", map[string]any{"x": tc.x, "y": tc.y}, false)
			requests := sdkInputRequests(fake.snapshot())
			if len(requests) != 1 || requests[0].Method != "mouse_move" {
				t.Fatalf("absolute move calls = %+v", requests)
			}
			params := sdkParams(t, requests[0])
			if params["x"] != float64(tc.wantX) || params["y"] != float64(tc.wantY) {
				t.Fatalf("coordinates = %v, want (%d,%d)", params, tc.wantX, tc.wantY)
			}
		})
	}
}

func TestSDKInvalidSnapshotCoordinatesSendNoInput(t *testing.T) {
	capture := sdkCapture{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply { return sdkDefaultReply(request, capture) })
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{}, apiclient.New(fake.listener.Addr().String())))
	sdkTool(t, session, "capture_screen", map[string]any{}, false)
	for _, tc := range []struct {
		name string
		args any
	}{
		{"mouse_move", map[string]any{"x": -1, "y": 0}},
		{"mouse_move", map[string]any{"x": 5, "y": 0}},
		{"mouse_move", map[string]any{"x": 0, "y": 3}},
		{"mouse_click", map[string]any{"x": 0, "y": -1}},
		{"mouse_click", map[string]any{"x": 0}},
		{"mouse_drag", map[string]any{"start_x": -1, "start_y": 0, "end_x": 4, "end_y": 2}},
		{"mouse_drag", map[string]any{"start_x": 0, "start_y": 0, "end_x": 4, "end_y": 3}},
	} {
		sdkTool(t, session, tc.name, tc.args, true)
	}
	if requests := sdkInputRequests(fake.snapshot()); len(requests) != 0 {
		t.Fatalf("invalid coordinates emitted input: %+v", requests)
	}
}

func TestSDKLatestSnapshotAndSessionIsolation(t *testing.T) {
	captures := []sdkCapture{
		{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7},
		{jpeg: sdkJPEG(t, 3, 2), width: 3, height: 2, targetWidth: 100, targetHigh: 200},
	}
	var current atomic.Int32
	fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
		return sdkDefaultReply(request, captures[current.Load()])
	})
	server := newServer(t.Context(), mcpOptions{}, apiclient.New(fake.listener.Addr().String()))
	first, second := sdkConnect(t, server), sdkConnect(t, server)
	sdkTool(t, first, "capture_screen", map[string]any{}, false)
	sdkTool(t, second, "mouse_move", map[string]any{"x": 0, "y": 0}, true)
	current.Store(1)
	sdkCheckCapture(t, sdkTool(t, second, "capture_screen", map[string]any{}, false), captures[1])
	sdkTool(t, first, "mouse_move", map[string]any{"x": 4, "y": 2}, false)
	sdkTool(t, second, "mouse_move", map[string]any{"x": 2, "y": 1}, false)
	// A new returned screenshot replaces both source and target dimensions in
	// this session; coordinates from its older, larger image are now invalid.
	sdkTool(t, first, "capture_screen", map[string]any{}, false)
	sdkTool(t, first, "mouse_move", map[string]any{"x": 4, "y": 2}, true)
	sdkTool(t, first, "mouse_move", map[string]any{"x": 2, "y": 1}, false)
	inputs := sdkInputRequests(fake.snapshot())
	if len(inputs) != 3 {
		t.Fatalf("session input calls = %+v", inputs)
	}
	for i, want := range [][2]int{{8, 6}, {99, 199}, {99, 199}} {
		params := sdkParams(t, inputs[i])
		if params["x"] != float64(want[0]) || params["y"] != float64(want[1]) {
			t.Fatalf("session input %d = %v, want %v", i, params, want)
		}
	}
}

func TestSDKCaptureChangeInvalidatesSnapshotEvenOnFailure(t *testing.T) {
	for _, method := range []string{"set_capture_device", "set_capture_resolution"} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failed=%t", method, failed), func(t *testing.T) {
				capture := sdkCapture{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
				fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
					if request.Method == method && failed {
						return sdkReply{code: apiwire.ErrUnavailable, detail: "reopen failed"}
					}
					return sdkDefaultReply(request, capture)
				})
				session := sdkConnect(t, newServer(t.Context(), mcpOptions{}, apiclient.New(fake.listener.Addr().String())))
				sdkTool(t, session, "capture_screen", map[string]any{}, false)
				var args any = map[string]any{"device": "/dev/video-missing"}
				if method == "set_capture_resolution" {
					args = map[string]any{"width": 640, "height": 480}
				}
				sdkTool(t, session, method, args, failed)
				before := len(fake.snapshot())
				sdkTool(t, session, "mouse_move", map[string]any{"x": 0, "y": 0}, true)
				if len(fake.snapshot()) != before {
					t.Fatal("input sent after capture-change invalidation")
				}
				sdkTool(t, session, "capture_screen", map[string]any{}, false)
				sdkTool(t, session, "mouse_move", map[string]any{"x": 4, "y": 2}, false)
			})
		}
	}
}

func TestSDKKeyboardLayoutChangePreservesSnapshot(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%t", failed), func(t *testing.T) {
			capture := sdkCapture{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
			fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
				if request.Method == "set_keyboard_layout" {
					if failed {
						return sdkReply{code: apiwire.ErrInvalidParams, detail: "unknown keyboard layout"}
					}
					return sdkReply{result: map[string]string{"keyboard_layout": "custom-target"}}
				}
				return sdkDefaultReply(request, capture)
			})
			session := sdkConnect(t, newServer(t.Context(), mcpOptions{}, apiclient.New(fake.listener.Addr().String())))
			sdkTool(t, session, "capture_screen", map[string]any{}, false)
			before := len(fake.snapshot())
			result := sdkTool(t, session, "set_keyboard_layout", map[string]any{"layout": "custom-target"}, failed)
			if !failed && strings.TrimSpace(sdkText(result)) != `{"keyboard_layout":"custom-target"}` {
				t.Fatalf("layout result changed: %s", sdkText(result))
			}
			sdkTool(t, session, "list_keyboard_layouts", map[string]any{}, false)
			sdkTool(t, session, "mouse_move", map[string]any{"x": 4, "y": 2}, false)
			requests := fake.snapshot()[before:]
			if len(requests) != 3 || requests[0].Method != "set_keyboard_layout" || requests[1].Method != "list_keyboard_layouts" || requests[2].Method != "mouse_move" {
				t.Fatalf("layout switch and cached-coordinate move calls = %+v", requests)
			}
			sdkEqualParams(t, requests[0], map[string]any{"layout": "custom-target"})
			sdkEqualParams(t, requests[1], map[string]any{})
			sdkEqualParams(t, requests[2], map[string]any{"x": 8, "y": 6})
		})
	}
}

func TestSDKKeyboardLayoutToolSchemasRejectInvalidArguments(t *testing.T) {
	fake := sdkNewAPI(t, func(_ net.Conn, _ apiwire.Request) sdkReply {
		t.Error("invalid tool arguments reached API")
		return sdkReply{code: apiwire.ErrUnavailable}
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{}, apiclient.New(fake.listener.Addr().String())))
	for _, tc := range []struct {
		name string
		args any
	}{
		{"set_keyboard_layout", map[string]any{}},
		{"set_keyboard_layout", map[string]any{"layout": 123}},
		{"set_keyboard_layout", map[string]any{"layout": "se105", "path": "/tmp/layout.yaml"}},
		{"list_keyboard_layouts", map[string]any{"layout": "se105"}},
	} {
		result, err := session.client.CallTool(sdkContext(t), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err == nil && (result == nil || !result.IsError) {
			t.Fatalf("%s accepted invalid arguments %v: %+v", tc.name, tc.args, result)
		}
	}
	if requests := fake.snapshot(); len(requests) != 0 {
		t.Fatalf("schema failures contacted API: %+v", requests)
	}
}

func TestSDKAPIFailuresAreToolErrors(t *testing.T) {
	fake := sdkNewAPI(t, func(_ net.Conn, _ apiwire.Request) sdkReply {
		return sdkReply{code: apiwire.ErrUnavailable, detail: "no capture device"}
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{}, apiclient.New(fake.listener.Addr().String())))
	for _, tc := range []struct {
		name string
		args any
	}{
		{"capture_screen", map[string]any{}},
		{"list_capture_devices", map[string]any{}},
		{"type_text", map[string]any{"text": "a"}},
	} {
		result := sdkTool(t, session, tc.name, tc.args, true)
		if !strings.Contains(sdkText(result), "unavailable") || !strings.Contains(sdkText(result), "no capture device") {
			t.Fatalf("API error text lost: %s", sdkText(result))
		}
	}
}

func TestSDKCancellationClosesTheAPICall(t *testing.T) {
	for _, tc := range []struct{ method, cancel string }{
		{"type_text", "call"}, {"mouse_drag", "call"}, {"type_text", "disconnect"}, {"type_text", "lifetime"},
	} {
		t.Run(tc.method+"/"+tc.cancel, func(t *testing.T) {
			started, disconnected := make(chan struct{}), make(chan struct{})
			capture := sdkCapture{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
			fake := sdkNewAPI(t, func(conn net.Conn, request apiwire.Request) sdkReply {
				if request.Method == tc.method {
					close(started)
					var one [1]byte
					if _, err := conn.Read(one[:]); !errors.Is(err, io.EOF) {
						t.Errorf("cancelled API connection: %v, want EOF", err)
					}
					close(disconnected)
					return sdkReply{code: apiwire.ErrCanceled}
				}
				return sdkDefaultReply(request, capture)
			})
			lifetime, stopLifetime := context.WithCancel(t.Context())
			defer stopLifetime()
			session := sdkConnect(t, newServer(lifetime, mcpOptions{}, apiclient.New(fake.listener.Addr().String())))
			var args any = map[string]any{"text": "long action"}
			if tc.method == "mouse_drag" {
				sdkTool(t, session, "capture_screen", map[string]any{}, false)
				args = map[string]any{"start_x": 0, "start_y": 0, "end_x": 4, "end_y": 2, "button_mode": "relative"}
			}
			ctx, cancel := context.WithCancel(sdkContext(t))
			defer cancel()
			type outcome struct {
				result *mcp.CallToolResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := session.client.CallTool(ctx, &mcp.CallToolParams{Name: tc.method, Arguments: args})
				done <- outcome{result: result, err: err}
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("tool did not start an API request")
			}
			switch tc.cancel {
			case "call":
				cancel()
			case "disconnect":
				session.abort()
			case "lifetime":
				stopLifetime()
			}
			select {
			case <-disconnected:
			case <-time.After(5 * time.Second):
				t.Fatal("SDK cancellation did not close the API connection")
			}
			select {
			case completed := <-done:
				switch tc.cancel {
				case "call":
					if !errors.Is(completed.err, context.Canceled) {
						t.Fatalf("CallTool cancellation = %v", completed.err)
					}
				case "disconnect":
					if completed.err == nil {
						t.Fatal("disconnected SDK call returned no connection error")
					}
				case "lifetime":
					if completed.err != nil || completed.result == nil || !completed.result.IsError {
						t.Fatalf("lifetime cancellation result = %+v, error %v", completed.result, completed.err)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled SDK tool did not complete")
			}
			inputs := sdkInputRequests(fake.snapshot())
			if len(inputs) != 1 || inputs[0].Method != tc.method {
				t.Fatalf("cancelled tool sent more than one input request: %+v", inputs)
			}
		})
	}
}

func TestSDKCaptureLogs(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			data := t.TempDir()
			t.Setenv("XDG_DATA_HOME", data)
			t.Setenv("LOCALAPPDATA", data)
			capture := sdkCapture{jpeg: sdkJPEG(t, 12, 7), width: 12, height: 7, targetWidth: 1920, targetHigh: 1080}
			fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply { return sdkDefaultReply(request, capture) })
			options := mcpOptions{}
			if enabled {
				options.CaptureLogDir = filepath.Join(data, "captures")
			}
			session := sdkConnect(t, newServer(t.Context(), options, apiclient.New(fake.listener.Addr().String())))
			for range 2 {
				sdkCheckCapture(t, sdkTool(t, session, "capture_screen", map[string]any{}, false), capture)
			}
			if !enabled {
				entries, err := os.ReadDir(data)
				if err != nil || len(entries) != 0 {
					t.Fatalf("disabled capture logging created files: %v, error %v", entries, err)
				}
				return
			}
			entries, err := os.ReadDir(options.CaptureLogDir)
			if err != nil || len(entries) != 2 || entries[0].Name() == entries[1].Name() {
				t.Fatalf("captures overwrote one another: %v, error %v", entries, err)
			}
			directory, err := os.Stat(options.CaptureLogDir)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS == "linux" && directory.Mode().Perm() != 0o700 {
				t.Fatalf("capture directory mode = %o, want 0700", directory.Mode().Perm())
			}
			for _, entry := range entries {
				if !strings.Contains(entry.Name(), "capture_screen") || !strings.HasSuffix(entry.Name(), ".jpg") {
					t.Fatalf("capture filename = %q", entry.Name())
				}
				path := filepath.Join(options.CaptureLogDir, entry.Name())
				contents, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(contents, capture.jpeg) {
					t.Fatalf("logged JPEG changed: %q, error %v", path, err)
				}
				info, err := entry.Info()
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS == "linux" && info.Mode().Perm() != 0o600 {
					t.Fatalf("capture file mode = %o, want 0600", info.Mode().Perm())
				}
			}
		})
	}
}
