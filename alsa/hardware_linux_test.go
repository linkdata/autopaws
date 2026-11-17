//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

// These tests never auto-select a host microphone or capture PCM. Hardware
// validation explicitly names the intended dedicated device; ordinary CI skips.
func hardwarePath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("AUTOPAWS_ALSA_TEST_DEVICE")
	if path == "" {
		t.Skip("set AUTOPAWS_ALSA_TEST_DEVICE to explicitly select a capture PCM")
	}
	return path
}

func TestHardwareCaptureBusyAndShutdown(t *testing.T) {
	path := hardwarePath(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	capture, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	config := capture.Config()
	started := time.Now()
	other, err := Open(ctx, path)
	busyElapsed := time.Since(started)
	if err == nil {
		_ = other.Close()
		t.Fatal("selected PCM allows another capture; select a dedicated exclusive substream for the busy test")
	}
	if !errors.Is(err, ErrBusy) || busyElapsed > 100*time.Millisecond {
		t.Fatalf("busy open=%v after %s", err, busyElapsed)
	}
	subscription := capture.Subscribe()
	var total uint64
	for total < uint64(config.Rate)/4 {
		block, err := subscription.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(block.Data) != int(block.Frames)*int(config.Channels)*2 || block.TimestampNS <= 0 || block.Config != config {
			t.Fatalf("invalid hardware block metadata: frames=%d bytes=%d timestamp=%d config=%+v", block.Frames, len(block.Data), block.TimestampNS, block.Config)
		}
		total += uint64(block.Frames)
	}
	started = time.Now()
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	// This is an observation/requirement for the explicitly selected hardware,
	// not a claim that every Linux kernel operation has a universal deadline.
	shutdownElapsed := time.Since(started)
	if shutdownElapsed > time.Second {
		t.Fatalf("selected hardware shutdown took %s", shutdownElapsed)
	}
	t.Logf("config=%+v frames=%d busy-open=%s shutdown=%s", config, total, busyElapsed, shutdownElapsed)
}

func TestHardwareOverrunRecovery(t *testing.T) {
	path := hardwarePath(t)
	ops := systemKernel()
	actualIOCTL := ops.ioctl
	startFirst, stalled, resume := make(chan struct{}), make(chan struct{}), make(chan struct{})
	start := sync.OnceFunc(func() { close(startFirst) })
	defer start()
	unblock := sync.OnceFunc(func() { close(resume) })
	defer unblock()
	var reads atomic.Uint32
	ops.ioctl = func(fd int, request uintptr, pointer unsafe.Pointer) error {
		if request == ioctlRead {
			switch reads.Add(1) {
			case 1:
				<-startFirst
			case 2:
				close(stalled)
				<-resume
			}
		}
		return actualIOCTL(fd, request, pointer)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	capture, err := open(ctx, path, ops)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	subscription := capture.Subscribe()
	before := capture.Config()
	start()
	if _, err := subscription.Next(ctx); err != nil {
		t.Fatal(err)
	}
	receive(t, stalled)
	bufferNS, err := framesNS(uint64(before.BufferFrames), before.Rate)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Duration(bufferNS) + 100*time.Millisecond)
	unblock()
	block, err := subscription.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if capture.Stats().Overruns == 0 || block.Config.Generation == before.Generation {
		t.Fatalf("overrun did not recover: stats=%+v before=%+v block=%+v", capture.Stats(), before, block.Config)
	}
	t.Logf("overrun recovery: stats=%+v generation=%d->%d", capture.Stats(), before.Generation, block.Config.Generation)
}
