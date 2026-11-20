//go:build (linux || darwin) && (amd64 || arm64)

package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	for _, path := range []string{"/", "/viewer.mjs", "/viewer-helpers.mjs", "/viewer.css", "/manifest.json", "/sw.js", "/icon.svg", "/audio-playback.mjs", "/audio-worklet.mjs"} {
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
	for _, cookie := range []string{"", "other=value", "autopaws_token=wrong-secret", "autopaws_token=%GG", "autopaws_token="} {
		conn, response, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {cookie}}})
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
	cookie := "autopaws_token=" + url.PathEscape(secret)
	conn, _, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {cookie}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
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
