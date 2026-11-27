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
	if words := len(strings.Fields(text)); words > 140 {
		t.Errorf("setup checklist too long: %d words", words)
	}
	for _, required := range []string{
		"get_device_info", "get_mouse_mapping", "capture_screen", "two separated image points",
		"real target cursor", "before clicking", "wait_for", "poll fresh captures", "stop positioned actions", "mirror/reposition",
		"captured offset", "absolute input bounds", "set_mouse_mapping",
		"list_keyboard_layouts", "set_keyboard_layout", "unless asked", "harmless sample",
		"window visible", "Maximize", "unless the user requests another layout", "repeat after target/display/layout changes",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("setup checklist missing %q", required)
		}
	}
}
