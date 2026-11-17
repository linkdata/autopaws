//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"math"
	"testing"
	"time"
	"unsafe"
)

func TestNewlyCapturedHundredMillisecondPeriodRemainsUsable(t *testing.T) {
	fake, ops := fakeOperations()
	fake.mutate = func(request uintptr, argument unsafe.Pointer) {
		if request == ioctlHWParams {
			params := (*hwParams)(argument)
			params.setRange(paramPeriodSize, 4800, 4800)
			params.setRange(paramBufferSize, 19_200, 19_200)
		}
	}
	fake.now += int64(time.Millisecond) // STATUS and queue dequeue are not simultaneous.
	capture, err := open(context.Background(), "/fake/pcm", ops)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	subscription := capture.Subscribe()
	sendRead(t, fake, readEvent{frames: 4800, state: stateRunning, timestamp: int64(10 * time.Second)})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	block, err := subscription.Next(ctx)
	if err != nil || block.Frames != 4800 || block.Config.PeriodFrames != 4800 || block.TimestampNS != int64(9900*time.Millisecond) {
		t.Fatalf("fresh 100ms period expired at acquisition: %+v, %v", block, err)
	}
}

func TestExpiryUsesEndOfBlockBoundaryWithoutTimestampOverflow(t *testing.T) {
	const duration = int64(20 * time.Millisecond)
	const budget = int64(ServerQueueBudget)
	for _, vector := range []struct {
		name   string
		now    int64
		first  int64
		expire bool
	}{
		{"before boundary", int64(time.Second), int64(time.Second) - duration - budget + 1, false},
		{"exact boundary", int64(time.Second), int64(time.Second) - duration - budget, false},
		{"after boundary", int64(time.Second), int64(time.Second) - duration - budget - 1, true},
		{"future timestamp", int64(time.Second), int64(time.Second) + 1, false},
		{"end timestamp beyond int64", math.MaxInt64, math.MaxInt64 - duration/2, false},
		{"large elapsed age", math.MaxInt64, 0, true},
	} {
		t.Run(vector.name, func(t *testing.T) {
			capture := queueCapture()
			capture.ops.now = func() (int64, error) { return vector.now, nil }
			subscription := capture.Subscribe()
			capture.publish([]byte{1}, 960, vector.first)
			capture.publish([]byte{2}, 960, vector.now-duration)
			block, err := subscription.Next(t.Context())
			want := byte(1)
			if vector.expire {
				want = 2
			}
			if err != nil || len(block.Data) != 1 || block.Data[0] != want {
				t.Fatalf("end-of-block expiry boundary = %+v, %v; want marker %d", block, err, want)
			}
		})
	}
}
