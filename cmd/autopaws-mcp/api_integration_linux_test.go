//go:build linux && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linkdata/autopaws/api"
	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/hid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type realMCPRecordingTransport struct {
	mu       sync.Mutex
	frames   [][]byte
	prepares int
	closes   int
	hook     func(context.Context, []byte) error
}

func (transport *realMCPRecordingTransport) Prepare(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.prepares++
	return 1, nil
}

func (transport *realMCPRecordingTransport) WriteFrame(ctx context.Context, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	transport.mu.Lock()
	transport.frames = append(transport.frames, bytes.Clone(frame))
	hook := transport.hook
	transport.mu.Unlock()
	if hook != nil {
		return hook(ctx, frame)
	}
	return nil
}

func (transport *realMCPRecordingTransport) Close() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.closes++
	return nil
}

func (transport *realMCPRecordingTransport) setHook(hook func(context.Context, []byte) error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.hook = hook
}

func (transport *realMCPRecordingTransport) snapshot() ([][]byte, int, int) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	frames := make([][]byte, len(transport.frames))
	for i, frame := range transport.frames {
		frames[i] = bytes.Clone(frame)
	}
	return frames, transport.prepares, transport.closes
}

type realMCPAPICall struct {
	method string
	params json.RawMessage
}

// Only capture_frame is substituted until the daemon's frame pipeline lands.
// Device information and every input call use the real TCP client unchanged.
type realMCPCaptureBridge struct {
	client *apiclient.Client
	jpeg   []byte
	mu     sync.Mutex
	inputs []realMCPAPICall
}

