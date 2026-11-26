//go:build (linux || darwin || windows) && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/internal/config"
	"github.com/linkdata/autopaws/internal/token"
)

func TestRuntimeReadsTokenBeforeHardwareAndOnlyOnce(t *testing.T) {
	cfg := runtimeConfig()
	cfg.TokenFile = filepath.Join(t.TempDir(), "secret")
	if err := serveWith(t.Context(), cfg, io.Discard, runtimeFactories{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token read did not precede device creation: %v", err)
	}
	if err := os.WriteFile(cfg.TokenFile, []byte("  a+;å% secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertRuntimeTokenAuthentication(t, cfg)
}

func TestRuntimeUsesDirectTokenBeforeHardware(t *testing.T) {
	cfg := runtimeConfig()
	for _, secret := range []string{" \r\n\t", "bad\xffsecret"} {
		cfg.Token = secret
		if err := serveWith(t.Context(), cfg, io.Discard, runtimeFactories{}); !errors.Is(err, token.ErrInvalid) || strings.Contains(err.Error(), secret) {
			t.Fatalf("invalid token was accepted or leaked before hardware: %v", err)
		}
	}
	cfg.Token, cfg.TokenFile = "not-logged", filepath.Join(t.TempDir(), "missing")
	if err := serveWith(t.Context(), cfg, io.Discard, runtimeFactories{}); !errors.Is(err, token.ErrConflict) || strings.Contains(err.Error(), cfg.Token) {
		t.Fatalf("conflicting token sources were accepted or leaked before hardware: %v", err)
	}
	cfg.TokenFile = ""
	cfg.Token = "  a+;å% secret\n"
	assertRuntimeTokenAuthentication(t, cfg)
}

func assertRuntimeTokenAuthentication(t *testing.T, cfg config.Config) {
	t.Helper()
	cfg.Web = "127.0.0.1:0"
	port := &runtimeTransport{}
	run := startViewerRuntime(t, cfg, port, newRuntimeVideoSource(), nil)
	if cfg.TokenFile != "" {
		// Removing the file proves both listeners retain the startup value.
		if err := os.Remove(cfg.TokenFile); err != nil {
			t.Fatal(err)
		}
	}
	for _, secret := range []string{"", "wrong"} {
		if err := runtimeCall(apiclient.NewWithToken(run.api.Addr().String(), secret), "send_key", map[string]string{"key": "a"}, nil); !errors.Is(err, apiwire.ErrUnauthorized) {
			t.Fatalf("missing/wrong API token = %v", err)
		}
	}
	if prepares, _, frames := port.snapshot(); prepares != 0 || len(frames) != 0 {
		t.Fatal("rejected API token touched HID")
	}
	client := apiclient.NewWithToken(run.api.Addr().String(), "a+;å% secret")
	for range 2 {
		if err := runtimeCall(client, "send_key", map[string]any{"key": "a"}, nil); err != nil {
			t.Fatalf("authenticated API call = %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	address := "ws://" + run.web.Addr().String() + "/ws"
	conn, response, err := websocket.Dial(ctx, address, nil)
	if conn != nil {
		_ = conn.CloseNow()
		t.Fatal("missing web token admitted")
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing web token = %v, %v", response, err)
	}
	conn, _, err = websocket.Dial(ctx, address, &websocket.DialOptions{Subprotocols: []string{"autopaws", "autopaws-token." + hex.EncodeToString([]byte("a+;å% secret"))}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("authenticated web config = %v", err)
	}
	_ = conn.CloseNow()
	if err := stopViewerRuntime(t, run); err != nil {
		t.Fatal(err)
	}
}

type nonTCPAddress string

func (address nonTCPAddress) Network() string { return "fixture" }
func (address nonTCPAddress) String() string  { return string(address) }

func TestWarningUsesActualBoundAddressWithoutDNS(t *testing.T) {
	for _, vector := range []struct {
		address net.Addr
		secret  string
		warn    bool
	}{
		{&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9329}, "", false},
		{&net.TCPAddr{IP: net.ParseIP("::1"), Port: 9330}, "", false},
		{&net.TCPAddr{IP: net.ParseIP("::ffff:127.0.0.1")}, "", false},
		{&net.TCPAddr{IP: net.IPv4zero}, "", true},
		{&net.TCPAddr{IP: net.IPv6zero}, "", true},
		{&net.TCPAddr{IP: net.ParseIP("192.0.2.1")}, "", true},
		{&net.TCPAddr{}, "", true},
		{nonTCPAddress("unresolved.invalid:9329"), "", true},
		{&net.TCPAddr{IP: net.IPv4zero}, "secret-not-logged", false},
	} {
		var output bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&output, nil))
		warnExposed(logger, "API", vector.address, vector.secret)
		if (output.Len() != 0) != vector.warn || strings.Contains(output.String(), "secret-not-logged") {
			t.Fatalf("warning for %v = %s", vector.address, output.String())
		}
		if vector.warn && (!strings.Contains(output.String(), "without authentication") || !strings.Contains(output.String(), vector.address.String())) {
			t.Fatalf("warning omitted bound address: %s", output.String())
		}
	}
}
