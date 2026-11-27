package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type unitCaller func(context.Context, string, any, any) error

func (caller unitCaller) Call(ctx context.Context, method string, input, output any) error {
	return caller(ctx, method, input, output)
}

func unitAssign(output, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, output)
}

func unitJPEG(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := jpeg.Encode(&output, image.NewRGBA(image.Rect(0, 0, 5, 3)), nil); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestCaptureValidatesBeforeCommitting(t *testing.T) {
	jpegData := unitJPEG(t)
	failure := errors.New("API unavailable")
	for _, test := range []struct {
		name            string
		frame           any
		frameErr        error
		info            any
		infoErr         error
		cancelAfterInfo bool
	}{
		{name: "frame API failure", frameErr: failure},
		{name: "missing dimensions", frame: map[string]any{"jpeg_b64": jpegData}},
		{name: "invalid JPEG", frame: map[string]any{"jpeg_b64": []byte("not JPEG"), "width": 5, "height": 3}},
		{name: "truncated JPEG header", frame: map[string]any{"jpeg_b64": jpegData[:30], "width": 5, "height": 3}},
		{name: "metadata mismatch", frame: map[string]any{"jpeg_b64": jpegData, "width": 4, "height": 3}},
		{name: "info API failure", infoErr: failure},
		{name: "invalid target width", info: map[string]any{"config": map[string]int{"screen_width": 0, "screen_height": 1080}}},
		{name: "invalid target height", info: map[string]any{"config": map[string]int{"screen_width": 1920, "screen_height": -1}}},
		{name: "canceled capture", cancelAfterInfo: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			frame := test.frame
			if frame == nil {
				frame = map[string]any{"jpeg_b64": jpegData, "width": 5, "height": 3}
			}
			owner := &toolServer{caller: unitCaller(func(_ context.Context, method string, _, output any) error {
				if method == "capture_frame" {
					if test.frameErr != nil {
						return test.frameErr
					}
					return unitAssign(output, frame)
				}
				if method != "get_device_info" {
					t.Fatalf("unexpected API method %q", method)
				}
				if test.infoErr != nil {
					return test.infoErr
				}
				info := test.info
				if info == nil {
					info = map[string]any{"config": map[string]int{"screen_width": 1920, "screen_height": 1080}}
				}
				if test.cancelAfterInfo {
					cancel()
				}
				return unitAssign(output, info)
			})}
			if _, err := owner.capture(ctx, "capture_screen"); err == nil {
				t.Fatal("invalid/canceled capture succeeded")
			}
		})
	}
}

func TestCaptureUsesSnapshotTargetDimensionsAndLegacyFallback(t *testing.T) {
	jpegData := unitJPEG(t)
	for _, legacy := range []bool{false, true} {
		name := "snapshot dimensions"
		if legacy {
			name = "legacy fallback"
		}
		t.Run(name, func(t *testing.T) {
			var calls []string
			owner := &toolServer{caller: unitCaller(func(_ context.Context, method string, _, output any) error {
				calls = append(calls, method)
				if method == "capture_frame" {
					frame := map[string]any{"jpeg_b64": jpegData, "width": 5, "height": 3}
					if !legacy {
						frame["target_width"], frame["target_height"] = 1920, 1080
					}
					return unitAssign(output, frame)
				}
				if method != "get_device_info" {
					t.Fatalf("unexpected API method %q", method)
				}
				return unitAssign(output, map[string]any{"config": map[string]int{"screen_width": 1920, "screen_height": 1080}})
			})}
			frame, err := owner.capture(t.Context(), "capture_screen")
			if err != nil || !bytes.Equal(frame.jpeg, jpegData) || frame.dimensions != (screenDimensions{5, 3, 1920, 1080, nil}) {
				t.Fatalf("capture = %+v, %v", frame, err)
			}
			want := []string{"capture_frame"}
			if legacy {
				want = append(want, "get_device_info")
			}
			if !slices.Equal(calls, want) {
				t.Fatalf("capture API calls = %v, want %v", calls, want)
			}
		})
	}
}

