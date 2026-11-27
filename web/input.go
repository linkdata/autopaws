//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/hid"
)

var ErrInput = errors.New("invalid web input")

type inputEvent struct {
	Type            string          `json:"type"`
	Code            *string         `json:"code,omitempty"`
	X               *int            `json:"x,omitempty"`
	Y               *int            `json:"y,omitempty"`
	Buttons         *int            `json:"buttons,omitempty"`
	DeltaY          *float64        `json:"deltaY,omitempty"`
	Keys            *[]string       `json:"keys,omitempty"`
	Revision        string          `json:"revision,omitempty"`
	FrameSequence   string          `json:"frame_sequence,omitempty"`
	FrameGeneration string          `json:"frame_generation,omitempty"`
	Mapping         *hid.MappingSet `json:"mapping,omitempty"`
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

func (server *Server) read(ctx context.Context, conn socket, input *browserInput) error {
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
		if err := input.apply(ctx, event); err != nil {
			if err := input.recover(ctx, err); err != nil {
				return err
			}
		} else {
			input.setStatus("")
		}
	}
}
