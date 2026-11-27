//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/hid"
)

type modeAdapter struct {
	mode     ch9329.USBMode
	err      error
	ctx      context.Context
	gets     int
	setModes []byte
}

func (adapter *modeAdapter) GetUSBMode(ctx context.Context) (ch9329.USBMode, error) {
	adapter.gets++
	adapter.ctx = ctx
	if err := ctx.Err(); err != nil {
		return ch9329.USBMode{}, err
	}
	return adapter.mode, adapter.err
}

func (adapter *modeAdapter) SetUSBMode(ctx context.Context, raw byte) (ch9329.USBMode, error) {
	adapter.setModes = append(adapter.setModes, raw)
	adapter.ctx = ctx
	return adapter.mode, adapter.err
}

func TestAdapterUSBMode(t *testing.T) {
	want := ch9329.USBMode{RawMode: 128, Mode: 0, Source: "hardware_pins", FirmwareVersion: 56, USBEnumerated: true}
	adapter := &modeAdapter{mode: want}
	server, _, _ := bareServer(t, Options{Adapter: adapter})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	before := server.mouse.Mapping()
	result, err := server.handlers["get_adapter_usb_mode"](ctx, json.RawMessage(`{}`))
	if err != nil || result != want || adapter.gets != 1 || adapter.ctx != ctx {
		t.Fatalf("get = %v, %v; calls=%d context=%v", result, err, adapter.gets, adapter.ctx)
	}
	if !reflect.DeepEqual(server.mouse.Mapping(), before) {
		t.Fatal("reading USB mode changed display mapping")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]any{
		"raw_mode": float64(128), "mode": float64(0), "source": "hardware_pins",
		"firmware_version_byte": float64(56), "usb_enumerated": true,
	} {
		if wire[field] != value {
			t.Errorf("response %s = %v; want %v", field, wire[field], value)
		}
	}
	for index, test := range []struct {
		raw   byte
		input string
	}{
		{0, `{"raw_mode":0}`},
		{2, `{"raw_mode":2}`},
		{128, `{"raw_mode":128}`},
		{130, `{"raw_mode":130}`},
	} {
		before := server.mouse.Mapping()
		result, err := server.handlers["set_adapter_usb_mode"](ctx, json.RawMessage(test.input))
		if len(adapter.setModes) != index+1 {
			t.Fatalf("set %d did not reach the adapter exactly once: %v", test.raw, adapter.setModes)
		}
		if err != nil || result != want || adapter.ctx != ctx || adapter.setModes[len(adapter.setModes)-1] != test.raw {
			t.Fatalf("set %d = %v, %v; modes=%v", test.raw, result, err, adapter.setModes)
		}
		if got := server.mouse.Mapping(); got.Revision <= before.Revision || !got.Ready {
			t.Fatalf("legacy mapping not invalidated: before%+v after%+v", before, got)
		}
	}
}

func TestAdapterUSBModeRejectsInvalidParamsBeforeIO(t *testing.T) {
	adapter := &modeAdapter{}
	server, _, transport := bareServer(t, Options{Adapter: adapter})
	before := server.mouse.Mapping()
	for _, test := range []struct{ method, input string }{
		{"get_adapter_usb_mode", `{"raw_mode":2}`},
		{"set_adapter_usb_mode", `{}`},
		{"set_adapter_usb_mode", `{"raw_mode":null}`},
		{"set_adapter_usb_mode", `{"raw_mode":"2"}`},
		{"set_adapter_usb_mode", `{"raw_mode":true}`},
		{"set_adapter_usb_mode", `{"raw_mode":2.5}`},
		{"set_adapter_usb_mode", `{"raw_mode":-1}`},
		{"set_adapter_usb_mode", `{"raw_mode":1}`},
		{"set_adapter_usb_mode", `{"raw_mode":3}`},
		{"set_adapter_usb_mode", `{"raw_mode":129}`},
		{"set_adapter_usb_mode", `{"raw_mode":131}`},
		{"set_adapter_usb_mode", `{"raw_mode":256}`},
		{"set_adapter_usb_mode", `{"raw_mode":999999999999999999999}`},
		{"set_adapter_usb_mode", `{"raw_mode":2,"extra":true}`},
	} {
		_, err := server.handlers[test.method](t.Context(), json.RawMessage(test.input))
		if !errors.Is(err, apiwire.ErrInvalidParams) {
			t.Errorf("%s %s accepted: %v", test.method, test.input, err)
		}
	}
	if adapter.gets != 0 || len(adapter.setModes) != 0 || transport.prepares != 0 || transport.writes != 0 || !reflect.DeepEqual(server.mouse.Mapping(), before) {
		t.Fatal("invalid parameters touched hardware or display mapping")
	}
}

