//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/ch9329"
)

func TestTokenProtectsWebSocketAndLeavesBootstrapPublic(t *testing.T) {
	var logs bytes.Buffer
	secret := "a+;å% secret"
	var audioOpens atomic.Int32
	audio := newAudioTestSource(1)
	fixture := newFixture(t, Options{
		Token: secret, Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Audio: &AudioOptions{Device: "/dev/fake-audio", OpenSource: func(context.Context, string) (AudioSource, error) {
			audioOpens.Add(1)
			return audio, nil
		}},
	})
	for _, path := range []string{"/", "/viewer.mjs", "/viewer-helpers.mjs", "/mapping.mjs", "/viewer.css", "/manifest.json", "/sw.js", "/icon.svg", "/audio-playback.mjs", "/audio-worklet.mjs"} {
		output := httptest.NewRecorder()
		fixture.server.ServeHTTP(output, httptest.NewRequest(http.MethodGet, path, nil))
		if output.Code != http.StatusOK || output.Body.Len() == 0 {
			t.Fatalf("unauthenticated bootstrap %s = %d", path, output.Code)
		}
	}
	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	address := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	for _, options := range []*websocket.DialOptions{
		nil,
		{Subprotocols: []string{"unrelated"}},
		{Subprotocols: []string{"autopaws-token."}},
		{Subprotocols: []string{"autopaws-token.GG"}},
		{Subprotocols: []string{"autopaws-token.1"}},
		{Subprotocols: []string{"autopaws-token." + hex.EncodeToString([]byte("wrong-secret"))}},
		{HTTPHeader: http.Header{"Cookie": {"autopaws_token=a%2B%3B%C3%A5%25%20secret"}}},
	} {
		conn, response, err := websocket.Dial(ctx, address, options)
		if conn != nil {
			_ = conn.CloseNow()
			t.Fatal("unauthenticated WebSocket admitted")
		}
		if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
			t.Fatalf("authentication response = %v, %v", response, err)
		}
	}
	select {
	case <-fixture.source.opened:
		t.Fatal("unauthenticated request opened capture")
	default:
	}
	if audioOpens.Load() != 0 {
		t.Fatal("unauthenticated request opened audio")
	}
	if prepares, frames := fixture.transport.snapshot(); prepares != 0 || len(frames) != 0 {
		t.Fatal("unauthenticated request touched HID")
	}
	pressed := make(chan struct{})
	fixture.transport.hook = func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
			close(pressed)
		}
		return ctx.Err()
	}
	conn, response, err := websocket.Dial(ctx, address, &websocket.DialOptions{
		Host: "kvm.example:9330", HTTPHeader: http.Header{"Origin": {"http://kvm.example:9330"}},
		Subprotocols: []string{"unrelated", "autopaws", "autopaws-token." + hex.EncodeToString([]byte(secret))},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != "autopaws" || response.Header.Get("Sec-WebSocket-Protocol") != "autopaws" {
		t.Fatal("credential was negotiated as a subprotocol")
	}
	if kind, _ := readViewer(t, conn); kind != websocket.MessageText {
		t.Fatal("authenticated connection did not receive config")
	}
	receive(t, audio.subscribed)
	if audioOpens.Load() != 1 {
		t.Fatal("authenticated viewer did not open exactly one audio source")
	}
	sendViewer(t, conn, `{"type":"keydown","code":"KeyA"}`)
	receive(t, pressed)
	_ = conn.CloseNow()
	fixture.server.Close()
	fixture.server.Wait()
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "wrong-secret") {
		t.Fatalf("token leaked into logs: %s", logs.String())
	}
}

func TestWebSocketWithoutTokenRejectsDNSHosts(t *testing.T) {
	fixture := newFixture(t, Options{})
	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()
	address := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	for _, host := range []string{"rebind.example:9330", "localhost.example:9330", "localhost:9330", "127.0.0.1:9330", "[::1]:9330"} {
		t.Run(host, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, response, err := websocket.Dial(ctx, address, &websocket.DialOptions{
				Host: host, HTTPHeader: http.Header{"Origin": {"http://" + host}},
			})
			if strings.Contains(host, "example") {
				if conn != nil {
					_ = conn.CloseNow()
					t.Fatal("foreign Host/Origin admitted")
				}
				if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
					t.Fatalf("host rejection = %v, %v", response, err)
				}
				select {
				case <-fixture.source.opened:
					t.Fatal("rejected Host opened capture")
				default:
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			readViewer(t, conn)
		})
	}
}

func TestLiteralHost(t *testing.T) {
	for _, test := range []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST:9330", true},
		{"127.0.0.1", true},
		{"[::1]", true},
		{"[fe80::1%eth0]:9330", true},
		{"192.0.2.1:9330", true},
		{"", false},
		{"example.com", false},
		{"127.0.0.1.example", false},
		{"[example.com]:9330", false},
	} {
		if got := literalHost(test.host); got != test.want {
			t.Errorf("literalHost(%q) = %v; want %v", test.host, got, test.want)
		}
	}
}

func TestViewerExpiresTokenCookie(t *testing.T) {
	fixture := newFixture(t, Options{})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: "autopaws_token", Value: "old-secret"})
	output := httptest.NewRecorder()
	fixture.server.ServeHTTP(output, request)
	cookies := output.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "autopaws_token" || cookies[0].Value != "" || cookies[0].MaxAge != -1 || cookies[0].Path != "/" {
		t.Fatalf("cookie removal = %v", cookies)
	}
}
