//go:build (linux || darwin) && (amd64 || arm64)

// Package api serves the KVM JSON Lines TCP API using shared HID instances.
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
	"sync"

	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/devices"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/internal/token"
)

// Options supplies configured device information and independent request/key
// loggers. Nil loggers disable their records. This does not probe serial devices.
// Capture is a shared manager owned by the caller. A nil manager/discovery hook
// leaves that service unavailable; no implicit hardware discovery is performed.
type Options struct {
	Token                     string
	SerialDevice              string
	SerialBaud                int
	ScreenWidth, ScreenHeight int
	KeyboardLayout            string
	LayoutsDir                string
	ButtonMode                string
	Logger, KeyLogger         *slog.Logger
	Capture                   *capture.Manager
	DiscoverCapture           func(context.Context) ([]devices.VideoDevice, error)
}

type handler func(context.Context, json.RawMessage) (any, error)

// Server handles one request/response per connection. The caller owns HID
// shutdown; Serve stops networking without waiting for input cleanup to drain.
type Server struct {
	keyboard   *hid.Keyboard
	mouse      *hid.Mouse
	options    Options
	layoutName string
	handlers   map[string]handler

	mu        sync.Mutex
	conns     map[net.Conn]struct{}
	workers   sync.WaitGroup
	serveDone chan struct{}
}

// New constructs a server without opening devices or listeners. Nil HID
// dependencies are programming errors and panic. Call Serve once per Server.
func New(keyboard *hid.Keyboard, mouse *hid.Mouse, options Options) *Server {
	if keyboard == nil || mouse == nil {
		panic("api: nil HID dependency")
	}
	server := &Server{
		keyboard: keyboard, mouse: mouse, options: options,
		layoutName: options.KeyboardLayout,
		conns:      make(map[net.Conn]struct{}), serveDone: make(chan struct{}),
	}
	server.handlers = server.methods()
	return server
}

// Serve accepts connections until ctx is cancelled or Accept fails. It closes
// the listener/connections before returning; cancellation is normal shutdown.
// It does not wait for HID cleanup. Start Controller.Close promptly, then Wait.
func (server *Server) Serve(ctx context.Context, listener net.Listener) error {
	runCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-runCtx.Done()
		server.stopNetwork(listener)
	}()
	defer func() {
		cancel()
		<-stopped
		close(server.serveDone)
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if runCtx.Err() != nil {
				return nil
			}
			return err
		}
		server.mu.Lock()
		if runCtx.Err() != nil {
			server.mu.Unlock()
			_ = conn.Close()
			return nil
		}
		server.conns[conn] = struct{}{}
		server.workers.Add(1)
		server.mu.Unlock()
		go func() {
			defer server.workers.Done()
			defer func() {
				server.mu.Lock()
				delete(server.conns, conn)
				server.mu.Unlock()
			}()
			server.handleConn(runCtx, conn)
		}()
	}
}

func (server *Server) stopNetwork(listener net.Listener) {
	_ = listener.Close()
	server.mu.Lock()
	conns := make([]net.Conn, 0, len(server.conns))
	for conn := range server.conns {
		conns = append(conns, conn)
	}
	server.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

// Wait joins handlers after Serve has stopped adding them. It may be called
// early, but callers should initiate shared Controller.Close before waiting.
func (server *Server) Wait() {
	<-server.serveDone
	server.workers.Wait()
}

func (server *Server) handleConn(ctx context.Context, conn net.Conn) {
	var monitorDone chan struct{}
	defer func() {
		_ = conn.Close()
		if monitorDone != nil {
			<-monitorDone
		}
	}()
	reader := bufio.NewReader(conn)
	line, err := apiwire.ReadLine(reader, apiwire.MaxRequestLine)
	if err != nil {
		if errors.Is(err, io.EOF) || ctx.Err() != nil {
			return
		}
		code := apiwire.ErrInvalidRequest
		if errors.Is(err, apiwire.ErrLineTooLarge) {
			code = apiwire.ErrRequestTooLarge
		}
		_ = server.respond(conn, nil, "", nil, fmt.Errorf("%w: %v", code, err))
		return
	}
	var request apiwire.Request
	if err := decodeObject(line, &request, apiwire.ErrInvalidRequest); err != nil {
		_ = server.respond(conn, nil, "", nil, err)
		return
	}
	if !token.Match(server.options.Token, request.Token) {
		_ = server.respond(conn, request.ID, "", nil, apiwire.ErrUnauthorized)
		return
	}
	if request.Method == "" {
		_ = server.respond(conn, request.ID, "", nil, fmt.Errorf("%w: method is required", apiwire.ErrInvalidRequest))
		return
	}
	if len(request.Params) == 0 {
		request.Params = json.RawMessage("{}")
	}
	if reader.Buffered() != 0 {
		_ = server.respond(conn, request.ID, request.Method, nil, fmt.Errorf("%w: extra input after request line", apiwire.ErrInvalidRequest))
		return
	}
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	monitorDone = make(chan struct{})
	monitorReason := make(chan bool, 1)
	go func() {
		defer close(monitorDone)
		_, err := reader.ReadByte()
		monitorReason <- err == nil
		cancel()
	}()
	method, found := server.handlers[request.Method]
	var result any
	if !found {
		err = fmt.Errorf("%w: %s", apiwire.ErrUnknownMethod, request.Method)
	} else {
		result, err = method(requestCtx, request.Params)
	}
	if err == nil {
		err = requestCtx.Err()
	}
	select {
	case extraInput := <-monitorReason:
		if extraInput {
			err = fmt.Errorf("%w: extra input after request line (action error: %v)", apiwire.ErrInvalidRequest, err)
		}
	default:
	}
	_ = server.respond(conn, request.ID, request.Method, result, err)
}

func (server *Server) respond(writer io.Writer, id json.RawMessage, method string, result any, err error) error {
	response := apiwire.Response{ID: id, OK: err == nil}
	if err == nil {
		response.Result, err = json.Marshal(result)
		response.OK = err == nil
	}
	if err != nil {
		response.Error, response.Detail = classifyError(err)
	}
	if server.options.Logger != nil {
		server.options.Logger.Debug("API request", "method", method, "outcome", string(response.Error))
	}
	return json.NewEncoder(writer).Encode(response)
}

func decodeObject(data []byte, output any, code apiwire.Code) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("%w: expected an object", code)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("%w: %v", code, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: expected one JSON object", code)
	}
	return nil
}
