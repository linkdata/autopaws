//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestRepeatedRecoverableReadsWithoutPCMTerminateAfterOneRecovery(t *testing.T) {
	for _, faults := range [][2]error{
		{syscall.EPIPE, syscall.EPIPE},
		{syscall.ESTRPIPE, syscall.ESTRPIPE},
		{syscall.EPIPE, syscall.ESTRPIPE},
	} {
		t.Run(faults[0].Error()+" then "+faults[1].Error(), func(t *testing.T) {
			fake, ops := fakeOperations()
			gate := make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			defer release()
			var reads atomic.Int32
			original := ops.ioctl
			ops.ioctl = func(fd int, request uintptr, argument unsafe.Pointer) error {
				if request != ioctlRead {
					return original(fd, request, argument)
				}
				<-gate
				count := reads.Add(1)
				if count == 1 {
					return faults[0]
				}
				if count <= 8 {
					return faults[1]
				}
				return syscall.ENODEV // Old behavior stays finite but spins8 recoveries.
			}
			capture, err := open(t.Context(), "/fake/pcm", ops)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { release(); _ = capture.Close() })
			subscription := capture.Subscribe()
			generation := capture.Config().Generation
			release()
			_, err = subscription.Next(t.Context())
			if !errors.Is(err, faults[1]) || errors.Is(err, syscall.ENODEV) {
				t.Fatalf("second read did not retain its terminal fault: %v", err)
			}
			receive(t, capture.done)
			events, closes := fake.snapshot()
			prepares := 0
			for _, event := range events {
				if event == "PREPARE" {
					prepares++
				}
			}
			if reads.Load() != 2 || prepares != 2 || closes != 1 || capture.Config().Generation != generation+1 {
				t.Fatalf("no-progress recovery storm: reads%d prepares%d closes%d generation%d->%d", reads.Load(), prepares, closes, generation, capture.Config().Generation)
			}
			if err := capture.Close(); !errors.Is(err, faults[1]) {
				t.Fatalf("Close lost read fault: %v", err)
			}
		})
	}
}

func TestValidPCMAllowsLaterIndependentOverrunRecovery(t *testing.T) {
	capture, fake := openFake(t)
	subscription := capture.Subscribe()
	previous := capture.Config().Generation
	for _, fault := range []readEvent{{err: syscall.EPIPE}, {frames: 960, state: stateSuspended}} {
		sendRead(t, fake, fault)
		sendRead(t, fake, readEvent{frames: 960, state: stateRunning, timestamp: int64(10 * time.Second)})
		block, err := subscription.Next(t.Context())
		if err != nil || block.Config.Generation != previous+1 || block.Sequence != 0 {
			t.Fatalf("independent recovery failed: %+v,%v", block, err)
		}
		previous = block.Config.Generation
	}
	stats := capture.Stats()
	if stats.Overruns != 1 || stats.Suspends != 1 {
		t.Fatalf("recovery counts %+v", stats)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	events, _ := fake.snapshot()
	if !slices.Contains(events, "DROP") {
		t.Fatal("normal recovery did not preserve owned shutdown")
	}
}

func TestRepeatedStatusRecoverableWithoutPCMAlsoTerminates(t *testing.T) {
	capture, fake := openFake(t)
	subscription := capture.Subscribe()
	sendRead(t, fake, readEvent{frames: 960, state: stateXRun})
	sendRead(t, fake, readEvent{frames: 960, state: stateSuspended})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := subscription.Next(ctx); !errors.Is(err, syscall.ESTRPIPE) {
		t.Fatalf("status-only failure did not stop: %v", err)
	}
	receive(t, capture.done)
}

func TestTemporaryReadErrorsDoNotCountAsPCMProgress(t *testing.T) {
	capture, fake := openFake(t)
	subscription := capture.Subscribe()
	for _, fault := range []error{syscall.EPIPE, syscall.EINTR, syscall.EAGAIN, syscall.EPIPE} {
		sendRead(t, fake, readEvent{err: fault})
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := subscription.Next(ctx); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("temporary errors renewed recovery without samples: %v", err)
	}
	receive(t, capture.done)
	if stats := capture.Stats(); stats.Overruns != 1 {
		t.Fatalf("temporary errors admitted another recovery: %+v", stats)
	}
}