func TestCaptureRejectsMalformedSnapshotTargetDimensionsWithoutFallback(t *testing.T) {
	jpegData := unitJPEG(t)
	for _, test := range []struct {
		name   string
		fields map[string]any
	}{
		{"missing width", map[string]any{"target_height": 1080}},
		{"missing height", map[string]any{"target_width": 1920}},
		{"zero width", map[string]any{"target_width": 0, "target_height": 1080}},
		{"negative height", map[string]any{"target_width": 1920, "target_height": -1}},
		{"null dimensions", map[string]any{"target_width": nil, "target_height": nil}},
		{"null width", map[string]any{"target_width": nil, "target_height": 1080}},
		{"string width", map[string]any{"target_width": "1920", "target_height": 1080}},
		{"fractional height", map[string]any{"target_width": 1920, "target_height": 1080.5}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			owner := &toolServer{caller: unitCaller(func(_ context.Context, method string, _, output any) error {
				calls++
				if method != "capture_frame" {
					t.Fatalf("malformed snapshot triggered %q", method)
				}
				frame := map[string]any{"jpeg_b64": jpegData, "width": 5, "height": 3}
				for field, value := range test.fields {
					frame[field] = value
				}
				return unitAssign(output, frame)
			})}
			if _, err := owner.capture(t.Context(), "capture_screen"); err == nil {
				t.Fatal("malformed target dimensions accepted")
			}
			if calls != 1 {
				t.Fatalf("capture API calls = %d, want 1", calls)
			}
		})
	}
}

func TestCaptureSnapshotDimensionsPreserveCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	jpegData := unitJPEG(t)
	owner := &toolServer{caller: unitCaller(func(_ context.Context, method string, _, output any) error {
		if method != "capture_frame" {
			t.Fatalf("unexpected API method %q", method)
		}
		cancel()
		return unitAssign(output, map[string]any{"jpeg_b64": jpegData, "width": 5, "height": 3, "target_width": 1920, "target_height": 1080})
	})}
	if _, err := owner.capture(ctx, "capture_screen"); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture cancellation = %v", err)
	}
}

func TestCaptureHelperDoesNotCommitAndOldCaptureCannotRestoreState(t *testing.T) {
	jpegData := unitJPEG(t)
	state := &screenState{}
	owner := &toolServer{sessions: map[*mcp.ServerSession]*screenState{nil: state}}
	invalidate := false
	owner.caller = unitCaller(func(_ context.Context, method string, _, output any) error {
		if method == "capture_frame" {
			return unitAssign(output, map[string]any{"jpeg_b64": jpegData, "width": 5, "height": 3})
		}
		if invalidate {
			state.invalidate()
		}
		return unitAssign(output, map[string]any{"config": map[string]int{"screen_width": 1920, "screen_height": 1080}})
	})
	frame, err := owner.capture(context.Background(), "future_ocr")
	if err != nil || !bytes.Equal(frame.jpeg, jpegData) {
		t.Fatalf("capture helper=%+v,%v", frame, err)
	}
	if _, err := state.snapshot(); err == nil {
		t.Fatal("helper committed a screenshot it did not return")
	}
	invalidate = true
	if _, err := owner.captureScreen(context.Background(), &mcp.CallToolRequest{}, struct{}{}); err == nil {
		t.Fatal("configuration change during capture restored old screenshot state")
	}
	if _, err := state.snapshot(); err == nil {
		t.Fatal("failed commit populated screenshot state")
	}
	invalidate = false
	if _, err := owner.captureScreen(context.Background(), &mcp.CallToolRequest{}, struct{}{}); err != nil {
		t.Fatal(err)
	}
	// A log failure makes capture_screen fail and preserves the previous image.
	old, _ := state.snapshot()
	logFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(logFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	owner.options.CaptureLogDir = logFile
	if _, err := owner.captureScreen(context.Background(), &mcp.CallToolRequest{}, struct{}{}); err == nil {
		t.Fatal("log failure was suppressed")
	}
	if got, err := state.snapshot(); err != nil || got != old {
		t.Fatalf("failed capture changed prior successful snapshot: %+v,%v", got, err)
	}
}
