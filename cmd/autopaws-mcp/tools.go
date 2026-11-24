package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type apiCaller interface {
	Call(context.Context, string, any, any) error
}

type toolServer struct {
	lifetime     context.Context
	options      mcpOptions
	caller       apiCaller
	mu           sync.Mutex
	sessions     map[*mcp.ServerSession]*screenState
	instance     string
	observations atomic.Uint64
}

type textInput struct {
	waitInput
	Text        string `json:"text" jsonschema:"Text to type. Characters unavailable in the target layout are rejected before typing."`
	Raw         bool   `json:"raw,omitempty" jsonschema:"Type braces literally instead of interpreting key tags. Default false."`
	CharDelayMS *int64 `json:"char_delay_ms,omitempty" jsonschema:"Delay after each character or tag, including the last, in milliseconds, 0 through 60000. Default 20."`
}

type keyInput struct {
	waitInput
	Key       string   `json:"key" jsonschema:"Named key such as enter, escape, f1, a, or a 0x-prefixed HID keycode."`
	Modifiers []string `json:"modifiers,omitempty" jsonschema:"Modifiers such as ctrl, shift, alt, or super."`
}

type keyStepInput struct {
	Key       string   `json:"key" jsonschema:"Named key or 0x-prefixed HID keycode."`
	Modifiers []string `json:"modifiers,omitempty" jsonschema:"Modifiers such as ctrl, shift, alt, or super."`
	DelayMS   *int64   `json:"delay_ms,omitempty" jsonschema:"Delay after this key in milliseconds, 0 through 60000."`
}

type sequenceInput struct {
	waitInput
	Steps          []keyStepInput `json:"steps" jsonschema:"Complete sequence, at most 4096 steps. Every step is validated before typing."`
	DefaultDelayMS *int64         `json:"default_delay_ms,omitempty" jsonschema:"Default delay after each key in milliseconds, 0 through 60000. Default 100."`
}

type moveInput struct {
	waitInput
	X        int  `json:"x" jsonschema:"Screenshot x pixel, or signed horizontal delta when relative is true."`
	Y        int  `json:"y" jsonschema:"Screenshot y pixel, or signed vertical delta when relative is true."`
	Relative bool `json:"relative,omitempty" jsonschema:"Use unscaled relative deltas, each -127 through 127. Default false."`
}

type clickInput struct {
	waitInput
	Button     string `json:"button,omitempty" jsonschema:"left, right, or middle. Default left."`
	X          *int   `json:"x,omitempty" jsonschema:"Optional screenshot x pixel. Supply both x and y."`
	Y          *int   `json:"y,omitempty" jsonschema:"Optional screenshot y pixel. Supply both x and y."`
	ButtonMode string `json:"button_mode,omitempty" jsonschema:"absolute or relative. Omit to use the daemon's configured mode."`
}

type dragInput struct {
	waitInput
	StartX     int    `json:"start_x" jsonschema:"Start x pixel in the latest screenshot."`
	StartY     int    `json:"start_y" jsonschema:"Start y pixel in the latest screenshot."`
	EndX       int    `json:"end_x" jsonschema:"End x pixel in the latest screenshot."`
	EndY       int    `json:"end_y" jsonschema:"End y pixel in the latest screenshot."`
	Button     string `json:"button,omitempty" jsonschema:"left, right, or middle. Default left."`
	ButtonMode string `json:"button_mode,omitempty" jsonschema:"absolute or relative. Omit to use the daemon's configured mode."`
}

type scrollInput struct {
	waitInput
	Amount int `json:"amount" jsonschema:"Signed wheel amount, clamped by the daemon to -127 through 127."`
}

type captureDeviceInput struct {
	Device string `json:"device" jsonschema:"Capture device path such as /dev/video0, macOS avfoundation:NAME, or Windows dshow:SELECTOR from list_capture_devices. Numeric indexes are not supported."`
}

type captureResolutionInput struct {
	Width  uint32 `json:"width" jsonschema:"Positive capture width in pixels."`
	Height uint32 `json:"height" jsonschema:"Positive capture height in pixels."`
}

type keyboardLayoutInput struct {
	Layout string `json:"layout" jsonschema:"Target text layout name: us104, uk105, de105, fr105, jp106, se105, or a custom name from the daemon's configured layouts directory."`
}

