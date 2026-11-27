//go:build (linux || darwin) && (amd64 || arm64)

package serial

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func testTTYReads(t *testing.T, master *os.File, path string) {
	t.Helper()
	opened, err := openDevice(t.Context(), path, baudRates[9600])
	if err != nil {
		t.Fatal(err)
	}
	d := opened.(*ttyDevice)
	t.Cleanup(func() { _ = d.Close() })
	if err := d.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	pending := []byte{0x57, 0xab, 0x00}
	if _, err := master.Write(pending); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	if n, err := d.Read(buf); err != nil || !bytes.Equal(buf[:n], pending) {
		t.Fatalf("partial read = % x, %v; want % x", buf[:n], err, pending)
	}
	if n, err := d.Read(nil); n != 0 || err != nil {
		t.Fatalf("zero-length read = %d, %v", n, err)
	}
	if _, err := master.Write([]byte("stale")); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Read(buf[:1]); n != 1 || err != nil || buf[0] != 's' {
		t.Fatalf("stale input arrival = % x, %v", buf[:n], err)
	}
	if err := d.flushInput(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := master.Write([]byte("fresh")); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Read(buf); err != nil || string(buf[:n]) != "fresh" {
		t.Fatalf("read after input drain = %q, %v", buf[:n], err)
	}
	if err := d.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Read(buf); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("blocked read deadline = %d, %v", n, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := d.flushInput(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled input drain = %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Read(buf); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("read after close = %v", err)
	}
	if err := d.SetReadDeadline(time.Time{}); err == nil {
		t.Fatalf("read deadline after close = %v", err)
	}
}
