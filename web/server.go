//go:build (linux || darwin) && (amd64 || arm64)

// Package web serves the browser viewer and forwards ordered physical input.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/internal/token"
)

var ErrOptions = errors.New("invalid web options")

// Options uses the daemon's frame limit and independent debug-key logger.
// Zero FPS uses 20; zero WriteTimeout uses one second. No send has an unbounded
// application wait. Nil loggers disable their respective records.
type Options struct {
	Token             string
	FPS               int
	WriteTimeout      time.Duration
	Logger, KeyLogger *slog.Logger
	Audio             *AudioOptions // Nil preserves a video/input-only server without probes.
}

// Server is an http.Handler with tracked WebSocket lifetimes. Close stops
// admission and cancels every connection before Wait joins any HID cleanup.
// The runtime owns Controller.Close and capture.Manager.Close separately.
type Server struct {
	controller *hid.Controller
	mouse      *hid.Mouse
	capture    *capture.Manager
	options    Options
	lifetime   context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	closed     bool
	workers    sync.WaitGroup
	audio      *audioManager
}

type socket interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Writer(context.Context, websocket.MessageType) (io.WriteCloser, error)
	CloseNow() error
}

func New(controller *hid.Controller, mouse *hid.Mouse, captures *capture.Manager, options Options) (*Server, error) {
	if controller == nil || mouse == nil || captures == nil || options.FPS < 0 || options.FPS > 120 || options.WriteTimeout < 0 {
		return nil, ErrOptions
	}
	if options.FPS == 0 {
		options.FPS = 20
	}
	if options.WriteTimeout == 0 {
		options.WriteTimeout = time.Second
	}
	lifetime, cancel := context.WithCancel(context.Background())
	server := &Server{controller: controller, mouse: mouse, capture: captures, options: options, lifetime: lifetime, cancel: cancel}
	if options.Audio != nil {
		server.audio = newAudioManager(lifetime, captures, *options.Audio, options.Logger)
	}
	return server, nil
}

func (server *Server) ServeHTTP(output http.ResponseWriter, request *http.Request) {
	output.Header().Set("Cache-Control", "no-cache")
	if request.URL.Path != "/ws" {
		serveAsset(output, request)
		return
	}
	if server.options.Token != "" {
		cookie, err := request.Cookie("autopaws_token")
		supplied := ""
		if err == nil {
			supplied, err = url.PathUnescape(cookie.Value)
		}
		if err != nil || !token.Match(server.options.Token, supplied) {
			http.Error(output, "authentication required", http.StatusForbidden)
			return
		}
	}
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		http.Error(output, "viewer is closing", http.StatusServiceUnavailable)
		return
	}
	server.workers.Add(1)
	server.mu.Unlock()
	defer server.workers.Done()
	conn, err := websocket.Accept(output, request, nil) // Keep default origin checks.
	if err != nil {
		return // Accept already wrote its HTTP error response.
	}
	server.serveConnection(conn)
}

func (server *Server) serveConnection(conn socket) {
	ctx, cancel := context.WithCancel(server.lifetime)
	defer cancel()
	defer conn.CloseNow()
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		return
	}
	server.mu.Unlock()
	subscription, err := server.capture.Subscribe()
	if err != nil {
		return
	}
	defer subscription.Close()
	browser := hid.NewBrowser(server.controller, server.mouse)
	written := make(chan error, 1)
	go func() {
		err := server.write(ctx, conn, subscription)
		cancel()
		_ = conn.CloseNow() // Wake Read even while the client sends no input.
		written <- err
	}()
	readErr := server.read(ctx, conn, browser)
	cancel()
	_ = conn.CloseNow()
	writeErr := <-written
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer stop()
	cleanupErr := browser.Release(cleanup)
	shutdownOwnsRelease := server.lifetime.Err() != nil && errors.Is(cleanupErr, hid.ErrClosed)
	if cleanupErr != nil && !shutdownOwnsRelease && server.options.Logger != nil {
		server.options.Logger.Warn("Web input cleanup failed", "error", cleanupErr)
	}
	if server.options.Logger != nil {
		server.options.Logger.Debug("Web client ended", "error", errors.Join(readErr, writeErr))
	}
}

// Close stops admission and cancels all clients together. Each client's writer
// closes its socket as cancellation arrives, independently of retained HID I/O.
// Start shared Controller.Close promptly, then call Wait to join the handlers.
func (server *Server) Close() {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.closed = true
	server.cancel()
}

// Wait may be called after Close, when no new connection worker can be admitted.
func (server *Server) Wait() {
	server.workers.Wait()
	if server.audio != nil {
		<-server.audio.done
	}
}

//go:embed assets/index.html assets/viewer.mjs assets/viewer-helpers.mjs assets/viewer.css
//go:embed assets/manifest.json assets/sw.js assets/icon.svg assets/audio-playback.mjs assets/audio-worklet.mjs
var pageAssets embed.FS

func serveAsset(output http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		output.Header().Set("Allow", "GET, HEAD")
		http.Error(output, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path, contentType := request.URL.Path, "text/html; charset=utf-8"
	switch path {
	case "/viewer.mjs", "/viewer-helpers.mjs", "/sw.js", "/audio-playback.mjs", "/audio-worklet.mjs":
		contentType = "text/javascript; charset=utf-8"
	case "/viewer.css":
		contentType = "text/css; charset=utf-8"
	case "/manifest.json":
		contentType = "application/manifest+json"
	case "/icon.svg":
		contentType = "image/svg+xml"
	default:
		path = "/index.html"
	}
	// Every selected path is an explicit compile-time embed match above.
	data, _ := pageAssets.ReadFile("assets" + path)
	output.Header().Set("Content-Type", contentType)
	http.ServeContent(output, request, "", time.Time{}, bytes.NewReader(data))
}

func (server *Server) writeMessage(ctx context.Context, conn socket, kind websocket.MessageType, parts ...[]byte) error {
	ctx, cancel := context.WithTimeout(ctx, server.options.WriteTimeout)
	defer cancel()
	writer, err := conn.Writer(ctx, kind)
	if err != nil {
		return err
	}
	for _, part := range parts {
		if _, err := writer.Write(part); err != nil {
			return errors.Join(err, writer.Close())
		}
	}
	return writer.Close()
}
