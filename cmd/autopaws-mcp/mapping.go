package main

import (
	"context"

	"github.com/linkdata/autopaws/hid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mappingGuidance = " Consult setup before target work. If movement disagrees with the image, check coordinate units and the target's actual absolute HID input span before changing this shared mapping."

type mappingSetInput struct {
	Mode     string               `json:"mode,omitempty" jsonschema:"legacy restores the identity mapping; extended blocks positioned input until geometry is configured. Reset preserves this mode."`
	Geometry *hid.MappingGeometry `json:"geometry,omitempty" jsonschema:"Optional complete desktop geometry in the same desktop units, including signed display offset and the span addressed by absolute HID. Applies immediately after structural bounds validation; the caller must supply correct target geometry. An offset alone is insufficient."`
}

func (owner *toolServer) getMouseMapping(ctx context.Context, _ *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, error) {
	return owner.call(ctx, "get_mouse_mapping", input)
}

func (owner *toolServer) setMouseMapping(ctx context.Context, request *mcp.CallToolRequest, input mappingSetInput) (*mcp.CallToolResult, error) {
	state := owner.state(request.Session)
	state.invalidate()
	defer state.invalidate()
	return owner.call(ctx, "set_mouse_mapping", input)
}

func (owner *toolServer) resetMouseMapping(ctx context.Context, request *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, error) {
	state := owner.state(request.Session)
	state.invalidate()
	defer state.invalidate()
	return owner.call(ctx, "reset_mouse_mapping", input)
}
