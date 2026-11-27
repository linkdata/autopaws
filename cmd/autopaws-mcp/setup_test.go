package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSetupReturnsChecklistWithoutCallingAPI(t *testing.T) {
	server := newServer(t.Context(), mcpOptions{}, unitCaller(func(context.Context, string, any, any) error {
		t.Error("setup called the KVM API")
		return errors.New("setup must remain local guidance")
	}))
	session := sdkConnect(t, server)
	if instructions := session.client.InitializeResult().Instructions; !strings.Contains(instructions, "Call setup once") {
		t.Fatalf("client did not receive setup instructions: %q", instructions)
	}
	result := sdkTool(t, session, "setup", map[string]any{}, false)
	text := sdkText(result)
	if words := len(strings.Fields(text)); words > 150 {
		t.Errorf("setup checklist too long: %d words", words)
	}
	for _, required := range []string{
		"get_device_info", "get_mouse_mapping", "capture_screen", "two separated image points",
		"real target cursor", "before clicking", "absolute screenshot coordinates",
		"Windows:", "Linux:", "relative button reports (default)", "macOS:", "first call mouse_move to the click point or drag start",
		"positioned mouse_click", "mouse_drag", "button_mode=absolute",
		"sustained drag/drop", "Stop if holds fail", "after relative movement", "restart KVM with -mouse-absolute-refresh",
		"wait_for", "poll fresh captures", "stop positioned actions", "primary/top-left", "reachable displays", "set_mouse_mapping",
		"list_keyboard_layouts", "set_keyboard_layout", "unless asked", "harmless typing",
		"window visible", "maximize", "repeat after target/display/layout changes",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("setup checklist missing %q", required)
		}
	}
	for _, omitted := range []string{"CH9329", "USB work mode", "serial protocol"} {
		if strings.Contains(text, omitted) {
			t.Errorf("setup checklist includes adapter configuration detail: %q", omitted)
		}
	}
}
