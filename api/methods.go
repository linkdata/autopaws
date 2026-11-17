//go:build linux && (amd64 || arm64)

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/hid"
)

func (server *Server) methods() map[string]handler {
	return map[string]handler{
		"ping": server.ping, "type_text": server.typeText,
		"send_key": server.sendKey, "send_key_sequence": server.sendKeySequence,
		"mouse_move": server.mouseMove, "mouse_click": server.mouseClick,
		"mouse_down": server.mouseDown, "mouse_up": server.mouseUp,
		"mouse_scroll": server.mouseScroll, "mouse_drag": server.mouseDrag,
		"get_device_info": server.deviceInfo,
		"capture_frame":   server.captureFrame, "list_capture_devices": server.listCaptureDevices,
		"set_capture_device": server.setCaptureDevice, "set_capture_resolution": server.setCaptureResolution,
	}
}

func params(data json.RawMessage, output any) error {
	return decodeObject(data, output, apiwire.ErrInvalidParams)
}

func required(name string) error {
	return fmt.Errorf("%w: %s is required", apiwire.ErrInvalidParams, name)
}

func milliseconds(value *int64) (*time.Duration, error) {
	if value == nil {
		return nil, nil
	}
	if *value < 0 || *value > int64(hid.MaxDelay/time.Millisecond) {
		return nil, fmt.Errorf("%w: delay must be in 0..%d ms", apiwire.ErrInvalidParams, hid.MaxDelay/time.Millisecond)
	}
	delay := time.Duration(*value) * time.Millisecond
	return &delay, nil
}

func (server *Server) ping(_ context.Context, data json.RawMessage) (any, error) {
	if err := params(data, &struct{}{}); err != nil {
		return nil, err
	}
	return map[string]bool{"pong": true}, nil
}

func (server *Server) typeText(ctx context.Context, data json.RawMessage) (any, error) {
	var request struct {
		Text      *string `json:"text"`
		CharDelay *int64  `json:"char_delay_ms"`
		Raw       bool    `json:"raw"`
	}
	if err := params(data, &request); err != nil {
		return nil, err
	}
	if request.Text == nil {
		return nil, required("text")
	}
	delay, err := milliseconds(request.CharDelay)
	if err != nil {
		return nil, err
	}
	if server.options.KeyLogger != nil {
		server.options.KeyLogger.Debug("API typed text", "text", *request.Text, "raw", request.Raw)
	}
	if err := server.keyboard.TypeText(ctx, *request.Text, hid.TypeOptions{Raw: request.Raw, CharDelay: delay}); err != nil {
		return nil, err
	}
	return map[string]int{"chars_typed": utf8.RuneCountInString(*request.Text)}, nil
}

func (server *Server) sendKey(ctx context.Context, data json.RawMessage) (any, error) {
	var request struct {
		Key       *string  `json:"key"`
		Modifiers []string `json:"modifiers"`
	}
	if err := params(data, &request); err != nil {
		return nil, err
	}
	if request.Key == nil {
		return nil, required("key")
	}
	if err := server.keyboard.SendKey(ctx, *request.Key, request.Modifiers); err != nil {
		return nil, err
	}
	return map[string]bool{"sent": true}, nil
}

func (server *Server) sendKeySequence(ctx context.Context, data json.RawMessage) (any, error) {
	var request struct {
		Steps *[]struct {
			Key       *string  `json:"key"`
			Modifiers []string `json:"modifiers"`
			Delay     *int64   `json:"delay_ms"`
		} `json:"steps"`
		DefaultDelay *int64 `json:"default_delay_ms"`
	}
	if err := params(data, &request); err != nil {
		return nil, err
	}
	if request.Steps == nil {
		return nil, required("steps")
	}
	if len(*request.Steps) > hid.MaxSequenceSteps {
		return nil, hid.ErrSequence
	}
	delay, err := milliseconds(request.DefaultDelay)
	if err != nil {
		return nil, err
	}
	steps := make([]hid.KeyStep, len(*request.Steps))
	for i, step := range *request.Steps {
		if step.Key == nil {
			return nil, required(fmt.Sprintf("steps[%d].key", i))
		}
		stepDelay, err := milliseconds(step.Delay)
		if err != nil {
			return nil, err
		}
		steps[i] = hid.KeyStep{Key: *step.Key, Modifiers: step.Modifiers, Delay: stepDelay}
	}
	if err := server.keyboard.SendKeySequence(ctx, steps, delay); err != nil {
		return nil, err
	}
	return map[string]int{"steps_sent": len(steps)}, nil
}