func newServer(lifetime context.Context, options mcpOptions, caller apiCaller) *mcp.Server {
	owner := &toolServer{lifetime: lifetime, options: options, caller: caller, sessions: make(map[*mcp.ServerSession]*screenState), instance: rand.Text()}
	server := mcp.NewServer(&mcp.Implementation{Name: "autopaws-mcp", Version: options.Version}, &mcp.ServerOptions{Logger: options.Logger})
	addTool(server, owner, "type_text", "Type text on the target keyboard. The entire text is validated against the active target layout before input. By default {enter} and {ctrl+c} are key tags; raw types braces literally. Character delay defaults to 20 ms.", false, owner.typeText)
	addTool(server, owner, "send_key", "Press and release one named key or 0x-prefixed HID keycode, with optional ctrl, shift, alt or super modifiers.", false, owner.sendKey)
	addTool(server, owner, "send_key_sequence", "Send one atomic keyboard sequence of at most 4096 steps. The complete sequence is validated first. Step delays default to 100 ms and may be 0 through 60000 ms.", false, owner.sendKeySequence)
	addTool(server, owner, "mouse_move", "Move the target pointer. Absolute x/y are integer pixels in this session's latest successful capture_screen image; capture first. Relative deltas are unchanged and each must be -127 through 127.", false, owner.mouseMove)
	addTool(server, owner, "mouse_click", "Click left, right or middle (default left). Optional x and y are pixels in this session's latest successful capture_screen; supply both. A click at the current pointer needs no screenshot. Optional button_mode overrides the daemon setting.", false, owner.mouseClick)
	addTool(server, owner, "mouse_drag", "Drag in one atomic daemon action, with 50 ms after button down and after the move. Both endpoints are pixels in this session's latest successful capture_screen image. Default button left; button_mode may override the daemon setting.", false, owner.mouseDrag)
	addTool(server, owner, "mouse_scroll", "Scroll the target's wheel by a signed amount, clamped to -127 through 127.", false, owner.mouseScroll)
	addTool(server, owner, "capture_screen", "Capture the current screen as an unchanged JPEG image with its width and height. Establishes this session's pixel coordinates for later absolute mouse actions.", true, owner.captureScreen)
	addTool(server, owner, "get_device_info", "Read configured serial and target screen settings, active keyboard layout, and capture information from the daemon.", true, owner.deviceInfo)
	addTool(server, owner, "list_capture_devices", "List capture devices available to the daemon.", true, owner.listCaptureDevices)
	addTool(server, owner, "list_keyboard_layouts", "List the daemon's built-in and configured custom text layouts and current selection. Entries report their source and any load error; this does not change the layout or target OS input source.", true, owner.listKeyboardLayouts)
	addTool(server, owner, "set_capture_device", "Select a capture device by path. Every attempt invalidates this session's screenshot coordinates, including failed attempts; capture_screen again before absolute mouse input.", false, owner.setCaptureDevice)
	addTool(server, owner, "set_capture_resolution", "Set a positive capture width and height. Every attempt invalidates this session's screenshot coordinates, including failed attempts; capture_screen again before absolute mouse input.", false, owner.setCaptureResolution)
	addTool(server, owner, "set_keyboard_layout", "Select the daemon's target text layout for subsequent keyboard actions without restarting or reconnecting. Use list_keyboard_layouts to discover names. This runtime setting does not change the target OS input source or screenshot coordinates; restart restores the configured startup layout. Selecting the same name reloads its file. Use sequential requests through one MCP connection.", false, owner.setKeyboardLayout)
	if options.TesseractPath != "" {
		addTool(server, owner, "get_screen_text", "Capture the current target screen and extract English text with local Tesseract OCR, optionally limited to a region in screenshot pixels. Prefer this to an image for terminal text. Does not establish screenshot coordinates for mouse actions.", true, owner.getScreenText)
		addTool(server, owner, "execute_and_read", "Type command plus Enter in one raw keyboard action, wait for output, then capture a fresh frame and extract English text with local Tesseract OCR, optionally limited to a region in screenshot pixels. The wait is cancellable, defaults to 1 second and accepts 0 through 60 seconds; a fresh frame does not prove the command has completed. Does not establish screenshot coordinates.", false, owner.executeAndRead)
	}
	return server
}

