package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type capturedScreen struct {
	jpeg       []byte
	dimensions screenDimensions
}

// capture validates and optionally logs a frame, without changing screenshot
// coordinates. Only a tool that successfully returns an image may commit them.
func (owner *toolServer) capture(ctx context.Context, tool string) (capturedScreen, error) {
	var frame struct {
		JPEG            []byte          `json:"jpeg_b64"`
		Width           int             `json:"width"`
		Height          int             `json:"height"`
		TargetWidth     json.RawMessage `json:"target_width"`
		TargetHeight    json.RawMessage `json:"target_height"`
		MappingRevision *uint64         `json:"mapping_revision"`
	}
	if err := owner.caller.Call(ctx, "capture_frame", struct{}{}, &frame); err != nil {
		return capturedScreen{}, err
	}
	if frame.Width <= 0 || frame.Height <= 0 {
		return capturedScreen{}, errors.New("capture returned invalid image dimensions")
	}
	image, err := jpeg.DecodeConfig(bytes.NewReader(frame.JPEG))
	if err != nil {
		return capturedScreen{}, fmt.Errorf("invalid captured JPEG: %w", err)
	}
	if image.Width != frame.Width || image.Height != frame.Height {
		return capturedScreen{}, errors.New("captured JPEG dimensions do not match capture metadata")
	}
	var targetWidth, targetHeight int
	if len(frame.TargetWidth) == 0 && len(frame.TargetHeight) == 0 {
		// Older daemons omit both target dimensions. Keep their coordinate mapping
		// compatible, without rediscovering capture devices on the current path.
		var info struct {
			Config struct {
				ScreenWidth  int `json:"screen_width"`
				ScreenHeight int `json:"screen_height"`
			} `json:"config"`
		}
		if err := owner.caller.Call(ctx, "get_device_info", struct{}{}, &info); err != nil {
			return capturedScreen{}, err
		}
		targetWidth, targetHeight = info.Config.ScreenWidth, info.Config.ScreenHeight
		if targetWidth <= 0 || targetHeight <= 0 {
			return capturedScreen{}, errors.New("device info returned invalid target screen dimensions")
		}
	} else if json.Unmarshal(frame.TargetWidth, &targetWidth) != nil || json.Unmarshal(frame.TargetHeight, &targetHeight) != nil || targetWidth <= 0 || targetHeight <= 0 {
		return capturedScreen{}, errors.New("capture returned invalid target screen dimensions")
	}
	if err := ctx.Err(); err != nil {
		return capturedScreen{}, err
	}
	if owner.options.CaptureLogDir != "" {
		if err := writeCaptureLog(owner.options.CaptureLogDir, tool, frame.JPEG); err != nil {
			return capturedScreen{}, err
		}
	}
	return capturedScreen{jpeg: frame.JPEG, dimensions: screenDimensions{frame.Width, frame.Height, targetWidth, targetHeight, frame.MappingRevision}}, nil
}

func (owner *toolServer) captureScreen(ctx context.Context, request *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, error) {
	state := owner.state(request.Session)
	epoch := state.beginCapture()
	frame, err := owner.capture(ctx, "capture_screen")
	if err != nil {
		return nil, err
	}
	if err := state.commit(ctx, epoch, frame.dimensions); err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: frame.jpeg, MIMEType: "image/jpeg"},
		&mcp.TextContent{Text: fmt.Sprintf("Screenshot: %dx%d pixels. Absolute mouse coordinates use this image.", frame.dimensions.width, frame.dimensions.height)},
	}}, nil
}
