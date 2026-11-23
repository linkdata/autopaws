//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/api"
	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/v4l2"
)

func dialViewer(t *testing.T, fixture fixture) *websocket.Conn {
	t.Helper()
	httpServer := httptest.NewServer(fixture.server)
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	kind, data := readViewer(t, conn)
	if kind != websocket.MessageText || string(data) != `{"type":"config","audio":{"status":"unavailable","generation":"0"}}` {
		t.Fatalf("first message was not precise audio config: %d,%s", kind, data)
	}
	return conn
}

func readViewer(t *testing.T, conn *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	kind, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return kind, data
}

func sendViewer(t *testing.T, conn *websocket.Conn, message string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
		t.Fatal(err)
	}
}

func TestWebSocketVideoUsesSharedCaptureAndUnchangedJPEG(t *testing.T) {
	fixture := newFixture(t, Options{FPS: 120})
	conn := dialViewer(t, fixture)
	receive(t, fixture.source.opened)
	jpegData := sourceJPEG(t)
	fixture.source.frames <- sourceFrame(jpegData, 100)
	kind, data := readViewer(t, conn)
	if kind != websocket.MessageBinary || len(data) != mediaHeaderSize+len(jpegData) || !bytes.Equal(data[mediaHeaderSize:], jpegData) {
		t.Fatal("viewer JPEG was re-encoded or framed incorrectly")
	}
	if binary.LittleEndian.Uint64(data[8:]) != 1 || binary.LittleEndian.Uint64(data[16:]) != 1 || binary.LittleEndian.Uint32(data[32:]) != 128 || binary.LittleEndian.Uint32(data[36:]) != 96 || binary.LittleEndian.Uint32(data[44:]) != 12 || binary.LittleEndian.Uint32(data[52:]) != 72 {
		t.Fatalf("frame metadata not paired with JPEG:%x", data[:mediaHeaderSize])
	}
	if !fixture.capture.Info().Running {
		t.Fatal("connected client did not keep shared capture running")
	}
	fixture.source.frames <- sourceFrame(jpegData, 0) // Source sequence may reset.
	_, data = readViewer(t, conn)
	if binary.LittleEndian.Uint64(data[16:]) != 2 {
		t.Fatal("delivery sequence reused the resetting raw source sequence")
	}
	_ = conn.CloseNow()
	fixture.server.Close()
	fixture.server.Wait()
	if prepares, frames := fixture.transport.snapshot(); prepares != 0 || len(frames) != 0 {
		t.Fatal("passive viewer disconnect emitted global input release")
	}
}

func TestWebSocketDefaultOriginCheckRejectsCrossOrigin(t *testing.T) {
	fixture := newFixture(t, Options{})
	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"http://elsewhere.invalid"}}})
	if conn != nil {
		_ = conn.CloseNow()
		t.Fatal("cross-origin upgrade succeeded")
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status=%v error=%v", response, err)
	}
	select {
	case <-fixture.source.opened:
		t.Fatal("rejected client subscribed to capture")
	default:
	}
}

func TestWebSocketOrderedInputAndDisconnectCleanup(t *testing.T) {
	fixture := newFixture(t, Options{})
	changed := make(chan []byte, 20)
	fixture.transport.hook = func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0)) || bytes.Equal(frame, ch9329.MouseRelative(0, 0, 0, 0)) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > time.Second || ctx.Err() != nil {
				return errors.New("release lacks uncancelled one-second budget")
			}
		}
		changed <- bytes.Clone(frame)
		return nil
	}
	conn := dialViewer(t, fixture)
	for _, input := range []struct {
		message string
		frames  [][]byte
	}{
		{`{"type":"keydown","code":"KeyA"}`, [][]byte{ch9329.Keyboard(0, 4)}},
		{`{"type":"mousedown","x":2048,"y":1024,"buttons":1}`, [][]byte{ch9329.MouseAbsolute(0, 2048, 1024, 0), ch9329.MouseRelative(1, 0, 0, 0)}},
		// A mismatched move mask cannot silently change relative held state.
		{`{"type":"mousemove","x":4095,"y":0,"buttons":0}`, [][]byte{ch9329.MouseAbsolute(1, 4095, 0, 0)}},
		{`{"type":"scroll","deltaY":-1000.2}`, [][]byte{ch9329.MouseRelative(1, 0, 0, -127)}},
	} {
		sendViewer(t, conn, input.message)
		for _, want := range input.frames {
			if got := receive(t, changed); !bytes.Equal(got, want) {
				t.Fatalf("input%s emitted%x; want%x", input.message, got, want)
			}
		}
	}
	_ = conn.CloseNow()
	for _, want := range [][]byte{ch9329.Keyboard(0), ch9329.MouseRelative(0, 0, 0, 0)} {
		if got := receive(t, changed); !bytes.Equal(got, want) {
			t.Fatalf("disconnect cleanup=%x; want%x", got, want)
		}
	}
	fixture.server.Close()
	fixture.server.Wait()
}

