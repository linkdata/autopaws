//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/hid"
)

func TestEveryMethodRequiresAnObject(t *testing.T) {
	server, _, transport := bareServer(t, Options{})
	for name, method := range server.handlers {
		t.Run(name, func(t *testing.T) {
			if _, err := method(context.Background(), json.RawMessage(`[]`)); !errors.Is(err, apiwire.ErrInvalidParams) {
				t.Fatalf("array params accepted: %v", err)
			}
		})
	}
	if transport.prepares != 0 || transport.writes != 0 {
		t.Fatal("invalid parameter shapes touched input")
	}
}

func TestInputMethodsPreserveTransportFailure(t *testing.T) {
	failure := errors.New("serial device disconnected")
	for _, test := range []struct{ method, params string }{
		{"type_text", `{"text":"a"}`},
		{"send_key", `{"key":"a"}`},
		{"send_key_sequence", `{"steps":[{"key":"a"}]}`},
		{"mouse_move", `{"x":0,"y":0}`},
		{"mouse_click", `{}`},
		{"mouse_down", `{}`},
		{"mouse_up", `{}`},
		{"mouse_scroll", `{"amount":0}`},
		{"mouse_drag", `{"start_x":0,"start_y":0,"end_x":1,"end_y":1}`},
	} {
		t.Run(test.method, func(t *testing.T) {
			server, _, transport := bareServer(t, Options{})
			transport.prepare = func(context.Context) error { return failure }
			if result, err := server.handlers[test.method](context.Background(), json.RawMessage(test.params)); result != nil || !errors.Is(err, failure) {
				t.Fatalf("result=%v error=%v; want underlying transport failure", result, err)
			}
			if transport.prepares != 1 || transport.writes != 0 {
				t.Fatalf("failed preparation emitted input: prepares=%d writes=%d", transport.prepares, transport.writes)
			}
		})
	}
}

func TestAcceptedActionLimitsWithoutExecutingDelays(t *testing.T) {
	server, _, transport := bareServer(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct{ method, params string }{
		{"type_text", `{"text":"a","char_delay_ms":60000}`},
		{"send_key_sequence", `{"steps":[{"key":"a","delay_ms":60000}],"default_delay_ms":60000}`},
		{"send_key_sequence", `{"steps":[` + strings.Repeat(`{"key":"a"},`, hid.MaxSequenceSteps-1) + `{"key":"a"}]}`},
	} {
		if _, err := server.handlers[test.method](ctx, json.RawMessage(test.params)); !errors.Is(err, context.Canceled) {
			t.Errorf("%s rejected a supported numeric boundary before cancellation: %v", test.method, err)
		}
	}
	if transport.prepares != 0 || transport.writes != 0 {
		t.Fatal("already canceled boundary probes touched input")
	}
}

func TestCaptureStubParameterBoundaries(t *testing.T) {
	server, _, _ := bareServer(t, Options{})
	for _, test := range []struct {
		method, params string
		code           apiwire.Code
	}{
		{"set_capture_device", `{"device":"0"}`, apiwire.ErrInvalidParams},
		{"set_capture_device", `{"device":"+0"}`, apiwire.ErrInvalidParams},
		{"set_capture_device", `{"device":"-1"}`, apiwire.ErrInvalidParams},
		{"set_capture_device", `{"device":""}`, apiwire.ErrInvalidParams},
		{"set_capture_device", `{"device":"./0"}`, apiwire.ErrUnavailable},
		{"set_capture_device", `{"device":"/dev/video0"}`, apiwire.ErrUnavailable},
		{"set_capture_resolution", `{"width":4294967295,"height":1}`, apiwire.ErrUnavailable},
		{"set_capture_resolution", `{"width":4294967296,"height":1}`, apiwire.ErrInvalidParams},
		{"set_capture_resolution", `{"width":1,"height":-1}`, apiwire.ErrInvalidParams},
	} {
		if _, err := server.handlers[test.method](context.Background(), json.RawMessage(test.params)); !errors.Is(err, test.code) {
			t.Errorf("%s %s: %v; want %v", test.method, test.params, err, test.code)
		}
	}
}
