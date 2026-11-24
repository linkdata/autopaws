package main

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type executeInput struct {
	ocrInput
	Command     string   `json:"command" jsonschema:"Command to type literally and execute with Enter."`
	WaitSeconds *float64 `json:"wait_seconds,omitempty" jsonschema:"Seconds to wait before a fresh capture. Finite 0 through 60, default 1. A fixed wait does not guarantee command completion."`
}

func (owner *toolServer) screenText(ctx context.Context, tool string, region *screenRegion) (*mcp.CallToolResult, error) {
	if err := region.validate(); err != nil {
		return nil, err
	}
	// The shared capture helper validates/logs bytes without committing mouse
	// coordinates: OCR returns text, so no screenshot was shown to this session.
	frame, err := owner.capture(ctx, tool)
	if err != nil {
		return nil, err
	}
	text, err := extractOCR(ctx, owner.options.TesseractPath, frame.jpeg, region)
	if err != nil {
		return nil, err
	}
	return textResult(text), nil
}

func (owner *toolServer) getScreenText(ctx context.Context, _ *mcp.CallToolRequest, input ocrInput) (*mcp.CallToolResult, error) {
	return owner.screenText(ctx, "get_screen_text", input.Region)
}

func executionWait(seconds *float64) (time.Duration, error) {
	if seconds == nil {
		return time.Second, nil
	}
	if math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds < 0 || *seconds > 60 {
		return 0, fmt.Errorf("wait_seconds must be finite and in 0..60")
	}
	return time.Duration(*seconds * float64(time.Second)), nil
}

func (owner *toolServer) executeAndRead(ctx context.Context, _ *mcp.CallToolRequest, input executeInput) (*mcp.CallToolResult, error) {
	if err := input.Region.validate(); err != nil {
		return nil, err
	}
	wait, err := executionWait(input.WaitSeconds)
	if err != nil {
		return nil, err
	}
	if _, err := owner.call(ctx, "type_text", textInput{Text: input.Command + "\n", Raw: true}); err != nil {
		return nil, err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	return owner.screenText(ctx, "execute_and_read", input.Region)
}
