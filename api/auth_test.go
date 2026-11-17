//go:build linux && (amd64 || arm64)

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/linkdata/autopaws/apiwire"
)

func TestAuthenticationPrecedesDispatchAndDoesNotLogToken(t *testing.T) {
	var logs bytes.Buffer
	server, _, transport := bareServer(t, Options{Token: "a+;å% secret", Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	for _, vector := range []struct {
		token  any
		omit   bool
		method string
		code   apiwire.Code
	}{
		{omit: true, method: "send_key", code: apiwire.ErrUnauthorized},
		{token: nil, method: "send_key", code: apiwire.ErrUnauthorized},
		{token: "", method: "send_key", code: apiwire.ErrUnauthorized},
		{token: "wrong-token-should-not-log", method: "send_key", code: apiwire.ErrUnauthorized},
		{token: "wrong-token-should-not-log", method: "unknown", code: apiwire.ErrUnauthorized},
		{token: "a+;å% secret", method: "ping"},
		{token: "a+;å% secret", method: "unknown", code: apiwire.ErrUnknownMethod},
	} {
		request := map[string]any{"id": 3, "method": vector.method, "params": map[string]string{"key": "a"}}
		if !vector.omit {
			request["token"] = vector.token
		}
		if vector.method == "ping" {
			request["params"] = struct{}{}
		}
		line, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		left, right := net.Pipe()
		if err := right.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { server.handleConn(context.Background(), left); close(done) }()
		writeTest(t, right, string(line)+"\n")
		response := replyTest(t, right)
		_ = right.Close()
		<-done
		if response.Error != vector.code || response.OK != (vector.code == "") || string(response.ID) != "3" {
			t.Fatalf("authentication response = %+v", response)
		}
		if vector.code == apiwire.ErrUnauthorized && response.Detail != "" {
			t.Fatalf("unauthorized response should be generic: %+v", response)
		}
	}
	if transport.prepares != 0 || transport.writes != 0 {
		t.Fatal("unauthenticated request touched HID")
	}
	if strings.Contains(logs.String(), "a+;å% secret") || strings.Contains(logs.String(), "wrong-token") {
		t.Fatalf("authentication leaked into logs: %s", logs.String())
	}
}
