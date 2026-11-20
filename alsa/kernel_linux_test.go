//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"errors"
	"math"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/linkdata/autopaws/internal/monotime"
)

func TestSystemKernelWithoutOpeningPCM(t *testing.T) {
	if _, err := Open(context.Background(), t.TempDir()+"/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing PCM=%v", err)
	}
	if _, err := Open(context.Background(), "/dev/null"); !errors.Is(err, syscall.ENOTTY) {
		t.Fatalf("/dev/null HW_REFINE=%v", err)
	}
	before, err := monotime.Now()
	if err != nil || before <= 0 {
		t.Fatalf("monotonic clock=%d,%v", before, err)
	}
	after, err := systemKernel().now()
	if err != nil || after < before {
		t.Fatalf("monotonic clock went backwards=%d,%v", after, err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := writer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	raw, err := reader.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var available int32
	var ioctlErr error
	if err := raw.Control(func(fd uintptr) { ioctlErr = ioctl(int(fd), syscall.TIOCINQ, unsafe.Pointer(&available)) }); err != nil || ioctlErr != nil || available != 3 {
		t.Fatalf("pointer-safe generic ioctl=%d,%v/%v", available, err, ioctlErr)
	}
}

func TestCallRetriesInterruptedKernelCallsAndChecksContext(t *testing.T) {
	calls := 0
	ops := kernel{ioctl: func(int, uintptr, unsafe.Pointer) error {
		calls++
		if calls == 1 {
			return syscall.EINTR
		}
		return nil
	}}
	if err := call(context.Background(), ops, 0, ioctlPrepare, nil); err != nil || calls != 2 {
		t.Fatalf("retry=%v calls=%d", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := call(ctx, kernel{}, 0, ioctlPrepare, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	ops.ioctl = func(int, uintptr, unsafe.Pointer) error { cancel(); return syscall.EINTR }
	if err := call(ctx, ops, 0, ioctlPrepare, nil); !errors.Is(err, context.Canceled) || !errors.Is(err, syscall.EINTR) {
		t.Fatalf("interrupted cancellation=%v", err)
	}
	if err := kernelError("test", "fake", syscall.ENXIO); !errors.Is(err, ErrDisconnected) || !errors.Is(err, syscall.ENXIO) {
		t.Fatal(err)
	}
}

func TestTimestampAndFrameArithmetic(t *testing.T) {
	for _, timestamp := range []syscall.Timespec{{Sec: -1}, {Nsec: -1}, {Nsec: int64(time.Second)}, {Sec: math.MaxInt64}} {
		if _, err := timestampNS(timestamp); !errors.Is(err, ErrTimestamp) {
			t.Fatalf("timestamp=%+v err=%v", timestamp, err)
		}
	}
	for _, test := range []struct {
		frames uint64
		rate   uint32
	}{
		{1, 0}, {math.MaxUint64, 1}, {math.MaxInt64 / uint64(time.Second), 1},
	} {
		_, err := framesNS(test.frames, test.rate)
		if test.frames == math.MaxInt64/uint64(time.Second) {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrTimestamp) {
			t.Fatalf("duration=%+v err=%v", test, err)
		}
	}
	if _, err := framesNS(math.MaxInt64/uint64(time.Second)+1, 1); !errors.Is(err, ErrTimestamp) {
		t.Fatalf("signed duration overflow=%v", err)
	}
	config := Config{Rate: 48_000, BufferFrames: 3840}
	for _, snapshot := range []status{
		{Delay: -1},
		{Available: 3841},
		{Timestamp: syscall.Timespec{Nsec: -1}},
		{Timestamp: syscall.Timespec{Sec: 1}, Delay: math.MaxInt64},
		{Timestamp: syscall.Timespec{}, Delay: 0},
	} {
		if _, err := firstSampleNS(snapshot, 960, config); !errors.Is(err, ErrTimestamp) {
			t.Fatalf("invalid sample timestamp snapshot=%+v err=%v", snapshot, err)
		}
	}
}

func TestReadStatusAndRecoveryFailuresBecomeTerminal(t *testing.T) {
	for _, stage := range []string{"STATUS", "PREPARE"} {
		capture, fake := openFake(t)
		subscription := capture.Subscribe()
		fake.mu.Lock()
		fake.fail = stage
		fake.mu.Unlock()
		if stage == "PREPARE" {
			sendRead(t, fake, readEvent{err: syscall.EPIPE})
		} else {
			sendRead(t, fake, readEvent{frames: 960, state: stateRunning})
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if _, err := subscription.Next(ctx); !errors.Is(err, fake.failure) {
			t.Fatalf("%s failure=%v", stage, err)
		}
		cancel()
		receive(t, capture.done)
		if err := capture.Close(); !errors.Is(err, fake.failure) {
			t.Fatalf("Close hid terminal failure=%v", err)
		}
	}
}

func TestCloseReportsDropAndWorkerCloseFailures(t *testing.T) {
	capture, fake := openFake(t)
	receive(t, fake.reading)
	fake.mu.Lock()
	fake.closeErr = errors.New("close failure")
	closeFailure := fake.closeErr
	fake.fail = "DROP"
	fake.dropEntered = make(chan struct{})
	dropped := fake.dropEntered
	fake.mu.Unlock()
	closed := make(chan error, 1)
	go func() { closed <- capture.Close() }()
	receive(t, dropped)
	// A failed DROP cannot force a blocked driver to return. Keep ownership
	// and model the driver's eventual return without closing/reusing its fd.
	sendRead(t, fake, readEvent{err: syscall.EINTR})
	if err := receive(t, closed); !errors.Is(err, fake.failure) || !errors.Is(err, closeFailure) {
		t.Fatalf("Close lost failures=%v", err)
	}
}
