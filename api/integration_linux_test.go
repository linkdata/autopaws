//go:build linux && (amd64 || arm64)

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linkdata/autopaws/api"
	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/hid"
)

type recordingTransport struct {
	mu       sync.Mutex
	prepares int
	frames   [][]byte
	closes   int
	hook     func(context.Context, []byte) error
}

func (transport *recordingTransport) Prepare(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.prepares++
	return 1, nil
}

func (transport *recordingTransport) WriteFrame(ctx context.Context, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	transport.mu.Lock()
	transport.frames = append(transport.frames, bytes.Clone(frame))
	hook := transport.hook
	transport.mu.Unlock()
	if hook != nil {
		return hook(ctx, frame)
	}
	return nil
}

func (transport *recordingTransport) Close() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.closes++
	return nil
}

func (transport *recordingTransport) snapshot() (int, [][]byte, int) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	frames := make([][]byte, len(transport.frames))
	for i, frame := range transport.frames {
		frames[i] = bytes.Clone(frame)
	}
	return transport.prepares, frames, transport.closes
}

func (transport *recordingTransport) setHook(hook func(context.Context, []byte) error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.hook = hook
}

type integrationFixture struct {
	client    *apiclient.Client
	keyboard  *hid.Keyboard
	transport *recordingTransport
}

func newIntegrationFixture(t *testing.T) integrationFixture {
	t.Helper()
	transport := &recordingTransport{}
	controller := hid.NewController(transport)
	layout, err := hid.LoadLayout("us104", "")
	if err != nil {
		t.Fatal(err)
	}
	keyboard := hid.NewKeyboard(controller, layout)
	mouse, err := hid.NewMouse(controller, hid.MouseOptions{Width: 1920, Height: 1080, ButtonMode: "relative"})
	if err != nil {
		t.Fatal(err)
	}
	server := api.New(keyboard, mouse, api.Options{
		SerialDevice: "/dev/fixture", SerialBaud: 9600,
		ScreenWidth: 1920, ScreenHeight: 1080,
		KeyboardLayout: "us104", ButtonMode: "relative",
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		if err := receive(t, served); err != nil {
			t.Errorf("Serve shutdown: %v", err)
		}
		if err := controller.Close(); err != nil {
			t.Errorf("controller close: %v", err)
		}
		drained := make(chan struct{})
		go func() {
			server.Wait()
			close(drained)
		}()
		receive(t, drained)
	})
	return integrationFixture{apiclient.New(listener.Addr().String()), keyboard, transport}
}

// These timeouts only make a broken loopback test fail promptly. They are not
// deadlines imposed on production HID actions or Linux driver calls.
func receive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for loopback test")
		var zero T
		return zero
	}
}

func call(t *testing.T, fixture integrationFixture, method, params string, result any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return fixture.client.Call(ctx, method, json.RawMessage(params), result)
}

func requireFrames(t *testing.T, transport *recordingTransport, want ...[]byte) {
	t.Helper()
	_, got, _ := transport.snapshot()
	if len(got) != len(want) {
		t.Fatalf("frames = %x; want %x", got, want)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("frame %d = %x; want %x (all frames %x)", i, got[i], want[i], got)
		}
	}
}

