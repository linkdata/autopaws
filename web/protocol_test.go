//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/v4l2"
)

func TestMediaHeaderFieldsAndClockDomain(t *testing.T) {
	metadata := capture.Metadata{Width: 128, Height: 96, Crop: image.Rect(4, 12, 124, 84), Generation: 1<<63 + 3, TimestampNS: 100_000_000_000, TimestampFlags: v4l2.TimestampMonotonic | v4l2.TimestampStartOfExposure}
	sequence := uint64(1<<63 + 7)
	header := videoHeader(metadata, sequence)
	if header[0] != videoMessage || header[1] != mediaVersion || binary.LittleEndian.Uint16(header[2:]) != 64 || audioMessage != 2 || flagMonotonic != 1 || flagStartOfExposure != 2 || flagEstimated != 4 {
		t.Fatal("media version/kind/header/flag contract changed")
	}
	if binary.LittleEndian.Uint32(header[4:]) != 3 || binary.LittleEndian.Uint64(header[8:]) != metadata.Generation || binary.LittleEndian.Uint64(header[16:]) != sequence || binary.LittleEndian.Uint64(header[24:]) != uint64(metadata.TimestampNS) {
		t.Fatal("media timestamp or uint64 precision lost")
	}
	for offset, want := range map[int]uint32{32: 128, 36: 96, 40: 4, 44: 12, 48: 120, 52: 72, 56: 0, 60: 0} {
		if got := binary.LittleEndian.Uint32(header[offset:]); got != want {
			t.Errorf("header offset%d=%d; want%d", offset, got, want)
		}
	}
	metadata.TimestampFlags = v4l2.TimestampMonotonic | v4l2.TimestampEstimated
	header = videoHeader(metadata, sequence)
	if binary.LittleEndian.Uint32(header[4:]) != 5 || binary.LittleEndian.Uint64(header[24:]) != uint64(metadata.TimestampNS) {
		t.Fatal("estimated receipt timing lost its wire flag or clock")
	}
	metadata.TimestampFlags = v4l2.TimestampEstimated
	header = videoHeader(metadata, sequence)
	if binary.LittleEndian.Uint32(header[4:]) != 0 || binary.LittleEndian.Uint64(header[24:]) != 0 {
		t.Fatal("estimate advertised without a monotonic clock")
	}
	metadata.Crop = image.Rectangle{}
	metadata.TimestampFlags = v4l2.TimestampCopy
	header = videoHeader(metadata, 1)
	if binary.LittleEndian.Uint64(header[24:]) != 0 || binary.LittleEndian.Uint32(header[4:]) != 0 || binary.LittleEndian.Uint32(header[48:]) != 128 || binary.LittleEndian.Uint32(header[52:]) != 96 {
		t.Fatal("unknown clock advertised as monotonic, or no-crop extent lost")
	}
}

