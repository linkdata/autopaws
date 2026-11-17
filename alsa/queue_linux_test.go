//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func queueCapture() *Capture {
	return &Capture{
		config: Config{Rate: 48_000, Channels: 2, Generation: generations.Add(1)},
		subs:   make(map[*Subscription]struct{}), lifetime: context.Background(),
		ops: kernel{now: func() (int64, error) { return int64(10 * time.Second), nil }, ioctl: func(int, uintptr, unsafe.Pointer) error { return nil }},
	}
}

func TestQueuesBoundAudioDurationDropOldestAndOwnBytes(t *testing.T) {
	capture := queueCapture()
	first, second := capture.Subscribe(), capture.Subscribe()
	source := []byte{1, 2, 3, 4}
	for range 6 {
		capture.publish(source, 960, int64(9980*time.Millisecond))
	}
	if len(first.queue) != 5 || first.frames != 4800 || len(second.queue) != 5 {
		t.Fatalf("queue exceeded 100ms: blocks=%d frames=%d", len(first.queue), first.frames)
	}
	source[0] = 99
	for sequence := uint64(1); sequence <= 5; sequence++ {
		block, err := first.Next(context.Background())
		if err != nil || block.Sequence != sequence || block.Data[0] != 1 {
			t.Fatalf("oldest-first/owned data: block=%+v err=%v", block, err)
		}
		block.Data[0] = 88
	}
	block, err := second.Next(context.Background())
	if err != nil || block.Sequence != 1 || block.Data[0] != 1 {
		t.Fatalf("subscriber bytes aliased: %+v,%v", block, err)
	}
	// A single over-budget source block is dropped instead of violating the cap.
	capture.publish(source, 4801, int64(9980*time.Millisecond))
	if first.frames != 0 || len(first.queue) != 0 {
		t.Fatal("oversized block entered queue")
	}
	if len(second.queue) != 4 {
		t.Fatal("oversized block discarded already queued valid audio")
	}
	first.Close()
	first.Close()
	if _, err := first.Next(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed subscription=%v", err)
	}
	if len(capture.subs) != 1 {
		t.Fatal("closing one subscription affected another")
	}
}

func TestStaleSamplesExpireAndRecoveryFlushesGeneration(t *testing.T) {
	capture := queueCapture()
	subscription := capture.Subscribe()
	capture.publish([]byte{1}, 960, int64(9800*time.Millisecond))
	capture.publish([]byte{2}, 960, int64(9980*time.Millisecond))
	block, err := subscription.Next(context.Background())
	if err != nil || block.Sequence != 1 || block.Data[0] != 2 {
		t.Fatalf("stale audio replayed: %+v,%v", block, err)
	}
	capture.publish([]byte{3}, 960, int64(9980*time.Millisecond))
	previous := capture.Config().Generation
	if err := capture.recover(syscall.EPIPE); err != nil {
		t.Fatal(err)
	}
	if len(subscription.queue) != 0 || subscription.frames != 0 || capture.Config().Generation == previous {
		t.Fatal("generation reset retained old queue")
	}
	capture.publish([]byte{4}, 960, int64(9980*time.Millisecond))
	block, err = subscription.Next(context.Background())
	if err != nil || block.Sequence != 0 || block.Config.Generation == previous || block.Data[0] != 4 {
		t.Fatalf("new generation block=%+v,%v", block, err)
	}
	// Future source generations (a reopened device) also start with empty queues.
	replacement := queueCapture()
	if replacement.Config().Generation == capture.Config().Generation || len(replacement.Subscribe().queue) != 0 {
		t.Fatal("replacement source reused old generation or audio")
	}
}

func TestNextCancellationClockErrorAndCloseBroadcast(t *testing.T) {
	capture := queueCapture()
	subscription := capture.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := subscription.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	failure := errors.New("clock failed")
	capture.ops.now = func() (int64, error) { return 0, failure }
	if _, err := subscription.Next(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	started := make(chan struct{}, 2)
	capture.ops.now = func() (int64, error) { started <- struct{}{}; return 1, nil }
	var waiters sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		waiters.Go(func() { _, err := subscription.Next(context.Background()); results <- err })
	}
	receive(t, started)
	receive(t, started)
	subscription.Close()
	for range 2 {
		if err := receive(t, results); !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
	}
	waiters.Wait()
	// Empty-queue cancellation wakes the existing subscriber rather than
	// cancelling the shared capture source.
	other := capture.Subscribe()
	clockEntered, clockReturn := make(chan struct{}), make(chan struct{})
	capture.ops.now = func() (int64, error) {
		close(clockEntered)
		<-clockReturn
		return 1, nil
	}
	ctx, cancel = context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := other.Next(ctx); result <- err }()
	receive(t, clockEntered)
	cancel()
	close(clockReturn)
	if err := receive(t, result); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestStoppedSourceRejectsNewReadsRecoveryAndPublication(t *testing.T) {
	capture := queueCapture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	capture.lifetime = ctx
	if err := capture.readLoop(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := capture.read(nil, Config{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after stop=%v", err)
	}
	if err := capture.recover(syscall.ESTRPIPE); err != nil {
		t.Fatal(err)
	}
	subscription := capture.Subscribe()
	capture.closing = true
	capture.publish([]byte{1}, 1, 1)
	if len(subscription.queue) != 0 {
		t.Fatal("stopped source published another block")
	}
}