func TestClientServerM1RoundTrips(t *testing.T) {
	release := ch9329.Keyboard(0)
	for _, test := range []struct {
		name, method, params, result string
		frames                       [][]byte
	}{
		{"ping", "ping", `{}`, `{"pong":true}`, nil},
		{"text", "type_text", `{"text":"a{enter}","char_delay_ms":0,"raw":false}`, `{"chars_typed":8}`, [][]byte{
			ch9329.Keyboard(0, 4), release, ch9329.Keyboard(0, 0x28), release,
		}},
		{"raw_text", "type_text", `{"text":"{a}","char_delay_ms":0,"raw":true}`, `{"chars_typed":3}`, [][]byte{
			ch9329.Keyboard(2, 0x2f), release, ch9329.Keyboard(0, 4), release, ch9329.Keyboard(2, 0x30), release,
		}},
		{"empty_text", "type_text", `{"text":"","char_delay_ms":null,"raw":null}`, `{"chars_typed":0}`, nil},
		{"key", "send_key", `{"key":"a","modifiers":["ctrl"]}`, `{"sent":true}`, [][]byte{ch9329.Keyboard(1, 4), release}},
		{"sequence", "send_key_sequence", `{"steps":[{"key":"a","modifiers":["shift"],"delay_ms":0},{"key":"enter"}],"default_delay_ms":0}`, `{"steps_sent":2}`, [][]byte{
			ch9329.Keyboard(2, 4), release, ch9329.Keyboard(0, 0x28), release,
		}},
		{"empty_sequence", "send_key_sequence", `{"steps":[]}`, `{"steps_sent":0}`, nil},
		{"absolute_move", "mouse_move", `{"x":1919,"y":1079,"relative":null}`, `{"position":{"x":1919,"y":1079,"relative":false}}`, [][]byte{ch9329.MouseAbsolute(0, 4095, 4095, 0)}},
		{"relative_move", "mouse_move", `{"x":-127,"y":127,"relative":true}`, `{"position":{"x":-127,"y":127,"relative":true}}`, [][]byte{ch9329.MouseRelative(0, -127, 127, 0)}},
		{"click", "mouse_click", `{"button":null,"button_mode":null}`, `{"clicked":true}`, [][]byte{ch9329.MouseRelative(1, 0, 0, 0), ch9329.MouseRelative(0, 0, 0, 0)}},
		{"positioned_click_override", "mouse_click", `{"x":0,"y":0,"button_mode":"absolute"}`, `{"clicked":true}`, [][]byte{ch9329.MouseAbsolute(1, 0, 0, 0), ch9329.MouseAbsolute(0, 0, 0, 0)}},
		{"down", "mouse_down", `{"button":"right","x":0,"y":0}`, `{"pressed":true,"button":"right"}`, [][]byte{ch9329.MouseAbsolute(0, 0, 0, 0), ch9329.MouseRelative(2, 0, 0, 0)}},
		{"up", "mouse_up", `{"button":"right","x":1919,"y":1079,"button_mode":"absolute"}`, `{"released":true,"button":"right"}`, [][]byte{ch9329.MouseAbsolute(0, 4095, 4095, 0)}},
		{"scroll", "mouse_scroll", `{"amount":1000}`, `{"scrolled":true}`, [][]byte{ch9329.MouseRelative(0, 0, 0, 127)}},
		{"drag", "mouse_drag", `{"start_x":0,"start_y":0,"end_x":1919,"end_y":1079}`, `{"dragged":true}`, [][]byte{
			ch9329.MouseAbsolute(0, 0, 0, 0), ch9329.MouseRelative(1, 0, 0, 0),
			ch9329.MouseAbsolute(1, 4095, 4095, 0), ch9329.MouseAbsolute(0, 4095, 4095, 0), ch9329.MouseRelative(0, 0, 0, 0),
		}},
		{"device_info", "get_device_info", `{}`, `{"serial":{"device":"/dev/fixture","baud":9600,"auto_detect":false},"config":{"screen_width":1920,"screen_height":1080,"keyboard_layout":"us104","serial_baud":9600,"mouse_button_mode":"relative"},"capture":{"error":"unavailable"}}`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIntegrationFixture(t)
			var got, want map[string]any
			if err := json.Unmarshal([]byte(test.result), &want); err != nil {
				t.Fatal(err)
			}
			if err := call(t, fixture, test.method, test.params, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("result = %#v; want %#v", got, want)
			}
			requireFrames(t, fixture.transport, test.frames...)
			if prepares, _, _ := fixture.transport.snapshot(); len(test.frames) == 0 && prepares != 0 {
				t.Fatalf("non-input method prepared transport %d times", prepares)
			}
		})
	}
}

func TestClientServerMediaStubsUnavailable(t *testing.T) {
	fixture := newIntegrationFixture(t)
	for _, test := range []struct{ method, params string }{
		{"capture_frame", `{}`},
		{"list_capture_devices", `{}`},
		{"set_capture_device", `{"device":"/dev/video0"}`},
		{"set_capture_resolution", `{"width":1920,"height":1080}`},
	} {
		t.Run(test.method, func(t *testing.T) {
			if err := call(t, fixture, test.method, test.params, nil); !errors.Is(err, apiwire.ErrUnavailable) {
				t.Fatalf("error = %v; want unavailable", err)
			}
		})
	}
	if prepares, frames, _ := fixture.transport.snapshot(); prepares != 0 || len(frames) != 0 {
		t.Fatalf("media stubs touched input: prepares=%d frames=%x", prepares, frames)
	}
}