func TestWebSocketExplicitReleaseAndInvalidInputCleanup(t *testing.T) {
	for _, final := range []string{`{"type":"release_all"}`, `null`} {
		t.Run(final, func(t *testing.T) {
			fixture := newFixture(t, Options{})
			changed := make(chan []byte, 10)
			fixture.transport.hook = func(_ context.Context, frame []byte) error { changed <- bytes.Clone(frame); return nil }
			conn := dialViewer(t, fixture)
			sendViewer(t, conn, `{"type":"keydown","code":"KeyA"}`)
			receive(t, changed)
			sendViewer(t, conn, `{"type":"keyup","code":"KeyA"}`)
			if frame := receive(t, changed); !bytes.Equal(frame, ch9329.Keyboard(0)) {
				t.Fatal("keyup overtook keydown")
			}
			sendViewer(t, conn, `{"type":"keydown","code":"KeyB"}`)
			receive(t, changed)
			sendViewer(t, conn, final)
			if frame := receive(t, changed); !bytes.Equal(frame, ch9329.Keyboard(0)) {
				t.Fatalf("release/error left key held:%x", frame)
			}
			_ = conn.CloseNow()
			fixture.server.Close()
			fixture.server.Wait()
			if _, frames := fixture.transport.snapshot(); len(frames) != 4 {
				t.Fatalf("release was retried on disconnect:%x", frames)
			}
		})
	}
}

func TestWebSocketShortcutAndAPIActionCannotInterleave(t *testing.T) {
	fixture := newFixture(t, Options{})
	layout, err := hid.LoadLayout("us104", "")
	if err != nil {
		t.Fatal(err)
	}
	keyboard := hid.NewKeyboard(fixture.controller, layout)
	apiServer := api.New(keyboard, fixture.mouse, api.Options{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- apiServer.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		if err := receive(t, served); err != nil {
			t.Error(err)
		}
		_ = fixture.controller.Close()
		apiServer.Wait()
	})
	pressed, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	fixture.transport.hook = func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(5, 0x4c)) {
			close(pressed)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	conn := dialViewer(t, fixture)
	sendViewer(t, conn, `{"type":"shortcut","keys":["ControlLeft","AltLeft","Delete"]}`)
	receive(t, pressed)
	apiDone := make(chan error, 1)
	go func() {
		apiDone <- apiclient.New(listener.Addr().String()).Call(ctx, "send_key", map[string]string{"key": "z"}, nil)
	}()
	time.Sleep(20 * time.Millisecond)
	if prepares, frames := fixture.transport.snapshot(); prepares != 1 || len(frames) != 1 {
		t.Fatal("API entered the browser's complete shortcut action")
	}
	unblock()
	if err := receive(t, apiDone); err != nil {
		t.Fatal(err)
	}
	_, frames := fixture.transport.snapshot()
	want := [][]byte{ch9329.Keyboard(5, 0x4c), ch9329.Keyboard(0), ch9329.Keyboard(0, 0x1d), ch9329.Keyboard(0)}
	if !reflect.DeepEqual(frames, want) {
		t.Fatalf("browser/API action frames=%x; want%x", frames, want)
	}
}

