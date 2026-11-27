//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/serial"
)

type testTransport struct {
	mu       sync.Mutex
	prepares int
	writes   int
	prepare  func(context.Context) error
}

func (transport *testTransport) Prepare(ctx context.Context) (uint64, error) {
	transport.mu.Lock()
	transport.prepares++
	transport.mu.Unlock()
	if transport.prepare != nil {
		return 0, transport.prepare(ctx)
	}
	return 1, ctx.Err()
}

func (transport *testTransport) WriteFrame(ctx context.Context, _ []byte) error {
	transport.mu.Lock()
	transport.writes++
	transport.mu.Unlock()
	return ctx.Err()
}

func (*testTransport) Close() error { return nil }

func bareServer(t *testing.T, options Options) (*Server, *hid.Controller, *testTransport) {
	t.Helper()
	transport := &testTransport{}
	controller := hid.NewController(transport)
	layout, err := hid.LoadLayout("us104", "")
	if err != nil {
		t.Fatal(err)
	}
	mouse, err := hid.NewMouse(controller, hid.MouseOptions{Width: 1920, Height: 1080})
	if err != nil {
		t.Fatal(err)
	}
	return New(hid.NewKeyboard(controller, layout), mouse, options), controller, transport
}

func serveForTest(t *testing.T, server *Server, controller *hid.Controller) (string, context.CancelFunc) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve shutdown: %v", err)
		}
		if err := controller.Close(); err != nil {
			t.Errorf("controller shutdown: %v", err)
		}
		server.Wait()
	})
	return listener.Addr().String(), cancel
}

func dialTest(t *testing.T, address string) *net.TCPConn {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn.(*net.TCPConn)
}

func writeTest(t *testing.T, writer io.Writer, data string) {
	t.Helper()
	if _, err := io.WriteString(writer, data); err != nil {
		t.Fatal(err)
	}
}

func replyTest(t *testing.T, conn net.Conn) apiwire.Response {
	t.Helper()
	reader := bufio.NewReader(conn)
	line, err := apiwire.ReadLine(reader, apiwire.MaxResponseLine)
	if err != nil {
		t.Fatal(err)
	}
	var response apiwire.Response
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected connection EOF after one response, got %v", err)
	}
	return response
}

func TestRawRequestFraming(t *testing.T) {
	server, controller, transport := bareServer(t, Options{})
	address, _ := serveForTest(t, server, controller)
	for _, test := range []struct {
		name       string
		line       string
		closeWrite bool
		code       apiwire.Code
		id         string
	}{
		{"optional token without authentication", "{\"id\":1,\"method\":\"ping\",\"token\":\"ignored\"}\n", false, "", "1"},
		{"wrong token type", "{\"id\":1,\"method\":\"ping\",\"token\":1}\n", false, apiwire.ErrInvalidRequest, "null"},
		{"missing params", "{\"id\":1,\"method\":\"ping\"}\n", false, "", "1"},
		{"missing ID", "{\"method\":\"ping\"}\r\n", false, "", "null"},
		{"large ID", "{\"id\":9007199254740993,\"method\":\"ping\"}\n", false, "", "9007199254740993"},
		{"structured ID", "{\"id\":{\"a\":[9007199254740993]},\"method\":\"ping\"}\n", false, "", `{"a":[9007199254740993]}`},
		{"invalid JSON", "{\n", false, apiwire.ErrInvalidRequest, "null"},
		{"top null", "null\n", false, apiwire.ErrInvalidRequest, "null"},
		{"unknown envelope field", "{\"method\":\"ping\",\"extra\":1}\n", false, apiwire.ErrInvalidRequest, "null"},
		{"missing method", "{\"id\":4}\n", false, apiwire.ErrInvalidRequest, "4"},
		{"null method", "{\"id\":4,\"method\":null}\n", false, apiwire.ErrInvalidRequest, "4"},
		{"wrong method type", "{\"id\":4,\"method\":1}\n", false, apiwire.ErrInvalidRequest, "null"},
		{"unknown method", "{\"id\":5,\"method\":\"unknown\"}\n", false, apiwire.ErrUnknownMethod, "5"},
		{"second object on line", "{\"method\":\"ping\"}{}\n", false, apiwire.ErrInvalidRequest, "null"},
		{"unterminated line", "{\"method\":\"ping\"}", true, apiwire.ErrInvalidRequest, "null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := dialTest(t, address)
			writeTest(t, conn, test.line)
			if test.closeWrite {
				if err := conn.CloseWrite(); err != nil {
					t.Fatal(err)
				}
			}
			response := replyTest(t, conn)
			if response.Error != test.code || response.OK != (test.code == "") || string(response.ID) != test.id {
				t.Fatalf("response=%+v id=%s; want code=%q id=%s", response, response.ID, test.code, test.id)
			}
		})
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.prepares != 0 || transport.writes != 0 {
		t.Fatal("framing/read-only requests touched HID transport")
	}
}

