//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/hid"
)

func TestMappingStateSharedAcrossAPIConnections(t *testing.T) {
	server, controller, transport := bareServer(t, Options{})
	address, _ := serveForTest(t, server, controller)
	call := func(method, input string) hid.MappingState {
		t.Helper()
		conn := dialTest(t, address)
		writeTest(t, conn, `{"id":1,"method":"`+method+`","params":`+input+"}\n")
		reply := replyTest(t, conn)
		if !reply.OK {
			t.Fatalf("%s: %s %s", method, reply.Error, reply.Detail)
		}
		var state hid.MappingState
		if err := json.Unmarshal(reply.Result, &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	initial := call("get_mouse_mapping", `{}`)
	if initial.Mode != "legacy" || !initial.Ready {
		t.Fatalf("initial mapping=%+v", initial)
	}
	changed := call("set_mouse_mapping", `{"mode":"extended"}`)
	if changed.Mode != "extended" || changed.Ready || changed.Revision <= initial.Revision {
		t.Fatalf("extended mapping=%+v", changed)
	}
	if got := call("get_mouse_mapping", `{}`); got.Revision != changed.Revision || got.Mode != "extended" {
		t.Fatalf("mapping did not survive connection: %+v", got)
	}
	configured := call("set_mouse_mapping", `{"geometry":{"offset":{"x":-1920,"y":-1080},"width":1920,"height":1080,"span":{"x":-1920,"y":-1080,"width":3840,"height":2160}}}`)
	if !configured.Ready || configured.Geometry == nil || configured.Geometry.Offset != (hid.Point{X: -1920, Y: -1080}) {
		t.Fatalf("complete signed geometry did not activate immediately: %+v", configured)
	}
	reset := call("reset_mouse_mapping", `{}`)
	if reset.Mode != "extended" || reset.Ready || reset.Geometry != nil {
		t.Fatalf("reset silently restored legacy: %+v", reset)
	}
	if transport.prepares != 0 || transport.writes != 0 {
		t.Fatal("unheld mapping configuration opened hardware")
	}
}

func TestMappingJSONRequiresExplicitOrigins(t *testing.T) {
	const complete = `{"offset":{"x":0,"y":0},"width":1920,"height":1080,"span":{"x":0,"y":0,"width":3840,"height":1200}}`
	for _, path := range []string{"offset", "offset.x", "offset.y", "span", "span.x", "span.y", "extra", "offset.extra", "span.extra"} {
		for _, null := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "/omitted", true: "/null"}[null], func(t *testing.T) {
				var geometry map[string]any
				if err := json.Unmarshal([]byte(complete), &geometry); err != nil {
					t.Fatal(err)
				}
				fields := strings.Split(path, ".")
				container := geometry
				if len(fields) == 2 {
					container = geometry[fields[0]].(map[string]any)
				}
				key := fields[len(fields)-1]
				if key == "extra" {
					container[key] = 0
				} else if null {
					container[key] = nil
				} else {
					delete(container, key)
				}
				data, _ := json.Marshal(map[string]any{"geometry": geometry})
				server, _, transport := bareServer(t, Options{})
				before := server.mouse.Mapping()
				if _, err := server.setMouseMapping(t.Context(), data); !errors.Is(err, apiwire.ErrInvalidParams) {
					t.Fatalf("incomplete/unknown geometry accepted: %s: %v", data, err)
				}
				if after := server.mouse.Mapping(); after.Revision != before.Revision || after.Ready != before.Ready || transport.prepares != 0 || transport.writes != 0 {
					t.Fatal("rejected geometry changed mapping or touched HID")
				}
			})
		}
	}
	server, _, transport := bareServer(t, Options{})
	if _, err := server.setMouseMapping(t.Context(), json.RawMessage(`{"geometry":`+complete+`}`)); err != nil || !server.mouse.Mapping().Ready || transport.prepares != 0 || transport.writes != 0 {
		t.Fatalf("explicit zero origins rejected or opened HID: %v", err)
	}
}

func TestRejectedCaptureSettersPreserveReadyGeometry(t *testing.T) {
	for _, test := range []struct {
		name, method, params string
		closed, unselected   bool
	}{
		{"zero resolution", "set_capture_resolution", `{"width":0,"height":0}`, false, false},
		{"numeric device", "set_capture_device", `{"device":"0"}`, false, false},
		{"closed device", "set_capture_device", `{"device":"/dev/second"}`, true, false},
		{"closed resolution", "set_capture_resolution", `{"width":1280,"height":720}`, true, false},
		{"unselected resolution", "set_capture_resolution", `{"width":1280,"height":720}`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			device := "/dev/first"
			if test.unselected {
				device = ""
			}
			manager, err := capture.New(capture.Options{Device: device, Width: 1920, Height: 1080})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Close() })
			if test.closed {
				_ = manager.Close()
			}
			server, _, transport := bareServer(t, Options{Capture: manager})
			geometry := hid.MappingGeometry{Width: 1920, Height: 1080, Span: hid.MappingSpan{Width: 3840, Height: 1200}}
			if err := server.mouse.SetMapping(t.Context(), hid.MappingSet{Geometry: &geometry}); err != nil {
				t.Fatal(err)
			}
			before := server.mouse.Mapping()
			if _, err := server.handlers[test.method](t.Context(), json.RawMessage(test.params)); err == nil {
				t.Fatal("unavailable/invalid capture setter accepted")
			}
			after := server.mouse.Mapping()
			if !after.Ready || after.Revision != before.Revision || after.Geometry == nil || *after.Geometry != *before.Geometry || transport.prepares != 0 || transport.writes != 0 {
				t.Fatalf("rejected capture request changed mapping or touched HID: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestMappingInvalidInputAndStalePositionDoNotWrite(t *testing.T) {
	for _, test := range []struct{ method, input string }{
		{"set_mouse_mapping", `{"mode":"guess"}`},
		{"set_mouse_mapping", `{"strategy":"relative"}`},
		{"set_mouse_mapping", `{"tolerance":4}`},
		{"set_mouse_mapping", `{"offset":{"x":1,"y":2}}`},
		{"set_mouse_mapping", `{"geometry":{"offset":{"x":1,"y":2}}}`},
	} {
		t.Run(test.method+test.input, func(t *testing.T) {
			server, _, transport := bareServer(t, Options{})
			if _, err := server.handlers[test.method](t.Context(), json.RawMessage(test.input)); err == nil {
				t.Fatal("invalid mapping input accepted")
			}
			if transport.prepares != 0 || transport.writes != 0 {
				t.Fatal("invalid mapping input touched hardware")
			}
		})
	}
	server, _, transport := bareServer(t, Options{})
	before := server.mouse.Mapping().Revision
	if err := server.mouse.SetMapping(t.Context(), hid.MappingSet{Mode: "legacy"}); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(map[string]any{"x": 10, "y": 10, "mapping_revision": before})
	if _, err := server.handlers["mouse_move"](context.Background(), request); !errors.Is(err, hid.ErrMappingChanged) {
		t.Fatalf("stale position error=%v", err)
	}
	if transport.prepares != 0 || transport.writes != 0 {
		t.Fatal("stale coordinates touched input")
	}
	for _, test := range []struct {
		err  error
		code apiwire.Code
	}{{hid.ErrMappingChanged, apiwire.ErrMappingChanged}, {hid.ErrMappingUnavailable, apiwire.ErrMappingUnavailable}, {hid.ErrMapping, apiwire.ErrInvalidParams}} {
		if code, _ := classifyError(test.err); code != test.code {
			t.Fatalf("mapping error classified as %s, want %s", code, test.code)
		}
	}
}
