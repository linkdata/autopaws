package main

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type screenDimensions struct {
	width, height             int
	targetWidth, targetHeight int
}

type screenState struct {
	mu         sync.Mutex
	epoch      uint64
	dimensions screenDimensions
	action     chan struct{}
}

// Serialize this session's input through observation without blocking capture
// configuration changes, which must still invalidate an in-flight image.
func (state *screenState) lockAction(ctx context.Context) (func(), error) {
	state.mu.Lock()
	if state.action == nil {
		state.action = make(chan struct{}, 1)
	}
	gate := state.action
	state.mu.Unlock()
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return nil, err
		}
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (state *screenState) invalidate() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.epoch++
	state.dimensions = screenDimensions{}
}

func (state *screenState) beginCapture() uint64 {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.epoch
}

func (state *screenState) commit(ctx context.Context, epoch uint64, dimensions screenDimensions) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if state.epoch != epoch {
		return errors.New("capture configuration changed during capture; capture_screen again")
	}
	state.dimensions = dimensions
	return nil
}

func (state *screenState) snapshot() (screenDimensions, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.dimensions.width == 0 {
		return screenDimensions{}, errors.New("capture_screen must succeed before absolute mouse input")
	}
	return state.dimensions, nil
}

// Pixel mapping uses a full 128-bit product, so large int dimensions cannot
// overflow or lose precision. Division's remainder gives exact half-up rounding.
func scalePixel(value, source, target int) (int, error) {
	if source <= 0 || target <= 0 || value < 0 || value >= source {
		return 0, fmt.Errorf("pixel %d outside image axis of length %d", value, source)
	}
	if source == 1 || target == 1 {
		return 0, nil
	}
	hi, lo := bits.Mul64(uint64(value), uint64(target-1))
	denominator := uint64(source - 1)
	quotient, remainder := bits.Div64(hi, lo, denominator)
	if remainder >= denominator/2+denominator%2 {
		quotient++
	}
	return int(quotient), nil
}

func (dimensions screenDimensions) point(x, y int) (int, int, error) {
	x, err := scalePixel(x, dimensions.width, dimensions.targetWidth)
	if err != nil {
		return 0, 0, fmt.Errorf("x: %w", err)
	}
	y, err = scalePixel(y, dimensions.height, dimensions.targetHeight)
	if err != nil {
		return 0, 0, fmt.Errorf("y: %w", err)
	}
	return x, y, nil
}

func (owner *toolServer) mouseMove(ctx context.Context, request *mcp.CallToolRequest, input moveInput) (*mcp.CallToolResult, error) {
	if !input.Relative {
		dimensions, err := owner.state(request.Session).snapshot()
		if err != nil {
			return nil, err
		}
		input.X, input.Y, err = dimensions.point(input.X, input.Y)
		if err != nil {
			return nil, err
		}
	}
	return owner.call(ctx, "mouse_move", input)
}

func (owner *toolServer) mouseClick(ctx context.Context, request *mcp.CallToolRequest, input clickInput) (*mcp.CallToolResult, error) {
	if (input.X == nil) != (input.Y == nil) {
		return nil, errors.New("x and y must be supplied together")
	}
	if input.X != nil {
		dimensions, err := owner.state(request.Session).snapshot()
		if err != nil {
			return nil, err
		}
		x, y, err := dimensions.point(*input.X, *input.Y)
		if err != nil {
			return nil, err
		}
		input.X, input.Y = &x, &y
	}
	return owner.call(ctx, "mouse_click", input)
}

func (owner *toolServer) mouseDrag(ctx context.Context, request *mcp.CallToolRequest, input dragInput) (*mcp.CallToolResult, error) {
	dimensions, err := owner.state(request.Session).snapshot()
	if err != nil {
		return nil, err
	}
	input.StartX, input.StartY, err = dimensions.point(input.StartX, input.StartY)
	if err != nil {
		return nil, err
	}
	input.EndX, input.EndY, err = dimensions.point(input.EndX, input.EndY)
	if err != nil {
		return nil, err
	}
	return owner.call(ctx, "mouse_drag", input)
}