func TestClientServerPreservesHeldButtonsAcrossCalls(t *testing.T) {
	fixture := newIntegrationFixture(t)
	for _, action := range []struct{ method, params string }{
		{"mouse_down", `{"button":"right"}`},
		{"mouse_move", `{"x":1919,"y":1079}`},
		{"mouse_down", `{}`},
		{"mouse_up", `{"button":"right"}`},
		{"mouse_scroll", `{"amount":-1000}`},
		{"mouse_up", `{}`},
	} {
		if err := call(t, fixture, action.method, action.params, nil); err != nil {
			t.Fatalf("%s: %v", action.method, err)
		}
	}
	requireFrames(t, fixture.transport,
		ch9329.MouseRelative(2, 0, 0, 0),
		ch9329.MouseAbsolute(2, 4095, 4095, 0),
		ch9329.MouseRelative(3, 0, 0, 0),
		ch9329.MouseRelative(1, 0, 0, 0),
		ch9329.MouseRelative(1, 0, 0, -127),
		ch9329.MouseRelative(0, 0, 0, 0),
	)
}

func TestClientServerRejectsInvalidInputBeforePreparing(t *testing.T) {
	fixture := newIntegrationFixture(t)
	for _, test := range []struct {
		name, method, params string
		code                 apiwire.Code
	}{
		{"params_null", "send_key", `null`, apiwire.ErrInvalidParams},
		{"params_array", "send_key", `[]`, apiwire.ErrInvalidParams},
		{"unknown_param", "send_key", `{"key":"a","modifer":[]}`, apiwire.ErrInvalidParams},
		{"unknown_method", "does_not_exist", `{}`, apiwire.ErrUnknownMethod},
		{"missing_text", "type_text", `{}`, apiwire.ErrInvalidParams},
		{"null_text", "type_text", `{"text":null}`, apiwire.ErrInvalidParams},
		{"numeric_text", "type_text", `{"text":7}`, apiwire.ErrInvalidParams},
		{"late_unsupported_text", "type_text", `{"text":"ab🙂"}`, apiwire.ErrUnsupported},
		{"negative_char_delay", "type_text", `{"text":"a","char_delay_ms":-1}`, apiwire.ErrInvalidParams},
		{"large_char_delay", "type_text", `{"text":"a","char_delay_ms":60001}`, apiwire.ErrInvalidParams},
		{"float_char_delay", "type_text", `{"text":"a","char_delay_ms":1.0}`, apiwire.ErrInvalidParams},
		{"overflow_char_delay", "type_text", `{"text":"a","char_delay_ms":9223372036854775808}`, apiwire.ErrInvalidParams},
		{"wrong_raw_type", "type_text", `{"text":"a","raw":1}`, apiwire.ErrInvalidParams},
		{"missing_key", "send_key", `{}`, apiwire.ErrInvalidParams},
		{"null_key", "send_key", `{"key":null}`, apiwire.ErrInvalidParams},
		{"wrong_key_type", "send_key", `{"key":7}`, apiwire.ErrInvalidParams},
		{"unknown_key", "send_key", `{"key":"not-a-key"}`, apiwire.ErrInvalidParams},
		{"unknown_modifier", "send_key", `{"key":"a","modifiers":["oops"]}`, apiwire.ErrInvalidParams},
		{"wrong_modifiers_type", "send_key", `{"key":"a","modifiers":"ctrl"}`, apiwire.ErrInvalidParams},
		{"missing_steps", "send_key_sequence", `{}`, apiwire.ErrInvalidParams},
		{"null_steps", "send_key_sequence", `{"steps":null}`, apiwire.ErrInvalidParams},
		{"wrong_steps_type", "send_key_sequence", `{"steps":{}}`, apiwire.ErrInvalidParams},
		{"late_missing_step_key", "send_key_sequence", `{"steps":[{"key":"a"},{}]}`, apiwire.ErrInvalidParams},
		{"late_null_step_key", "send_key_sequence", `{"steps":[{"key":"a"},{"key":null}]}`, apiwire.ErrInvalidParams},
		{"late_unknown_step_key", "send_key_sequence", `{"steps":[{"key":"a"},{"key":"oops"}]}`, apiwire.ErrInvalidParams},
		{"late_negative_step_delay", "send_key_sequence", `{"steps":[{"key":"a"},{"key":"b","delay_ms":-1}]}`, apiwire.ErrInvalidParams},
		{"late_large_step_delay", "send_key_sequence", `{"steps":[{"key":"a"},{"key":"b","delay_ms":60001}]}`, apiwire.ErrInvalidParams},
		{"float_step_delay", "send_key_sequence", `{"steps":[{"key":"a","delay_ms":1.0}]}`, apiwire.ErrInvalidParams},
		{"negative_default_delay", "send_key_sequence", `{"steps":[{"key":"a"}],"default_delay_ms":-1}`, apiwire.ErrInvalidParams},
		{"large_default_delay", "send_key_sequence", `{"steps":[{"key":"a"}],"default_delay_ms":60001}`, apiwire.ErrInvalidParams},
		{"too_many_steps", "send_key_sequence", `{"steps":[` + strings.Repeat(`{"key":"a"},`, hid.MaxSequenceSteps) + `{"key":"b"}]}`, apiwire.ErrInvalidParams},
		{"missing_x", "mouse_move", `{"y":0}`, apiwire.ErrInvalidParams},
		{"missing_y", "mouse_move", `{"x":0}`, apiwire.ErrInvalidParams},
		{"null_x", "mouse_move", `{"x":null,"y":0}`, apiwire.ErrInvalidParams},
		{"float_x", "mouse_move", `{"x":1.0,"y":0}`, apiwire.ErrInvalidParams},
		{"string_y", "mouse_move", `{"x":0,"y":"0"}`, apiwire.ErrInvalidParams},
		{"overflow_x", "mouse_move", `{"x":9223372036854775808,"y":0}`, apiwire.ErrInvalidParams},
		{"negative_absolute", "mouse_move", `{"x":-1,"y":0}`, apiwire.ErrInvalidParams},
		{"outside_absolute", "mouse_move", `{"x":1920,"y":0}`, apiwire.ErrInvalidParams},
		{"outside_relative", "mouse_move", `{"x":128,"y":0,"relative":true}`, apiwire.ErrInvalidParams},
		{"wrong_relative_type", "mouse_move", `{"x":0,"y":0,"relative":"false"}`, apiwire.ErrInvalidParams},
		{"partial_click_position", "mouse_click", `{"x":0}`, apiwire.ErrInvalidParams},
		{"part_null_click_position", "mouse_click", `{"x":0,"y":null}`, apiwire.ErrInvalidParams},
		{"outside_click_position", "mouse_click", `{"x":0,"y":1080}`, apiwire.ErrInvalidParams},
		{"unknown_button", "mouse_click", `{"button":"other"}`, apiwire.ErrInvalidParams},
		{"wrong_button_type", "mouse_down", `{"button":1}`, apiwire.ErrInvalidParams},
		{"unknown_button_mode", "mouse_up", `{"button_mode":"other"}`, apiwire.ErrInvalidParams},
		{"missing_scroll", "mouse_scroll", `{}`, apiwire.ErrInvalidParams},
		{"null_scroll", "mouse_scroll", `{"amount":null}`, apiwire.ErrInvalidParams},
		{"float_scroll", "mouse_scroll", `{"amount":1.0}`, apiwire.ErrInvalidParams},
		{"missing_drag_end", "mouse_drag", `{"start_x":0,"start_y":0,"end_x":1}`, apiwire.ErrInvalidParams},
		{"null_drag_start", "mouse_drag", `{"start_x":null,"start_y":0,"end_x":1,"end_y":1}`, apiwire.ErrInvalidParams},
		{"late_invalid_drag_endpoint", "mouse_drag", `{"start_x":0,"start_y":0,"end_x":1,"end_y":1080}`, apiwire.ErrInvalidParams},
		{"unknown_drag_button", "mouse_drag", `{"start_x":0,"start_y":0,"end_x":1,"end_y":1,"button":"other"}`, apiwire.ErrInvalidParams},
		{"missing_capture_device", "set_capture_device", `{}`, apiwire.ErrInvalidParams},
		{"null_capture_device", "set_capture_device", `{"device":null}`, apiwire.ErrInvalidParams},
		{"numeric_capture_device", "set_capture_device", `{"device":"-1"}`, apiwire.ErrInvalidParams},
		{"missing_capture_dimension", "set_capture_resolution", `{"width":1}`, apiwire.ErrInvalidParams},
		{"null_capture_dimension", "set_capture_resolution", `{"width":1,"height":null}`, apiwire.ErrInvalidParams},
		{"float_capture_dimension", "set_capture_resolution", `{"width":1.0,"height":1}`, apiwire.ErrInvalidParams},
		{"zero_capture_dimension", "set_capture_resolution", `{"width":0,"height":1}`, apiwire.ErrInvalidParams},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := call(t, fixture, test.method, test.params, nil); !errors.Is(err, test.code) {
				t.Fatalf("error = %v; want %v", err, test.code)
			}
			if prepares, frames, _ := fixture.transport.snapshot(); prepares != 0 || len(frames) != 0 {
				t.Fatalf("invalid request touched input: prepares=%d frames=%x", prepares, frames)
			}
		})
	}
}