func TestInputValidationBeforeHardware(t *testing.T) {
	fixture := newFixture(t, Options{})
	browser := hid.NewBrowser(fixture.controller, fixture.mouse)
	for _, data := range []string{"", `null`, `[]`, `{`, `{} {}`, `{"type":"scroll","unexpected":1}`, `{"type":"mousemove","x":1.0}`} {
		if _, err := decodeInput([]byte(data)); !errors.Is(err, ErrInput) {
			t.Errorf("invalid JSON shape%s accepted: %v", data, err)
		}
	}
	for _, data := range []string{
		`{}`, `{"type":"unknown"}`, `{"type":"keydown"}`, `{"type":"keyup","code":null}`,
		`{"type":"mousemove","x":0,"y":0}`, `{"type":"mousedown","x":0,"buttons":1}`,
		`{"type":"mouseup","x":null,"y":0,"buttons":0}`, `{"type":"mousemove","x":0,"y":0,"buttons":-1}`,
		`{"type":"mousedown","x":0,"y":0,"buttons":8}`, `{"type":"mousemove","x":4096,"y":0,"buttons":0}`,
		`{"type":"mouseup","x":0,"y":-1,"buttons":0}`, `{"type":"scroll"}`, `{"type":"scroll","deltaY":null}`,
		`{"type":"shortcut"}`, `{"type":"shortcut","keys":null}`, `{"type":"shortcut","keys":[]}`,
		`{"type":"shortcut","keys":["ControlLeft","unknown"]}`,
	} {
		event, err := decodeInput([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if err := applyInput(t.Context(), browser, event); err == nil {
			t.Errorf("invalid event accepted:%s", data)
		}
	}
	if prepares, frames := fixture.transport.snapshot(); prepares != 0 || len(frames) != 0 {
		t.Fatal("invalid event emitted input")
	}
}

func TestInputReaderErrorsAndDedicatedLogging(t *testing.T) {
	var ordinary, keys bytes.Buffer
	fixture := newFixture(t, Options{Logger: slog.New(slog.NewTextHandler(&ordinary, nil)), KeyLogger: slog.New(slog.NewTextHandler(&keys, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	browser := hid.NewBrowser(fixture.controller, fixture.mouse)
	for _, message := range []socketMessage{{websocket.MessageBinary, []byte("binary")}, {websocket.MessageText, []byte(`null`)}, {websocket.MessageText, []byte(`{"type":"unknown"}`)}} {
		conn := newFakeSocket()
		done := make(chan error, 1)
		go func() { done <- fixture.server.read(t.Context(), conn, browser) }()
		conn.input <- message
		if err := receive(t, done); !errors.Is(err, ErrInput) {
			t.Fatalf("invalid input=%v", err)
		}
	}
	conn := newFakeSocket()
	done := make(chan error, 1)
	go func() { done <- fixture.server.read(t.Context(), conn, browser) }()
	conn.input <- socketMessage{websocket.MessageText, []byte(`{"type":"keydown","code":"KeyA"}`)}
	_ = conn.CloseNow()
	if err := receive(t, done); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if !strings.Contains(keys.String(), "KeyA") || strings.Contains(ordinary.String(), "KeyA") {
		t.Fatalf("key logging separation: ordinary%q keys%q", ordinary.String(), keys.String())
	}
}

func TestBoundedMessageWriterFailureIdentity(t *testing.T) {
	fixture := newFixture(t, Options{})
	openError, writeError, closeError := errors.New("open failed"), errors.New("write failed"), errors.New("close failed")
	for _, test := range []struct{ open, write, close error }{{open: openError}, {write: writeError}, {close: closeError}, {write: writeError, close: closeError}} {
		conn := newFakeSocket()
		conn.writer = func(ctx context.Context, _ websocket.MessageType) (io.WriteCloser, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > time.Second {
				t.Error("write lacks bounded deadline")
			}
			if test.open != nil {
				return nil, test.open
			}
			return &messageWriter{writeErr: test.write, finish: func([]byte) error { return test.close }}, nil
		}
		err := fixture.server.writeMessage(t.Context(), conn, websocket.MessageBinary, []byte("header"), []byte("payload"))
		for _, want := range []error{test.open, test.write, test.close} {
			if want != nil && !errors.Is(err, want) {
				t.Errorf("write lost error%v:%v", want, err)
			}
		}
	}
}

func TestStaticAssetsAndAdmission(t *testing.T) {
	fixture := newFixture(t, Options{})
	for _, asset := range []struct{ path, file, contentType string }{
		{"/", "index.html", "text/html; charset=utf-8"},
		{"/fallback/path", "index.html", "text/html; charset=utf-8"},
		{"/index.html", "index.html", "text/html; charset=utf-8"},
		{"/app.js", "index.html", "text/html; charset=utf-8"},
		{"/viewer.mjs", "viewer.mjs", "text/javascript; charset=utf-8"},
		{"/viewer-helpers.mjs", "viewer-helpers.mjs", "text/javascript; charset=utf-8"},
		{"/viewer.css", "viewer.css", "text/css; charset=utf-8"},
		{"/audio-playback.mjs", "audio-playback.mjs", "text/javascript; charset=utf-8"},
		{"/audio-worklet.mjs", "audio-worklet.mjs", "text/javascript; charset=utf-8"},
		{"/manifest.json", "manifest.json", "application/manifest+json"},
		{"/sw.js", "sw.js", "text/javascript; charset=utf-8"},
		{"/icon.svg", "icon.svg", "image/svg+xml"},
	} {
		want, err := os.ReadFile("assets/" + asset.file)
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			output := httptest.NewRecorder()
			fixture.server.ServeHTTP(output, httptest.NewRequest(method, asset.path, nil))
			if output.Code != 200 || output.Header().Get("Cache-Control") != "no-cache" || output.Header().Get("Content-Type") != asset.contentType || method == http.MethodGet && !bytes.Equal(output.Body.Bytes(), want) || method == http.MethodHead && output.Body.Len() != 0 {
				t.Fatalf("asset%s%s:%+v", method, asset.path, output)
			}
		}
	}
	output := httptest.NewRecorder()
	fixture.server.ServeHTTP(output, httptest.NewRequest(http.MethodPost, "/", nil))
	if output.Code != http.StatusMethodNotAllowed || output.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal("asset method not validated")
	}
	output = httptest.NewRecorder()
	fixture.server.ServeHTTP(output, httptest.NewRequest(http.MethodGet, "/ws", nil))
	if output.Code != http.StatusUpgradeRequired {
		t.Fatal("invalid upgrade accepted")
	}
	fixture.server.Close()
	output = httptest.NewRecorder()
	fixture.server.ServeHTTP(output, httptest.NewRequest(http.MethodGet, "/ws", nil))
	if output.Code != http.StatusServiceUnavailable {
		t.Fatal("web shutdown admitted a new connection")
	}
	fixture.server.Wait()
	select {
	case <-fixture.source.opened:
		t.Fatal("static/failed upgrade opened capture")
	default:
	}
}

func TestConstructorAndClosedCapture(t *testing.T) {
	fixture := newFixture(t, Options{})
	for _, options := range []Options{{FPS: -1}, {FPS: 121}, {WriteTimeout: -1}} {
		if _, err := New(fixture.controller, fixture.mouse, fixture.capture, options); !errors.Is(err, ErrOptions) {
			t.Fatal("invalid web options accepted")
		}
	}
	for _, construct := range []func() error{
		func() error { _, err := New(nil, fixture.mouse, fixture.capture, Options{}); return err },
		func() error { _, err := New(fixture.controller, nil, fixture.capture, Options{}); return err },
		func() error { _, err := New(fixture.controller, fixture.mouse, nil, Options{}); return err },
	} {
		if err := construct(); !errors.Is(err, ErrOptions) {
			t.Fatal(err)
		}
	}
	if fixture.server.options.FPS != 20 || fixture.server.options.WriteTimeout != time.Second {
		t.Fatal("web defaults changed")
	}
	_ = fixture.capture.Close()
	conn := newFakeSocket()
	fixture.server.serveConnection(conn)
	select {
	case <-conn.closed:
	default:
		t.Fatal("closed capture left accepted socket open")
	}
	fixture.server.Close()
	fixture.server.serveConnection(newFakeSocket())
}