func TestAdapterUSBModeUnavailable(t *testing.T) {
	server, _, transport := bareServer(t, Options{})
	before := server.mouse.Mapping()
	for _, test := range []struct{ method, input string }{
		{"get_adapter_usb_mode", `{}`}, {"set_adapter_usb_mode", `{"raw_mode":2}`},
	} {
		if _, err := server.handlers[test.method](t.Context(), json.RawMessage(test.input)); !errors.Is(err, apiwire.ErrUnavailable) {
			t.Errorf("%s without adapter: %v", test.method, err)
		}
	}
	if transport.prepares != 0 || transport.writes != 0 || !reflect.DeepEqual(server.mouse.Mapping(), before) {
		t.Fatal("unavailable capability touched hardware or display mapping")
	}
}

func TestAdapterUSBModeFailureAndCancellation(t *testing.T) {
	failure := errors.New("adapter command not acknowledged")
	for _, test := range []struct{ method, input string }{
		{"get_adapter_usb_mode", `{}`}, {"set_adapter_usb_mode", `{"raw_mode":2}`},
	} {
		t.Run(test.method, func(t *testing.T) {
			adapter := &modeAdapter{err: failure}
			server, _, _ := bareServer(t, Options{Adapter: adapter})
			if err := server.mouse.SetMapping(t.Context(), hid.MappingSet{Geometry: &hid.MappingGeometry{
				Width: 1920, Height: 1080, Span: hid.MappingSpan{Width: 1920, Height: 1080},
			}}); err != nil {
				t.Fatal(err)
			}
			before := server.mouse.Mapping()
			result, err := server.handlers[test.method](t.Context(), json.RawMessage(test.input))
			if result != nil || !errors.Is(err, failure) {
				t.Fatalf("backend failure lost: %v, %v", result, err)
			}
			if code, _ := classifyError(err); code != apiwire.ErrUnavailable {
				t.Fatalf("backend failure code = %v", code)
			}
			after := server.mouse.Mapping()
			if test.method == "set_adapter_usb_mode" {
				if after.Revision <= before.Revision || after.Ready {
					t.Fatal("failed setter did not invalidate extended mapping")
				}
			} else if !reflect.DeepEqual(after, before) {
				t.Fatal("failed getter changed display mapping")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := server.handlers[test.method](ctx, json.RawMessage(test.input)); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if test.method == "set_adapter_usb_mode" && len(adapter.setModes) != 1 {
				t.Fatal("already canceled setter reached adapter")
			}
		})
	}
}

func TestAdapterUSBModeStopsAfterMappingReleaseFailure(t *testing.T) {
	adapter := &modeAdapter{}
	server, _, transport := bareServer(t, Options{Adapter: adapter})
	if err := server.mouse.MoveRel(t.Context(), 0, 0); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("release unavailable")
	transport.prepare = func(context.Context) error { return failure }
	result, err := server.handlers["set_adapter_usb_mode"](t.Context(), json.RawMessage(`{"raw_mode":2}`))
	if result != nil || !errors.Is(err, failure) || len(adapter.setModes) != 0 {
		t.Fatalf("setter passed failed release: %v, %v, modes=%v", result, err, adapter.setModes)
	}
}