func TestClientServerActionsShareTheControllerGate(t *testing.T) {
	release := ch9329.Keyboard(0)
	for _, test := range []struct {
		method, params string
		frames         [][]byte
	}{
		{"type_text", `{"text":"ab","char_delay_ms":0}`, [][]byte{ch9329.Keyboard(0, 4), release, ch9329.Keyboard(0, 5), release}},
		{"send_key_sequence", `{"steps":[{"key":"a"},{"key":"b"}],"default_delay_ms":0}`, [][]byte{ch9329.Keyboard(0, 4), release, ch9329.Keyboard(0, 5), release}},
		{"mouse_drag", `{"start_x":0,"start_y":0,"end_x":1919,"end_y":1079}`, [][]byte{
			ch9329.MouseAbsolute(0, 0, 0, 0), ch9329.MouseRelative(1, 0, 0, 0), ch9329.MouseAbsolute(1, 4095, 4095, 0),
			ch9329.MouseAbsolute(0, 4095, 4095, 0), ch9329.MouseRelative(0, 0, 0, 0),
		}},
	} {
		t.Run(test.method, func(t *testing.T) {
			fixture := newIntegrationFixture(t)
			firstFrame, proceed := make(chan struct{}), make(chan struct{})
			var first sync.Once
			fixture.transport.setHook(func(ctx context.Context, _ []byte) error {
				var err error
				first.Do(func() {
					close(firstFrame)
					select {
					case <-proceed:
					case <-ctx.Done():
						err = ctx.Err()
					}
				})
				return err
			})
			apiDone := make(chan error, 1)
			go func() { apiDone <- call(t, fixture, test.method, test.params, nil) }()
			receive(t, firstFrame)
			directStarted, directDone := make(chan struct{}), make(chan error, 1)
			go func() {
				close(directStarted)
				directDone <- fixture.keyboard.SendKey(context.Background(), "z", nil)
			}()
			receive(t, directStarted)
			select {
			case err := <-directDone:
				t.Fatalf("second public caller completed during API action: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			requireFrames(t, fixture.transport, test.frames[0])
			close(proceed)
			if err := receive(t, apiDone); err != nil {
				t.Fatal(err)
			}
			if err := receive(t, directDone); err != nil {
				t.Fatal(err)
			}
			want := append(append([][]byte(nil), test.frames...), ch9329.Keyboard(0, 0x1d), release)
			requireFrames(t, fixture.transport, want...)
			if prepares, _, _ := fixture.transport.snapshot(); prepares != 2 {
				t.Fatalf("prepared %d times; want one preparation per complete action", prepares)
			}
		})
	}
}

func TestClientCancellationReleasesBeforeNextAction(t *testing.T) {
	fixture := newIntegrationFixture(t)
	press := ch9329.Keyboard(0, 4)
	release := ch9329.Keyboard(0)
	pressed, releasing, finishRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var cleanup sync.Once
	fixture.transport.setHook(func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, press) {
			close(pressed)
			<-ctx.Done()
			return ctx.Err()
		}
		var err error
		if bytes.Equal(frame, release) {
			cleanup.Do(func() {
				close(releasing)
				select {
				case <-finishRelease:
				case <-ctx.Done():
					err = ctx.Err()
				}
			})
		}
		return err
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan error, 1)
	go func() {
		canceled <- fixture.client.Call(ctx, "type_text", json.RawMessage(`{"text":"ab","char_delay_ms":0}`), nil)
	}()
	receive(t, pressed)
	cancel()
	if err := receive(t, canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Call error = %v; want context.Canceled", err)
	}
	receive(t, releasing)
	next := make(chan error, 1)
	go func() { next <- call(t, fixture, "send_key", `{"key":"c"}`, nil) }()
	select {
	case err := <-next:
		t.Fatalf("next action completed before cancellation cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	requireFrames(t, fixture.transport, press, release)
	close(finishRelease)
	if err := receive(t, next); err != nil {
		t.Fatal(err)
	}
	requireFrames(t, fixture.transport, press, release, ch9329.Keyboard(0, 6), release)
	if prepares, _, closes := fixture.transport.snapshot(); prepares != 3 || closes != 0 {
		t.Fatalf("prepares=%d closes=%d; want action, cleanup, next action without permanent close", prepares, closes)
	}
}
