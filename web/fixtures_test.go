//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/v4l2"
)

type inputTransport struct {
	mu       sync.Mutex
	frames   [][]byte
	prepares int
	hook     func(context.Context, []byte) error
}

func (transport *inputTransport) Prepare(ctx context.Context) (uint64, error) {
	transport.mu.Lock()
	transport.prepares++
	transport.mu.Unlock()
	return 1, ctx.Err()
}

func (transport *inputTransport) WriteFrame(ctx context.Context, frame []byte) error {
	transport.mu.Lock()
	transport.frames = append(transport.frames, bytes.Clone(frame))
	hook := transport.hook
	transport.mu.Unlock()
	if hook != nil {
		return hook(ctx, frame)
	}
	return ctx.Err()
}

func (*inputTransport) Close() error { return nil }

func (transport *inputTransport) snapshot() (int, [][]byte) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.prepares, append([][]byte(nil), transport.frames...)
}

type frameSource struct {
	frames chan v4l2.Frame
	opened chan struct{}
	closed chan struct{}
}

func (source *frameSource) Format() v4l2.Format {
	return v4l2.Format{Width: 128, Height: 96, FourCC: v4l2.PixFmtMJPEG}
}

func (source *frameSource) ReadFrame(ctx context.Context) (v4l2.Frame, error) {
	select {
	case frame := <-source.frames:
		return frame, nil
	case <-ctx.Done():
		return v4l2.Frame{}, ctx.Err()
	}
}

func (source *frameSource) Close() error { close(source.closed); return nil }

type fixture struct {
	server     *Server
	controller *hid.Controller
	mouse      *hid.Mouse
	capture    *capture.Manager
	transport  *inputTransport
	source     *frameSource
}

func newFixture(t *testing.T, options Options) fixture {
	t.Helper()
	transport := &inputTransport{}
	controller := hid.NewController(transport)
	mouse, err := hid.NewMouse(controller, hid.MouseOptions{Width: 1920, Height: 1080, ButtonMode: "relative"})
	if err != nil {
		t.Fatal(err)
	}
	source := &frameSource{frames: make(chan v4l2.Frame), opened: make(chan struct{}), closed: make(chan struct{})}
	captures, err := capture.New(capture.Options{
		Device: "/dev/fake", Width: 128, Height: 96, Autocrop: true, IdleTimeout: time.Hour,
		OpenSource: func(context.Context, string, v4l2.Options) (capture.Source, error) {
			close(source.opened)
			return source, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(controller, mouse, captures, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		server.Close()
		if err := controller.Close(); err != nil {
			t.Errorf("controller shutdown: %v", err)
		}
		server.Wait()
		if err := captures.Close(); err != nil {
			t.Errorf("capture shutdown: %v", err)
		}
	})
	return fixture{server, controller, mouse, captures, transport, source}
}

func sourceJPEG(t testing.TB) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 128, 96))
	for y := 12; y < 84; y++ {
		for x := range 128 {
			img.SetGray(x, y, color.Gray{Y: 220})
		}
	}
	var data bytes.Buffer
	if err := jpeg.Encode(&data, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func sourceFrame(data []byte, sequence uint32) v4l2.Frame {
	return v4l2.Frame{Data: bytes.Clone(data), Flags: v4l2.TimestampMonotonic | v4l2.TimestampStartOfExposure, Timestamp: v4l2.Timestamp{Seconds: 100}, Sequence: sequence}
}

type socketMessage struct {
	kind websocket.MessageType
	data []byte
}

type fakeSocket struct {
	input     chan socketMessage
	output    chan socketMessage
	closed    chan struct{}
	closeOnce sync.Once
	writer    func(context.Context, websocket.MessageType) (io.WriteCloser, error)
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{input: make(chan socketMessage), output: make(chan socketMessage, 100), closed: make(chan struct{})}
}

func (socket *fakeSocket) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case message := <-socket.input:
		return message.kind, message.data, nil
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case <-socket.closed:
		return 0, nil, io.EOF
	}
}

func (socket *fakeSocket) Writer(ctx context.Context, kind websocket.MessageType) (io.WriteCloser, error) {
	if socket.writer != nil {
		return socket.writer(ctx, kind)
	}
	return &messageWriter{finish: func(data []byte) error {
		select {
		case socket.output <- socketMessage{kind, bytes.Clone(data)}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}, nil
}

func (socket *fakeSocket) CloseNow() error {
	socket.closeOnce.Do(func() { close(socket.closed) })
	return nil
}

type messageWriter struct {
	data     bytes.Buffer
	finish   func([]byte) error
	writeErr error
}

func (writer *messageWriter) Write(data []byte) (int, error) {
	if writer.writeErr != nil {
		return 0, writer.writeErr
	}
	return writer.data.Write(data)
}

func (writer *messageWriter) Close() error { return writer.finish(writer.data.Bytes()) }

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for test event")
		var zero T
		return zero
	}
}
