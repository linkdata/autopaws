//go:build (linux || darwin || windows) && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net"
	"slices"
	"sync"
	"testing"

	"github.com/linkdata/autopaws/api"
	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/v4l2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type cropClickTransport struct {
	mu     sync.Mutex
	frames [][]byte
}

func (*cropClickTransport) Prepare(ctx context.Context) (uint64, error) { return 1, ctx.Err() }
func (*cropClickTransport) Close() error                                { return nil }

func (transport *cropClickTransport) WriteFrame(ctx context.Context, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.frames = append(transport.frames, bytes.Clone(frame))
	return nil
}

type cropClickSource struct{ frames <-chan v4l2.Frame }

func (*cropClickSource) Format() v4l2.Format {
	return v4l2.Format{Width: 128, Height: 96, FourCC: v4l2.PixFmtMJPEG}
}

func (source *cropClickSource) ReadFrame(ctx context.Context) (v4l2.Frame, error) {
	select {
	case frame := <-source.frames:
		return frame, nil
	case <-ctx.Done():
		return v4l2.Frame{}, ctx.Err()
	}
}

func (*cropClickSource) Close() error { return nil }

func TestCaptureCropPreservesMCPClickCoordinates(t *testing.T) {
	for _, test := range []struct {
		name         string
		content      image.Rectangle
		screenHeight int
		hidY         int
	}{
		{"dark screen edges", image.Rect(16, 8, 112, 88), 96, 1035},
		{"real letterbox", image.Rect(0, 12, 128, 84), 72, 1384},
	} {
		t.Run(test.name, func(t *testing.T) {
			img := image.NewGray(image.Rect(0, 0, 128, 96))
			draw.Draw(img, img.Bounds(), image.NewUniform(color.Gray{Y: 10}), image.Point{}, draw.Src)
			draw.Draw(img, test.content, image.NewUniform(color.Gray{Y: 220}), image.Point{}, draw.Src)
			var encoded bytes.Buffer
			if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 100}); err != nil {
				t.Fatal(err)
			}
			frames := make(chan v4l2.Frame, 1)
			frames <- v4l2.Frame{Data: encoded.Bytes(), Timestamp: v4l2.Timestamp{Microseconds: 1}, Flags: v4l2.TimestampMonotonic}
			manager, err := capture.New(capture.Options{
				Device: "test-capture", Width: 128, Height: 96, Autocrop: true,
				ScreenWidth: 128, ScreenHeight: test.screenHeight,
				MonotonicNow: func() (int64, error) { return 1, nil },
				OpenSource: func(context.Context, string, v4l2.Options) (capture.Source, error) {
					return &cropClickSource{frames: frames}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := manager.Close(); err != nil {
					t.Error(err)
				}
			})
			transport := &cropClickTransport{}
			controller := hid.NewController(transport)
			layout, err := hid.LoadLayout("us104", "")
			if err != nil {
				t.Fatal(err)
			}
			mouse, err := hid.NewMouse(controller, hid.MouseOptions{Width: 128, Height: test.screenHeight, ButtonMode: "relative"})
			if err != nil {
				t.Fatal(err)
			}
			server := api.New(hid.NewKeyboard(controller, layout), mouse, api.Options{
				ScreenWidth: 128, ScreenHeight: test.screenHeight, Capture: manager,
			})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx, listener) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
				if err := controller.Close(); err != nil {
					t.Error(err)
				}
				server.Wait()
			})
			session := sdkConnect(t, newServer(ctx, mcpOptions{Version: "test"}, apiclient.New(listener.Addr().String())))
			result := sdkTool(t, session, "capture_screen", nil, false)
			screenshot, ok := result.Content[0].(*mcp.ImageContent)
			if !ok {
				t.Fatalf("capture_screen returned %T instead of an image", result.Content[0])
			}
			config, err := jpeg.DecodeConfig(bytes.NewReader(screenshot.Data))
			if err != nil {
				t.Fatal(err)
			}
			if config.Width != 128 || config.Height != test.screenHeight {
				t.Errorf("screenshot = %dx%d, want 128x%d", config.Width, config.Height, test.screenHeight)
			}
			sdkTool(t, session, "mouse_click", map[string]any{"x": 32, "y": 24}, false)
			want := [][]byte{
				ch9329.MouseAbsolute(0, 1032, test.hidY, 0), ch9329.MouseRelative(1, 0, 0, 0),
				ch9329.MouseAbsolute(0, 1032, test.hidY, 0), ch9329.MouseRelative(0, 0, 0, 0),
			}
			transport.mu.Lock()
			defer transport.mu.Unlock()
			if !slices.EqualFunc(transport.frames, want, bytes.Equal) {
				t.Fatalf("click HID reports = %x, want %x", transport.frames, want)
			}
		})
	}
}
