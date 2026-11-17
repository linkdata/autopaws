//go:build linux && (amd64 || arm64)

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/hid"
)

var ErrInput = errors.New("invalid web input")

type inputEvent struct {
	Type    string    `json:"type"`
	Code    *string   `json:"code,omitempty"`
	X       *int      `json:"x,omitempty"`
	Y       *int      `json:"y,omitempty"`
	Buttons *int      `json:"buttons,omitempty"`
	DeltaY  *float64  `json:"deltaY,omitempty"`
	Keys    *[]string `json:"keys,omitempty"`
}

func decodeInput(data []byte) (inputEvent, error) {
	var event inputEvent
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return event, ErrInput
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return event, fmt.Errorf("%w: %v", ErrInput, err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return event, ErrInput
	}
	return event, nil
}

func (server *Server) read(ctx context.Context, conn socket, browser *hid.Browser) error {
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if kind != websocket.MessageText {
			return ErrInput
		}
		event, err := decodeInput(data)
		if err != nil {
			return err
		}
		if server.options.KeyLogger != nil {
			server.options.KeyLogger.Debug("Web input", "message", string(data))
		}
		if err := applyInput(ctx, browser, event); err != nil {
			return err
		}
	}
}

func applyInput(ctx context.Context, browser *hid.Browser, event inputEvent) error {
	switch event.Type {
	case "keydown", "keyup":
		if event.Code == nil {
			return ErrInput
		}
		return browser.Key(ctx, *event.Code, event.Type == "keydown")
	case "mousemove", "mousedown", "mouseup":
		if event.X == nil || event.Y == nil || event.Buttons == nil || *event.Buttons < 0 || *event.Buttons > 7 {
			return ErrInput
		}
		if event.Type == "mousemove" {
			return browser.Move(ctx, *event.X, *event.Y)
		}
		return browser.Buttons(ctx, *event.X, *event.Y, *event.Buttons)
	case "scroll":
		if event.DeltaY == nil {
			return ErrInput
		}
		return browser.Scroll(ctx, int(math.Max(-127, math.Min(127, *event.DeltaY))))
	case "release_all":
		return browser.Release(ctx)
	case "shortcut":
		if event.Keys == nil {
			return ErrInput
		}
		return browser.Shortcut(ctx, *event.Keys)
	default:
		return ErrInput
	}
}