func (server *Server) mouseMove(ctx context.Context, data json.RawMessage) (any, error) {
	var request struct {
		X, Y     *int
		Relative bool `json:"relative"`
	}
	if err := params(data, &request); err != nil {
		return nil, err
	}
	if request.X == nil || request.Y == nil {
		return nil, required("x and y")
	}
	var err error
	if request.Relative {
		err = server.mouse.MoveRel(ctx, *request.X, *request.Y)
	} else {
		err = server.mouse.MoveAbs(ctx, *request.X, *request.Y)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"position": map[string]any{"x": *request.X, "y": *request.Y, "relative": request.Relative}}, nil
}

type buttonParams struct {
	Button string `json:"button"`
	X, Y   *int
	Mode   string `json:"button_mode"`
}

func buttonRequest(data json.RawMessage) (string, hid.ButtonOptions, error) {
	var request buttonParams
	if err := params(data, &request); err != nil {
		return "", hid.ButtonOptions{}, err
	}
	if (request.X == nil) != (request.Y == nil) {
		return "", hid.ButtonOptions{}, fmt.Errorf("%w: x and y must be supplied together", apiwire.ErrInvalidParams)
	}
	options := hid.ButtonOptions{Mode: request.Mode}
	if request.X != nil {
		options.Position = &hid.Point{X: *request.X, Y: *request.Y}
	}
	if request.Button == "" {
		request.Button = "left"
	}
	return request.Button, options, nil
}

func (server *Server) mouseClick(ctx context.Context, data json.RawMessage) (any, error) {
	button, options, err := buttonRequest(data)
	if err != nil {
		return nil, err
	}
	if err := server.mouse.Click(ctx, button, options); err != nil {
		return nil, err
	}
	return map[string]bool{"clicked": true}, nil
}

func (server *Server) mouseDown(ctx context.Context, data json.RawMessage) (any, error) {
	button, options, err := buttonRequest(data)
	if err != nil {
		return nil, err
	}
	if err := server.mouse.Down(ctx, button, options); err != nil {
		return nil, err
	}
	return map[string]any{"pressed": true, "button": button}, nil
}

func (server *Server) mouseUp(ctx context.Context, data json.RawMessage) (any, error) {
	button, options, err := buttonRequest(data)
	if err != nil {
		return nil, err
	}
	if err := server.mouse.Up(ctx, button, options); err != nil {
		return nil, err
	}
	return map[string]any{"released": true, "button": button}, nil
}

func (server *Server) mouseScroll(ctx context.Context, data json.RawMessage) (any, error) {
	var request struct {
		Amount *int `json:"amount"`
	}
	if err := params(data, &request); err != nil {
		return nil, err
	}
	if request.Amount == nil {
		return nil, required("amount")
	}
	if err := server.mouse.Scroll(ctx, *request.Amount); err != nil {
		return nil, err
	}
	return map[string]bool{"scrolled": true}, nil
}

func (server *Server) mouseDrag(ctx context.Context, data json.RawMessage) (any, error) {
	var request struct {
		StartX *int   `json:"start_x"`
		StartY *int   `json:"start_y"`
		EndX   *int   `json:"end_x"`
		EndY   *int   `json:"end_y"`
		Button string `json:"button"`
		Mode   string `json:"button_mode"`
	}
	if err := params(data, &request); err != nil {
		return nil, err
	}
	if request.StartX == nil || request.StartY == nil || request.EndX == nil || request.EndY == nil {
		return nil, required("start_x, start_y, end_x and end_y")
	}
	if err := server.mouse.Drag(ctx, hid.Point{X: *request.StartX, Y: *request.StartY}, hid.Point{X: *request.EndX, Y: *request.EndY}, request.Button, request.Mode); err != nil {
		return nil, err
	}
	return map[string]bool{"dragged": true}, nil
}

func (server *Server) deviceInfo(ctx context.Context, data json.RawMessage) (any, error) {
	if err := params(data, &struct{}{}); err != nil {
		return nil, err
	}
	options := server.options
	return map[string]any{
		"serial": map[string]any{"device": options.SerialDevice, "baud": options.SerialBaud, "auto_detect": options.SerialDevice == ""},
		"config": map[string]any{
			"screen_width": options.ScreenWidth, "screen_height": options.ScreenHeight,
			"keyboard_layout": options.KeyboardLayout, "serial_baud": options.SerialBaud,
			"mouse_button_mode": options.ButtonMode,
		},
		"capture": server.captureInfo(ctx),
	}, nil
}