func TestRequestLineBoundaries(t *testing.T) {
	server, controller, _ := bareServer(t, Options{})
	address, _ := serveForTest(t, server, controller)
	base := `{"id":1,"method":"ping"}`
	for _, extra := range []int{0, 1} {
		conn := dialTest(t, address)
		line := base + strings.Repeat(" ", apiwire.MaxRequestLine-len(base)-1+extra) + "\n"
		writeTest(t, conn, line)
		response := replyTest(t, conn)
		if extra == 0 && !response.OK || extra != 0 && response.Error != apiwire.ErrRequestTooLarge {
			t.Fatalf("line length=%d response=%+v", len(line), response)
		}
	}
}

func TestBufferedExtraInputIsNotDispatched(t *testing.T) {
	server, _, transport := bareServer(t, Options{})
	serverConn, client := net.Pipe()
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	server.pending = append(server.pending, serverConn)
	go func() {
		defer close(done)
		server.handleConn(context.Background(), serverConn)
	}()
	writeTest(t, client, "{\"id\":1,\"method\":\"send_key\",\"params\":{\"key\":\"a\"}}\nsecond\n")
	if response := replyTest(t, client); response.Error != apiwire.ErrInvalidRequest {
		t.Fatalf("extra input accepted: %+v", response)
	}
	<-done
	if transport.prepares != 0 || transport.writes != 0 {
		t.Fatal("known buffered extra input was dispatched")
	}
}

func TestDisconnectAndExtraByteCancelStartedHandler(t *testing.T) {
	for _, kind := range []string{"full close", "half close", "extra byte"} {
		t.Run(kind, func(t *testing.T) {
			server, controller, _ := bareServer(t, Options{})
			started, canceled := make(chan struct{}), make(chan struct{})
			server.handlers["block"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
				close(started)
				<-ctx.Done()
				close(canceled)
				return nil, ctx.Err()
			}
			address, _ := serveForTest(t, server, controller)
			conn := dialTest(t, address)
			writeTest(t, conn, "{\"id\":1,\"method\":\"block\"}\n")
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("handler did not start")
			}
			switch kind {
			case "full close":
				_ = conn.Close()
			case "half close":
				if err := conn.CloseWrite(); err != nil {
					t.Fatal(err)
				}
			case "extra byte":
				writeTest(t, conn, "x")
			}
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("handler did not observe connection cancellation")
			}
			if kind != "full close" {
				response := replyTest(t, conn)
				want := apiwire.ErrCanceled
				if kind == "extra byte" {
					want = apiwire.ErrInvalidRequest
				}
				if response.Error != want {
					t.Fatalf("response=%+v; want %q", response, want)
				}
			}
		})
	}
}

func TestShutdownWakesInitialReader(t *testing.T) {
	server, controller, _ := bareServer(t, Options{})
	address, cancel := serveForTest(t, server, controller)
	conn := dialTest(t, address)
	writeTest(t, conn, "{")
	cancel()
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("shutdown left a partial-line reader open")
	}
}

func TestFatalAcceptReturnsBeforeHandlersDrain(t *testing.T) {
	server, controller, _ := bareServer(t, Options{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	server.handlers["slow"] = func(context.Context, json.RawMessage) (any, error) {
		close(started)
		<-release // Models owned work that cannot be interrupted immediately.
		return struct{}{}, nil
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(context.Background(), listener) }()
	conn := dialTest(t, listener.Addr().String())
	writeTest(t, conn, "{\"method\":\"slow\"}\n")
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("slow handler did not start")
	}
	waited := make(chan struct{})
	go func() { server.Wait(); close(waited) }()
	_ = listener.Close()
	select {
	case err := <-served:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("lost unexpected Accept failure: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve waited for slow handler cleanup")
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waited:
		t.Fatal("Wait returned before the owned handler finished")
	default:
	}
	unblock()
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("owned handler did not finish after release")
	}
}

type functionListener struct {
	accept func() (net.Conn, error)
}

func (listener functionListener) Accept() (net.Conn, error) { return listener.accept() }
func (functionListener) Close() error                       { return nil }
func (functionListener) Addr() net.Addr                     { return &net.TCPAddr{} }

