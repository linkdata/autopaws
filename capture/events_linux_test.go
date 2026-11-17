//go:build linux && (amd64 || arm64)

package capture

import (
	"context"
	"errors"
	"testing"
)

func TestWatchSnapshotAndEventsUseExistingDeliverySlot(t *testing.T) {
	manager, err := New(Options{Width: 128, Height: 96})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	info, changed := manager.Watch()
	if info.Generation != 1 || !errors.Is(info.Err, ErrUnavailable) {
		t.Fatal(info)
	}
	if err := manager.SetDevice(context.Background(), "/dev/unopened"); err != nil {
		t.Fatal(err)
	}
	<-changed
	info, changed = manager.Watch()
	if info.Device != "/dev/unopened" || info.Generation != 2 {
		t.Fatal(info)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	<-changed
	info, _ = manager.Watch()
	if !errors.Is(info.Err, ErrClosed) {
		t.Fatal(info)
	}
	other, err := New(Options{Width: 128, Height: 96})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	subscription, _ := other.Subscribe()
	if subscription.Events() != subscription.frames {
		t.Fatal("events allocated a second frame queue")
	}
	event := <-subscription.Events()
	if event.Frame != nil || !errors.Is(event.Err, ErrUnavailable) {
		t.Fatal(event)
	}
	subscription.Close()
	if _, ok := <-subscription.Events(); ok {
		t.Fatal("closed subscription retained a readable event")
	}
}