func TestSlowWriterSkipsFramesAndDoesNotBlockOtherClients(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fixture := newFixture(t, Options{FPS: 20})
		slow, fast := newFakeSocket(), newFakeSocket()
		blocked := make(chan struct{})
		slow.writer = func(ctx context.Context, kind websocket.MessageType) (io.WriteCloser, error) {
			if kind == websocket.MessageBinary {
				close(blocked)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return &messageWriter{finish: func(data []byte) error { slow.output <- socketMessage{kind, bytes.Clone(data)}; return nil }}, nil
		}
		slowDone, fastDone := make(chan struct{}), make(chan struct{})
		go func() { fixture.server.serveConnection(slow); close(slowDone) }()
		go func() { fixture.server.serveConnection(fast); close(fastDone) }()
		receive(t, slow.output)
		receive(t, fast.output)
		data := sourceJPEG(t)
		fixture.source.frames <- sourceFrame(data, 1)
		receive(t, blocked)
		first := receive(t, fast.output)
		if first.kind != websocket.MessageBinary {
			t.Fatal("fast client did not receive first frame")
		}
		for sequence := uint32(2); sequence <= 3; sequence++ {
			tagged := append(bytes.Clone(data[:2]), 0xff, 0xfe, 0, 3, byte(sequence))
			tagged = append(tagged, data[2:]...)
			fixture.source.frames <- sourceFrame(tagged, sequence)
		}
		synctest.Wait()
		time.Sleep(49 * time.Millisecond)
		select {
		case <-fast.output:
			t.Fatal("video exceeded configured frame rate")
		default:
		}
		time.Sleep(time.Millisecond)
		second := receive(t, fast.output)
		if binary.LittleEndian.Uint64(second.data[16:]) != 2 || second.data[mediaHeaderSize+6] != 3 {
			t.Fatal("fast writer did not take newest queued source frame")
		}
		time.Sleep(time.Second)
		receive(t, slowDone)
		select {
		case <-fast.closed:
			t.Fatal("slow client write deadline closed another client")
		default:
		}
		fixture.source.frames <- sourceFrame(data, 0)
		third := receive(t, fast.output)
		if binary.LittleEndian.Uint64(third.data[16:]) != 3 {
			t.Fatal("raw source sequence reset corrupted delivery ordering")
		}
		fixture.server.Close()
		receive(t, fastDone)
	})
}

func TestConnectionConfigIsValidJSON(t *testing.T) {
	fixture := newFixture(t, Options{})
	conn := newFakeSocket()
	done := make(chan struct{})
	go func() { fixture.server.serveConnection(conn); close(done) }()
	message := receive(t, conn.output)
	var config struct {
		Type  string `json:"type"`
		Audio struct {
			Status, Generation string
		} `json:"audio"`
	}
	if err := json.Unmarshal(message.data, &config); err != nil || config.Type != "config" || config.Audio.Generation != "0" || config.Audio.Status != "unavailable" {
		t.Fatalf("config=%+v,%v", config, err)
	}
	fixture.server.Close()
	receive(t, done)
}

func TestConfigWriteFailureEndsConnection(t *testing.T) {
	fixture := newFixture(t, Options{})
	failure := errors.New("configuration write failed")
	conn := newFakeSocket()
	conn.writer = func(context.Context, websocket.MessageType) (io.WriteCloser, error) { return nil, failure }
	done := make(chan struct{})
	go func() { fixture.server.serveConnection(conn); close(done) }()
	receive(t, done)
	if prepares, frames := fixture.transport.snapshot(); prepares != 0 || len(frames) != 0 {
		t.Fatal("configuration failure emitted passive release")
	}
}

