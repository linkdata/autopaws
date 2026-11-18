//go:build linux && (amd64 || arm64)

package main

import (
	"encoding/json"
	"testing"

	"github.com/linkdata/autopaws/ch9329"
)

func TestRealMCPKeyboardLayoutSwitchesWithinOneSession(t *testing.T) {
	fixture := newRealMCPFixture(t)
	var wantFrames [][]byte
	for _, tc := range []struct {
		layout, active string
		modifier       byte
		failed         bool
	}{
		{layout: "us104", active: "us104", modifier: 0x02},
		{layout: "se105", active: "se105", modifier: 0x40},
		{layout: "missing-layout", active: "se105", modifier: 0x40, failed: true},
		{layout: "us104", active: "us104", modifier: 0x02},
	} {
		_, preparesBefore, _ := fixture.transport.snapshot()
		reply := realMCPCall(fixture, "set_keyboard_layout", map[string]any{"layout": tc.layout})
		if reply.err != nil || reply.result == nil || reply.result.IsError != tc.failed {
			t.Fatalf("select %s: result=%+v, error=%v", tc.layout, reply.result, reply.err)
		}
		if !tc.failed {
			var selected struct {
				KeyboardLayout string `json:"keyboard_layout"`
			}
			if err := json.Unmarshal([]byte(realMCPResultText(reply.result)), &selected); err != nil || selected.KeyboardLayout != tc.active {
				t.Fatalf("select %s response=%s, error=%v", tc.layout, realMCPResultText(reply.result), err)
			}
		}
		info := realMCPCall(fixture, "get_device_info", map[string]any{})
		realMCPSuccess(t, info)
		var observed struct {
			Config struct {
				KeyboardLayout string `json:"keyboard_layout"`
			} `json:"config"`
		}
		if err := json.Unmarshal([]byte(realMCPResultText(info.result)), &observed); err != nil || observed.Config.KeyboardLayout != tc.active {
			t.Fatalf("active after selecting %s: info=%s, error=%v", tc.layout, realMCPResultText(info.result), err)
		}
		realMCPRequireFrames(t, fixture.transport, wantFrames...)
		if _, preparesAfter, _ := fixture.transport.snapshot(); preparesAfter != preparesBefore {
			t.Fatal("layout selection prepared hardware")
		}
		realMCPSuccess(t, realMCPCall(fixture, "type_text", map[string]any{"text": "@", "raw": true, "char_delay_ms": 0}))
		wantFrames = append(wantFrames, ch9329.Keyboard(tc.modifier, 0x1f), ch9329.Keyboard(0))
		realMCPRequireFrames(t, fixture.transport, wantFrames...)
	}
}
