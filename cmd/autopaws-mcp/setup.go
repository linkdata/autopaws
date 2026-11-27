package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const setupInstructions = "Call setup once before controlling the target, and again after target, display, or keyboard-layout changes."

const setupChecklist = `Before work (repeat after target/display/layout changes):

- Read get_device_info and get_mouse_mapping. Call capture_screen. Use absolute screenshot coordinates. Check the real target cursor at two separated image points before clicking.
- Windows: use relative button reports (default).
- Linux: use relative button reports (default). Extended GNOME desktops may need set_mouse_mapping.
- macOS: first call mouse_move to the click point or drag start. Use button_mode=absolute for positioned mouse_click and mouse_drag. Verify real clicks and sustained drag/drop. Stop if holds fail.
- If an absolute destination is ignored after relative movement, restart KVM with -mouse-absolute-refresh.
- If misaligned, stop positioned actions. Mirror displays or make capture primary/top-left; configure only reachable displays with set_mouse_mapping.
- Match the target layout using list_keyboard_layouts and set_keyboard_layout. Test harmless typing; leave OS layout unchanged unless asked.
- Use wait_for or poll fresh captures. Keep the working window visible; maximize unless asked otherwise.`

func (*toolServer) setup(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, error) {
	return textResult(setupChecklist), nil
}