func addTool[In any](server *mcp.Server, owner *toolServer, name, description string, readOnly bool, handle func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, error)) {
	tool := &mcp.Tool{Name: name, Description: description}
	if owner.options.Vision == nil {
		schema, err := jsonschema.For[In](nil)
		if err != nil {
			panic(err)
		}
		delete(schema.Properties, "wait_for")
		delete(schema.Properties, "wait_timeout_ms")
		tool.InputSchema = schema
	}
	if readOnly {
		tool.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
	}
	mcp.AddTool(server, tool, func(ctx context.Context, request *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(owner.lifetime, cancel)
		defer stop()
		defer cancel()
		if owner.lifetime.Err() != nil {
			cancel()
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		var wait waitInput
		waitParams, inputTool := any(&input).(interface{ takeWait() waitInput })
		if inputTool {
			wait = waitParams.takeWait()
			if err := validateWait(wait); err != nil {
				return nil, nil, err
			}
		}
		if inputTool || name == "execute_and_read" {
			state := owner.state(request.Session)
			unlock, err := state.lockAction(ctx)
			if err != nil {
				return nil, nil, err
			}
			defer unlock()
		}
		started := time.Now()
		result, err := handle(ctx, request, input)
		if err == nil && wait.WaitFor != nil {
			return owner.observe(ctx, request, wait, result, time.Since(started))
		}
		return result, nil, err
	})
}

func (owner *toolServer) state(session *mcp.ServerSession) *screenState {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if state := owner.sessions[session]; state != nil {
		return state
	}
	state := &screenState{}
	owner.sessions[session] = state
	go func() {
		_ = session.Wait()
		owner.mu.Lock()
		delete(owner.sessions, session)
		owner.mu.Unlock()
	}()
	return state
}

func (owner *toolServer) call(ctx context.Context, method string, input any) (*mcp.CallToolResult, error) {
	var result json.RawMessage
	if err := owner.caller.Call(ctx, method, input, &result); err != nil {
		return nil, err
	}
	return textResult(string(result)), nil
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func (owner *toolServer) typeText(ctx context.Context, _ *mcp.CallToolRequest, input textInput) (*mcp.CallToolResult, error) {
	return owner.call(ctx, "type_text", input)
}

func (owner *toolServer) sendKey(ctx context.Context, _ *mcp.CallToolRequest, input keyInput) (*mcp.CallToolResult, error) {
	return owner.call(ctx, "send_key", input)
}

func (owner *toolServer) sendKeySequence(ctx context.Context, _ *mcp.CallToolRequest, input sequenceInput) (*mcp.CallToolResult, error) {
	return owner.call(ctx, "send_key_sequence", input)
}

func (owner *toolServer) mouseScroll(ctx context.Context, _ *mcp.CallToolRequest, input scrollInput) (*mcp.CallToolResult, error) {
	return owner.call(ctx, "mouse_scroll", input)
}

func (owner *toolServer) deviceInfo(ctx context.Context, _ *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, error) {
	return owner.call(ctx, "get_device_info", input)
}

func (owner *toolServer) listCaptureDevices(ctx context.Context, _ *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, error) {
	return owner.call(ctx, "list_capture_devices", input)
}

func (owner *toolServer) listKeyboardLayouts(ctx context.Context, _ *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, error) {
	return owner.call(ctx, "list_keyboard_layouts", input)
}

func (owner *toolServer) setCaptureDevice(ctx context.Context, request *mcp.CallToolRequest, input captureDeviceInput) (*mcp.CallToolResult, error) {
	state := owner.state(request.Session)
	state.invalidate()
	defer state.invalidate()
	return owner.call(ctx, "set_capture_device", input)
}

func (owner *toolServer) setCaptureResolution(ctx context.Context, request *mcp.CallToolRequest, input captureResolutionInput) (*mcp.CallToolResult, error) {
	state := owner.state(request.Session)
	state.invalidate()
	defer state.invalidate()
	return owner.call(ctx, "set_capture_resolution", input)
}

func (owner *toolServer) setKeyboardLayout(ctx context.Context, _ *mcp.CallToolRequest, input keyboardLayoutInput) (*mcp.CallToolResult, error) {
	return owner.call(ctx, "set_keyboard_layout", input)
}
