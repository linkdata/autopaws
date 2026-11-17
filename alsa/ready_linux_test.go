//go:build linux && (amd64 || arm64)

package alsa

import (
	"errors"
	"testing"
	"time"
)

func TestReadyTryNextOwnsNoAdditionalSampleQueue(t *testing.T) {
	capture := queueCapture()
	subscription := capture.Subscribe()
	ready := subscription.Ready()
	if _, ok, err := subscription.TryNext(); ok || err != nil {
		t.Fatalf("empty dequeue: %t,%v", ok, err)
	}
	capture.publish([]byte{1}, 960, int64(9980*time.Millisecond))
	select {
	case <-ready:
	default:
		t.Fatal("publication did not wake the prior readiness observation")
	}
	block, ok, err := subscription.TryNext()
	if !ok || err != nil || block.Sequence != 0 || len(subscription.queue) != 0 || subscription.frames != 0 {
		t.Fatalf("dequeue did not consume original queue: %+v,%t,%v", block, ok, err)
	}
	ready = subscription.Ready()
	subscription.Close()
	<-ready
	if _, ok, err := subscription.TryNext(); ok || !errors.Is(err, ErrClosed) {
		t.Fatalf("closed dequeue: %t,%v", ok, err)
	}
}