func TestConnectionReportsCleanupFailureWithoutSecondAttempt(t *testing.T) {
	var log bytes.Buffer
	fixture := newFixture(t, Options{Logger: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	pressed := make(chan struct{})
	failure := errors.New("matching release failed")
	fixture.transport.hook = func(_ context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
			close(pressed)
		}
		if bytes.Equal(frame, ch9329.Keyboard(0)) {
			return failure
		}
		return nil
	}
	conn := newFakeSocket()
	done := make(chan struct{})
	go func() { fixture.server.serveConnection(conn); close(done) }()
	receive(t, conn.output)
	conn.input <- socketMessage{websocket.MessageText, []byte(`{"type":"keydown","code":"KeyA"}`)}
	receive(t, pressed)
	_ = conn.CloseNow()
	receive(t, done)
	if _, frames := fixture.transport.snapshot(); len(frames) != 2 || !strings.Contains(log.String(), failure.Error()) || !strings.Contains(log.String(), "Web client ended") {
		t.Fatalf("cleanup error was hidden or retried: %s", log.String())
	}
	fixture.transport.mu.Lock()
	fixture.transport.hook = nil // Runtime's final Controller.Close can retry.
	fixture.transport.mu.Unlock()
}

func TestGlobalControllerShutdownOwnsConnectionRelease(t *testing.T) {
	var log bytes.Buffer
	fixture := newFixture(t, Options{Logger: slog.New(slog.NewTextHandler(&log, nil))})
	pressed := make(chan struct{})
	fixture.transport.hook = func(_ context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
			close(pressed)
		}
		return nil
	}
	conn := newFakeSocket()
	done := make(chan struct{})
	go func() { fixture.server.serveConnection(conn); close(done) }()
	receive(t, conn.output)
	conn.input <- socketMessage{websocket.MessageText, []byte(`{"type":"keydown","code":"KeyA"}`)}
	receive(t, pressed)
	if err := fixture.controller.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.server.Close()
	receive(t, done)
	if strings.Contains(log.String(), "cleanup failed") {
		t.Fatalf("normal global shutdown reported a connection cleanup failure: %s", log.String())
	}
}

func TestVideoUnavailableEventKeepsSubscriptionRepairable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fixture := newFixture(t, Options{})
		captures, err := capture.New(capture.Options{Width: 128, Height: 96, OpenSource: func(context.Context, string, v4l2.Options) (capture.Source, error) { return fixture.source, nil }})
		if err != nil {
			t.Fatal(err)
		}
		defer captures.Close()
		server, err := New(fixture.controller, fixture.mouse, captures, Options{})
		if err != nil {
			t.Fatal(err)
		}
		subscription, err := captures.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		defer subscription.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		conn := newFakeSocket()
		done := make(chan error, 1)
		go func() { done <- server.write(ctx, conn, subscription) }()
		receive(t, conn.output)
		synctest.Wait() // No repeated unavailable-event spin; writer awaits repair.
		if err := captures.SetDevice(t.Context(), "/dev/repaired"); err != nil {
			t.Fatal(err)
		}
		fixture.source.frames <- sourceFrame(sourceJPEG(t), 1)
		if message := receive(t, conn.output); message.kind != websocket.MessageBinary {
			t.Fatal("repaired capture did not resume subscription")
		}
		cancel()
		if err := receive(t, done); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestServerCloseCancelsOtherClientsWhileKernelCallIsOwned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fixture := newFixture(t, Options{})
		pressed, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		fixture.transport.hook = func(_ context.Context, frame []byte) error {
			if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
				close(pressed)
				<-release // Models a driver call retaining ownership after cancel.
			}
			return nil
		}
		active, passive := newFakeSocket(), newFakeSocket()
		activeDone, passiveDone := make(chan struct{}), make(chan struct{})
		go func() { fixture.server.serveConnection(active); close(activeDone) }()
		go func() { fixture.server.serveConnection(passive); close(passiveDone) }()
		receive(t, active.output)
		receive(t, passive.output)
		active.input <- socketMessage{websocket.MessageText, []byte(`{"type":"keydown","code":"KeyA"}`)}
		receive(t, pressed)
		fixture.server.Close()
		receive(t, passiveDone)
		receive(t, active.closed)
		select {
		case <-activeDone:
			t.Fatal("active handler abandoned retained driver ownership")
		default:
		}
		closed := make(chan error, 1)
		go func() { closed <- fixture.controller.Close() }()
		synctest.Wait()
		unblock()
		receive(t, activeDone)
		if err := receive(t, closed); err != nil {
			t.Fatal(err)
		}
		if _, frames := fixture.transport.snapshot(); !reflect.DeepEqual(frames, [][]byte{ch9329.Keyboard(0, 4), ch9329.Keyboard(0), ch9329.MouseRelative(0, 0, 0, 0)}) {
			t.Fatalf("shutdown cleanup was omitted/retried:%x", frames)
		}
	})
}