func TestAcceptCancellationRaceClosesReturnedConnection(t *testing.T) {
	server, _, transport := bareServer(t, Options{})
	serverConn, client := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener := functionListener{accept: func() (net.Conn, error) {
		cancel() // Cancellation wins after Accept selected a connection.
		return serverConn, nil
	}}
	if err := server.Serve(ctx, listener); err != nil {
		t.Fatal(err)
	}
	server.Wait()
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("late accepted connection was not closed: %v", err)
	}
	if transport.prepares != 0 {
		t.Fatal("shutdown admitted input")
	}
}

type readFailureConn struct {
	net.Conn
	err    error
	output bytes.Buffer
}

func (conn *readFailureConn) Read([]byte) (int, error)    { return 0, conn.err }
func (conn *readFailureConn) Write(p []byte) (int, error) { return conn.output.Write(p) }

func TestInitialReadErrorIsReported(t *testing.T) {
	server, _, _ := bareServer(t, Options{})
	left, right := net.Pipe()
	defer right.Close()
	conn := &readFailureConn{Conn: left, err: errors.New("read failed")}
	server.handleConn(context.Background(), conn)
	var response apiwire.Response
	if err := json.Unmarshal(conn.output.Bytes(), &response); err != nil || response.Error != apiwire.ErrInvalidRequest {
		t.Fatalf("read error response=%+v, %v", response, err)
	}
}

func TestClassifyErrors(t *testing.T) {
	for _, test := range []struct {
		err  error
		code apiwire.Code
	}{
		{apiwire.ErrCaptureChanged, apiwire.ErrCaptureChanged},
		{errors.Join(context.Canceled, errors.New("cleanup failed")), apiwire.ErrCanceled},
		{context.DeadlineExceeded, apiwire.ErrTimeout},
		{hid.ErrUnsupported, apiwire.ErrUnsupported},
		{hid.ErrModifier, apiwire.ErrInvalidParams},
		{hid.ErrKeycode, apiwire.ErrInvalidParams},
		{hid.ErrDelay, apiwire.ErrInvalidParams},
		{hid.ErrSequence, apiwire.ErrInvalidParams},
		{hid.ErrCoordinates, apiwire.ErrInvalidParams},
		{hid.ErrMovement, apiwire.ErrInvalidParams},
		{hid.ErrButton, apiwire.ErrInvalidParams},
		{hid.ErrButtonMode, apiwire.ErrInvalidParams},
		{serial.ErrBusy, apiwire.ErrBusy},
		{hid.ErrClosed, apiwire.ErrUnavailable},
		{errors.New("device vanished"), apiwire.ErrUnavailable},
	} {
		if code, _ := classifyError(test.err); code != test.code {
			t.Errorf("classify %v=%q; want %q", test.err, code, test.code)
		}
	}
}

type failingWriter struct{ err error }

func (writer failingWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestReplyEncodingAndLogging(t *testing.T) {
	var ordinary, keys, output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&ordinary, &slog.HandlerOptions{Level: slog.LevelDebug}))
	keyLogger := slog.New(slog.NewTextHandler(&keys, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server, _, _ := bareServer(t, Options{Logger: logger, KeyLogger: keyLogger})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := server.typeText(ctx, json.RawMessage(`{"text":"secret"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := server.respond(&output, nil, "send_key", nil, fmt.Errorf("%w: secret", hid.ErrKeycode)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ordinary.String(), "secret") || !strings.Contains(ordinary.String(), "send_key") || !strings.Contains(keys.String(), "secret") {
		t.Fatalf("request/key logging separation failed: ordinary=%s keys=%s", ordinary.String(), keys.String())
	}
	output.Reset()
	if err := server.respond(&output, nil, "broken result", make(chan int), nil); err != nil {
		t.Fatal(err)
	}
	var response apiwire.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.OK || response.Error != apiwire.ErrUnavailable {
		t.Fatalf("unencodable result response=%+v, %v", response, err)
	}
	writeError := errors.New("write failed")
	if err := server.respond(failingWriter{writeError}, nil, "ping", struct{}{}, nil); !errors.Is(err, writeError) {
		t.Fatalf("lost reply write failure: %v", err)
	}
}

func TestConstructorRejectsNilHID(t *testing.T) {
	for _, construct := range []func(){func() { New(nil, &hid.Mouse{}, Options{}) }, func() { New(&hid.Keyboard{}, nil, Options{}) }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("nil HID dependency accepted")
				}
			}()
			construct()
		}()
	}
}