func (bridge *realMCPCaptureBridge) Call(ctx context.Context, method string, params, result any) error {
	if method == "capture_frame" {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := json.Marshal(struct {
			JPEG   []byte `json:"jpeg_b64"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		}{bridge.jpeg, 4, 3})
		if err != nil {
			return err
		}
		return json.Unmarshal(data, result)
	}
	if method != "get_device_info" {
		data, err := json.Marshal(params)
		if err != nil {
			return err
		}
		bridge.mu.Lock()
		bridge.inputs = append(bridge.inputs, realMCPAPICall{method, data})
		bridge.mu.Unlock()
	}
	return bridge.client.Call(ctx, method, params, result)
}

func (bridge *realMCPCaptureBridge) inputCalls() []realMCPAPICall {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return append([]realMCPAPICall(nil), bridge.inputs...)
}

// Capturing the SDK's underlying connection lets EOF tests break the carrier
// directly, without ClientSession.Close's graceful request-drain behavior.
type realMCPConnectionCapture struct {
	mcp.Transport
	connection mcp.Connection
}

func (transport *realMCPConnectionCapture) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := transport.Transport.Connect(ctx)
	transport.connection = connection
	return connection, err
}

type realMCPFixture struct {
	client     *mcp.ClientSession
	keyboard   *hid.Keyboard
	transport  *realMCPRecordingTransport
	bridge     *realMCPCaptureBridge
	disconnect func() error
	cancelLife context.CancelFunc
}

func newRealMCPFixture(t *testing.T) realMCPFixture {
	t.Helper()
	transport := &realMCPRecordingTransport{}
	controller := hid.NewController(transport)
	layout, err := hid.LoadLayout("us104", "")
	if err != nil {
		t.Fatal(err)
	}
	keyboard := hid.NewKeyboard(controller, layout)
	mouse, err := hid.NewMouse(controller, hid.MouseOptions{Width: 1280, Height: 720, ButtonMode: "relative"})
	if err != nil {
		t.Fatal(err)
	}
	apiServer := api.New(keyboard, mouse, api.Options{
		SerialDevice: "/dev/recording", SerialBaud: 9600,
		ScreenWidth: 1280, ScreenHeight: 720, KeyboardLayout: "us104", ButtonMode: "relative",
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	apiCtx, cancelAPI := context.WithCancel(context.Background())
	apiDone := make(chan error, 1)
	go func() { apiDone <- apiServer.Serve(apiCtx, listener) }()
	lifetime, cancelLife := context.WithCancel(context.Background())
	var clientSession *mcp.ClientSession
	var serverSession *mcp.ServerSession
	var captured *realMCPConnectionCapture
	t.Cleanup(func() {
		cancelLife()
		if captured != nil && captured.connection != nil {
			_ = captured.connection.Close()
		}
		cancelAPI()
		if err := realMCPReceive(t, apiDone); err != nil {
			t.Errorf("API shutdown: %v", err)
		}
		if err := controller.Close(); err != nil {
			t.Errorf("controller close: %v", err)
		}
		drained := make(chan struct{})
		go func() {
			apiServer.Wait()
			if clientSession != nil {
				_ = clientSession.Close()
			}
			if serverSession != nil {
				_ = serverSession.Close()
				_ = serverSession.Wait()
			}
			close(drained)
		}()
		realMCPReceive(t, drained)
	})
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 4, 3)), nil); err != nil {
		t.Fatal(err)
	}
	bridge := &realMCPCaptureBridge{client: apiclient.New(listener.Addr().String()), jpeg: encoded.Bytes()}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := newServer(lifetime, mcpOptions{APIAddress: listener.Addr().String(), Version: "test", Logger: logger}, bridge)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	captured = &realMCPConnectionCapture{Transport: clientTransport}
	serverSession, err = server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	clientSession, err = mcp.NewClient(&mcp.Implementation{Name: "real-api-test", Version: "test"}, &mcp.ClientOptions{Logger: logger}).Connect(context.Background(), captured, nil)
	if err != nil {
		t.Fatal(err)
	}
	return realMCPFixture{clientSession, keyboard, transport, bridge, captured.connection.Close, cancelLife}
}

// Timeouts here bound broken tests, not production driver calls or HID actions.
func realMCPReceive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for real API/MCP integration")
		var zero T
		return zero
	}
}

type realMCPToolReply struct {
	result *mcp.CallToolResult
	err    error
}

func realMCPCall(fixture realMCPFixture, name string, arguments any) realMCPToolReply {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := fixture.client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	return realMCPToolReply{result, err}
}

func realMCPSuccess(t *testing.T, reply realMCPToolReply) {
	t.Helper()
	if reply.err != nil {
		t.Fatal(reply.err)
	}
	if reply.result == nil || reply.result.IsError {
		t.Fatalf("tool result = %+v: %s", reply.result, realMCPResultText(reply.result))
	}
}

func realMCPResultText(result *mcp.CallToolResult) string {
	var text strings.Builder
	if result != nil {
		for _, content := range result.Content {
			if part, ok := content.(*mcp.TextContent); ok {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String()
}

func realMCPRequireFrames(t *testing.T, transport *realMCPRecordingTransport, want ...[]byte) {
	t.Helper()
	got, _, _ := transport.snapshot()
	if len(got) != len(want) {
		t.Fatalf("frames = %x; want %x", got, want)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("frame %d = %x; want %x (all frames %x)", i, got[i], want[i], got)
		}
	}
}

func realMCPDragArguments() map[string]any {
	return map[string]any{"start_x": 0, "start_y": 0, "end_x": 3, "end_y": 2, "button": "left", "button_mode": "relative"}
}

func realMCPRequireInputMethods(t *testing.T, fixture realMCPFixture, want ...string) {
	t.Helper()
	var got []string
	for _, input := range fixture.bridge.inputCalls() {
		got = append(got, input.method)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MCP API input methods = %v; want %v", got, want)
	}
}

func TestRealMCPDragUsesOneAtomicAPIAction(t *testing.T) {
	fixture := newRealMCPFixture(t)
	realMCPSuccess(t, realMCPCall(fixture, "capture_screen", nil))
	down, proceed := make(chan struct{}), make(chan struct{})
	fixture.transport.setHook(func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.MouseRelative(1, 0, 0, 0)) {
			close(down)
			select {
			case <-proceed:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	dragged := make(chan realMCPToolReply, 1)
	go func() { dragged <- realMCPCall(fixture, "mouse_drag", realMCPDragArguments()) }()
	realMCPReceive(t, down)
	queued := make(chan error, 1)
	go func() { queued <- fixture.keyboard.SendKey(context.Background(), "z", nil) }()
	select {
	case err := <-queued:
		t.Fatalf("second public caller interleaved with MCP drag: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	realMCPRequireFrames(t, fixture.transport, ch9329.MouseAbsolute(0, 0, 0, 0), ch9329.MouseRelative(1, 0, 0, 0))
	close(proceed)
	realMCPSuccess(t, realMCPReceive(t, dragged))
	if err := realMCPReceive(t, queued); err != nil {
		t.Fatal(err)
	}
	realMCPRequireFrames(t, fixture.transport,
		ch9329.MouseAbsolute(0, 0, 0, 0), ch9329.MouseRelative(1, 0, 0, 0),
		ch9329.MouseAbsolute(1, 4095, 4095, 0), ch9329.MouseAbsolute(0, 4095, 4095, 0), ch9329.MouseRelative(0, 0, 0, 0),
		ch9329.Keyboard(0, 0x1d), ch9329.Keyboard(0),
	)
	realMCPRequireInputMethods(t, fixture, "mouse_drag")
	var parameters map[string]any
	if err := json.Unmarshal(fixture.bridge.inputCalls()[0].params, &parameters); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"start_x": float64(0), "start_y": float64(0), "end_x": float64(1279), "end_y": float64(719), "button": "left", "button_mode": "relative"}
	if !reflect.DeepEqual(parameters, want) {
		t.Fatalf("drag API parameters = %v; want %v", parameters, want)
	}
	if _, prepares, _ := fixture.transport.snapshot(); prepares != 2 {
		t.Fatalf("prepared %d times; want one drag and one queued key action", prepares)
	}
}

func TestRealMCPDragCleanupPrecedesNextInput(t *testing.T) {
	for _, kind := range []string{"failed move", "cancel after down"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newRealMCPFixture(t)
			realMCPSuccess(t, realMCPCall(fixture, "capture_screen", nil))
			down, cleaning, finishCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var release sync.Once
			fixture.transport.setHook(func(ctx context.Context, frame []byte) error {
				if bytes.Equal(frame, ch9329.MouseRelative(1, 0, 0, 0)) {
					close(down)
					if kind == "cancel after down" {
						<-ctx.Done()
						return ctx.Err()
					}
				}
				if kind == "failed move" && bytes.Equal(frame, ch9329.MouseAbsolute(1, 4095, 4095, 0)) {
					return errors.New("forced move failure")
				}
				var err error
				if bytes.Equal(frame, ch9329.MouseRelative(0, 0, 0, 0)) {
					release.Do(func() {
						close(cleaning)
						select {
						case <-finishCleanup:
						case <-ctx.Done():
							err = ctx.Err()
						}
					})
				}
				return err
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dragged := make(chan realMCPToolReply, 1)
			go func() {
				result, err := fixture.client.CallTool(ctx, &mcp.CallToolParams{Name: "mouse_drag", Arguments: realMCPDragArguments()})
				dragged <- realMCPToolReply{result, err}
			}()
			realMCPReceive(t, down)
			if kind == "cancel after down" {
				cancel()
				if reply := realMCPReceive(t, dragged); !errors.Is(reply.err, context.Canceled) {
					t.Fatalf("canceled SDK call = %v; want context.Canceled", reply.err)
				}
			}
			// SDK cancel notifications are asynchronous: returning from CallTool
			// alone does not prove the daemon has begun its release cleanup.
			realMCPReceive(t, cleaning)
			next := make(chan realMCPToolReply, 1)
			go func() { next <- realMCPCall(fixture, "send_key", map[string]any{"key": "c"}) }()
			select {
			case reply := <-next:
				t.Fatalf("next input bypassed release cleanup: %+v", reply)
			case <-time.After(20 * time.Millisecond):
			}
			want := [][]byte{ch9329.MouseAbsolute(0, 0, 0, 0), ch9329.MouseRelative(1, 0, 0, 0)}
			if kind == "failed move" {
				want = append(want, ch9329.MouseAbsolute(1, 4095, 4095, 0))
			}
			want = append(want, ch9329.MouseRelative(0, 0, 0, 0))
			realMCPRequireFrames(t, fixture.transport, want...)
			close(finishCleanup)
			if kind == "failed move" {
				reply := realMCPReceive(t, dragged)
				if reply.err != nil || reply.result == nil || !reply.result.IsError || !strings.Contains(realMCPResultText(reply.result), "forced move failure") {
					t.Fatalf("failed drag result = %+v, %v", reply.result, reply.err)
				}
			}
			realMCPSuccess(t, realMCPReceive(t, next))
			want = append(want, ch9329.Keyboard(0, 6), ch9329.Keyboard(0))
			realMCPRequireFrames(t, fixture.transport, want...)
			realMCPRequireInputMethods(t, fixture, "mouse_drag", "send_key")
			if _, prepares, closes := fixture.transport.snapshot(); prepares != 3 || closes != 0 {
				t.Fatalf("prepares=%d closes=%d; want drag, release preparation, next input without permanent close", prepares, closes)
			}
		})
	}
}

func TestRealMCPTypingCancellationClosesAPIConnection(t *testing.T) {
	for _, kind := range []string{"cancel notification", "transport EOF", "process lifetime"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newRealMCPFixture(t)
			pressed, apiCanceled, cleaned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var release sync.Once
			fixture.transport.setHook(func(ctx context.Context, frame []byte) error {
				if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
					close(pressed)
					<-ctx.Done()
					close(apiCanceled)
					return ctx.Err()
				}
				if bytes.Equal(frame, ch9329.Keyboard(0)) {
					release.Do(func() { close(cleaned) })
				}
				return ctx.Err()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			typed := make(chan realMCPToolReply, 1)
			go func() {
				result, err := fixture.client.CallTool(ctx, &mcp.CallToolParams{Name: "type_text", Arguments: map[string]any{"text": "ab", "char_delay_ms": 0}})
				typed <- realMCPToolReply{result, err}
			}()
			realMCPReceive(t, pressed)
			switch kind {
			case "cancel notification":
				cancel()
			case "transport EOF":
				if err := fixture.disconnect(); err != nil {
					t.Fatal(err)
				}
			case "process lifetime":
				// Keep the SDK carrier open: process cancellation must stop
				// API work before graceful SDK session closing can drain it.
				fixture.cancelLife()
			}
			reply := realMCPReceive(t, typed)
			if kind == "cancel notification" && !errors.Is(reply.err, context.Canceled) {
				t.Fatalf("canceled SDK call = %v; want context.Canceled", reply.err)
			}
			if reply.err == nil && (reply.result == nil || !reply.result.IsError) {
				t.Fatalf("interrupted typing succeeded: %+v", reply.result)
			}
			realMCPReceive(t, apiCanceled)
			realMCPReceive(t, cleaned)
			realMCPRequireFrames(t, fixture.transport, ch9329.Keyboard(0, 4), ch9329.Keyboard(0))
			realMCPRequireInputMethods(t, fixture, "type_text")
			if _, prepares, closes := fixture.transport.snapshot(); prepares != 2 || closes != 0 {
				t.Fatalf("prepares=%d closes=%d; want typing and cleanup without a subsequent action or permanent close", prepares, closes)
			}
		})
	}
}
