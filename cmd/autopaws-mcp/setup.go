package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const setupInstructions = "Call setup once before controlling the target, and again after target, display, or keyboard-layout changes."

const setupChecklist = `Before work (repeat after target/display/layout changes):

- Read get_device_info/get_mouse_mapping and capture_screen. Test absolute moves at two separated image points; verify the real target cursor in fresh captures before clicking. Use wait_for when available; otherwise poll fresh captures for the expected response instead of sleeping a fixed duration.
- If misaligned, stop positioned actions. Ask the user to mirror/reposition target displays or configure captured offset, size and absolute input bounds with set_mouse_mapping. Recheck.
- Check the target keyboard layout; use list_keyboard_layouts/set_keyboard_layout to match the KVM. Verify harmless sample typing before commands; leave the OS layout unchanged unless asked.
- Keep the working window visible. Maximize it for single-window tasks unless the user requests another layout.`

func (*toolServer) setup(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, error) {
	return textResult(setupChecklist), nil
}
