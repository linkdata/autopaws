package main

import (
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/apiwire"
)

func TestMappingToolsUseDaemonStateAndCaptureRevision(t *testing.T) {
	capture := sdkCapture{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
		reply := sdkDefaultReply(request, capture)
		if request.Method == "capture_frame" {
			reply.result.(map[string]any)["mapping_revision"] = 17
		}
		return reply
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{}, apiclient.New(fake.listener.Addr().String())))
	sdkTool(t, session, "get_mouse_mapping", map[string]any{}, false)
	sdkTool(t, session, "capture_screen", map[string]any{}, false)
	sdkTool(t, session, "mouse_move", map[string]any{"x": 2, "y": 1}, false)
	sdkTool(t, session, "mouse_click", map[string]any{"x": 4, "y": 2}, false)
	sdkTool(t, session, "mouse_drag", map[string]any{"start_x": 0, "start_y": 0, "end_x": 4, "end_y": 2}, false)
	sdkTool(t, session, "mouse_move", map[string]any{"x": -12, "y": 7, "relative": true}, false)
	sdkTool(t, session, "mouse_click", map[string]any{}, false)
	requests := fake.snapshot()
	for _, index := range []int{2, 3, 4} {
		var params map[string]any
		if err := json.Unmarshal(requests[index].Params, &params); err != nil {
			t.Fatal(err)
		}
		if params["mapping_revision"] != float64(17) {
			t.Fatalf("%s lost screenshot revision: %s", requests[index].Method, requests[index].Params)
		}
		if _, exists := params["offset"]; exists {
			t.Fatal("per-call offset leaked into mouse action")
		}
	}
	sdkEqualParams(t, requests[2], map[string]any{"x": 4, "y": 3, "mapping_revision": 17})
	sdkEqualParams(t, requests[5], map[string]any{"x": -12, "y": 7, "relative": true})
	sdkEqualParams(t, requests[6], map[string]any{})
}

func TestMappingChangesRequireNewScreenshotEvenWhenRejected(t *testing.T) {
	for _, method := range []string{"set_mouse_mapping", "reset_mouse_mapping"} {
		for _, rejected := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/success", true: "/rejected"}[rejected], func(t *testing.T) {
				capture := sdkCapture{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
				fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
					if request.Method == method && rejected {
						return sdkReply{code: apiwire.ErrInvalidParams}
					}
					return sdkDefaultReply(request, capture)
				})
				session := sdkConnect(t, newServer(t.Context(), mcpOptions{}, apiclient.New(fake.listener.Addr().String())))
				sdkTool(t, session, "capture_screen", map[string]any{}, false)
				params := map[string]any{}
				if method == "set_mouse_mapping" {
					params["mode"] = "extended"
				}
				sdkTool(t, session, method, params, rejected)
				result := sdkTool(t, session, "mouse_move", map[string]any{"x": 0, "y": 0}, true)
				if !strings.Contains(sdkText(result), "capture_screen must succeed") {
					t.Fatalf("old screenshot survived mapping edit: %s", sdkText(result))
				}
				if len(fake.snapshot()) != 2 {
					t.Fatal("unpositioned state forwarded an absolute action")
				}
			})
		}
	}
}
