//go:build (linux || darwin || windows) && (amd64 || arm64)

package main

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRuntimeHTTPBoundsStalledRequestsAndPreservesWebSocket(t *testing.T) {
	cfg := runtimeConfig()
	cfg.API, cfg.Web, cfg.Token = "", "127.0.0.1:0", "secret"
	run := startViewerRuntime(t, cfg, &runtimeTransport{}, newRuntimeVideoSource(), nil)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+run.web.Addr().String()+"/ws", &websocket.DialOptions{
		Subprotocols: []string{"autopaws", "autopaws-token." + hex.EncodeToString([]byte(cfg.Token))},
	})
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()
	defer func() { _ = conn.CloseNow(); <-readDone }()
	var stalled []net.Conn
	for _, request := range []string{
		"",
		"GET / HTTP/1.1\r\nHost: localhost\r\n",
		"GET /ws HTTP/1.1\r\nHost: localhost\r\nContent-Length: 1\r\n\r\n",
	} {
		client, err := net.Dial("tcp", run.web.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		if err := client.SetDeadline(time.Now().Add(13 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(client, request); err != nil {
			t.Fatal(err)
		}
		stalled = append(stalled, client)
	}
	for i, client := range stalled {
		_, err := io.Copy(io.Discard, client)
		var networkErr net.Error
		if errors.As(err, &networkErr) && networkErr.Timeout() {
			t.Fatalf("stalled request %d outlived server deadline: %v", i, err)
		}
	}
	// All stalled requests expire after 10s; the upgraded session remains usable.
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("HTTP deadline ended upgraded WebSocket: %v", err)
	}
}

func TestRuntimeHTTPHeaderLimit(t *testing.T) {
	cfg := runtimeConfig()
	cfg.API, cfg.Web = "", "127.0.0.1:0"
	run := startViewerRuntime(t, cfg, &runtimeTransport{}, newRuntimeVideoSource(), nil)
	for _, size := range []int{60 << 10, 70 << 10, 0} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+run.web.Addr().String()+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Close = true
		request.Header.Set("Cookie", "other="+strings.Repeat("a", size))
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		want := http.StatusOK
		if size > 64<<10 {
			want = http.StatusRequestHeaderFieldsTooLarge
		}
		if response.StatusCode != want {
			t.Fatalf("%d-byte cookie status = %d; want %d", size, response.StatusCode, want)
		}
	}
}
