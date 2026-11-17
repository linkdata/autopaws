//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"errors"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestTerminalFailureWakesSubscribersBeforeRetainedClose(t *testing.T) {
	fake, ops := fakeOperations()
	closeEntered, finishClose := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(finishClose) })
	defer release()
	closeFailure := errors.New("retained close failed")
	fake.closeErr = closeFailure
	originalClose := ops.close
	ops.close = func(fd int) error {
		close(closeEntered)
		<-finishClose
		return originalClose(fd)
	}
	capture, err := open(context.Background(), "/fake/pcm", ops)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { release(); _ = capture.Close() })
	existing := capture.Subscribe()
	sendRead(t, fake, readEvent{err: syscall.ENODEV})
	receive(t, closeEntered)
	for _, subscription := range []*Subscription{existing, capture.Subscribe()} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		_, err := subscription.Next(ctx)
		cancel()
		if !errors.Is(err, ErrDisconnected) || !errors.Is(err, syscall.ENODEV) || errors.Is(err, closeFailure) {
			t.Fatalf("terminal read failure delayed or lost before close: %v", err)
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- capture.Close() }()
	select {
	case <-capture.done:
		t.Fatal("capture abandoned retained close ownership")
	default:
	}
	if _, closes := fake.snapshot(); closes != 0 {
		t.Fatal("retained close was reported finished")
	}
	release()
	if err := receive(t, closed); !errors.Is(err, closeFailure) || !errors.Is(err, syscall.ENODEV) || !errors.Is(err, ErrDisconnected) {
		t.Fatalf("Close lost terminal/driver-close errors: %v", err)
	}
	if _, closes := fake.snapshot(); closes != 1 {
		t.Fatalf("descriptor closed %d times", closes)
	}
	if _, err := capture.Subscribe().Next(t.Context()); !errors.Is(err, closeFailure) || !errors.Is(err, ErrDisconnected) {
		t.Fatalf("completed terminal result lost close error: %v", err)
	}
}

func TestReadAdmittedBeforeCloseCannotRestartAfterDrop(t *testing.T) {
	fake, ops := fakeOperations()
	fake.dropEntered = make(chan struct{})
	admitted, enterKernel := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(enterKernel) })
	defer release()
	readResult := make(chan error, 1)
	originalIOCTL := ops.ioctl
	ops.ioctl = func(fd int, request uintptr, argument unsafe.Pointer) error {
		if request == ioctlRead {
			// read already checked lifetime and released kernelMu. Hold it at
			// kernel entry until DROP has moved the PCM from PREPARED to SETUP.
			close(admitted)
			<-enterKernel
			err := originalIOCTL(fd, request, argument)
			readResult <- err
			return err
		}
		return originalIOCTL(fd, request, argument)
	}
	capture, err := open(context.Background(), "/fake/pcm", ops)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { release(); _ = capture.Close() })
	receive(t, admitted)
	closed := make(chan error, 1)
	go func() { closed <- capture.Close() }()
	receive(t, fake.dropEntered)
	// The fake models the kernel's SETUP -> EBADFD rejection. The real ALSA
	// read path checks accessible state under the stream lock before its
	// PREPARED-only autostart (pcm_lib.c); DROP sets SETUP (pcm_native.c).
	release()
	if err := receive(t, readResult); !errors.Is(err, syscall.EBADFD) {
		t.Fatalf("read after DROP restarted instead of rejecting SETUP: %v", err)
	}
	if err := receive(t, closed); err != nil {
		t.Fatalf("shutdown failed after a late rejected read: %v", err)
	}
	events, closes := fake.snapshot()
	if closes != 1 || !slices.Equal(events[len(events)-3:], []string{"DROP", "READI", "close"}) {
		t.Fatalf("late read lost shutdown ownership/order: %v, closes=%d", events, closes)
	}
}
