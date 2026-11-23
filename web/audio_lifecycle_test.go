//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/linkdata/autopaws/alsa"
	"github.com/linkdata/autopaws/capture"
)

type audioCancelAtDone struct {
	context.Context
	cancel context.CancelFunc
}

func (ctx audioCancelAtDone) Done() <-chan struct{} {
	ctx.cancel()
	return ctx.Context.Done()
}

func TestAudioControlCancellationAtEmptyAdmissionCannotLoseWakeup(t *testing.T) {
	video, err := capture.New(capture.Options{Width: 128, Height: 96})
	if err != nil {
		t.Fatal(err)
	}
	defer video.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	manager := newAudioManager(audioCancelAtDone{ctx, cancel}, video, AudioOptions{Device: "off"}, nil)
	receive(t, manager.done)
}

func TestAudioLazilyStartsAndStopsWithWebDemand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newAudioTestSource(1)
		opened := make(chan string, 2)
		manager, _ := audioManagerFixture(t, AudioOptions{Device: "/dev/fixed", OpenSource: func(_ context.Context, path string) (AudioSource, error) {
			opened <- path
			return source, nil
		}})
		synctest.Wait()
		select {
		case <-opened:
			t.Fatal("audio opened without web demand")
		default:
		}
		manager.subscribe()
		if path := receive(t, opened); path != "/dev/fixed" {
			t.Fatal(path)
		}
		waitAudioView(t, manager, true)
		manager.subscribe()
		manager.unsubscribe()
		synctest.Wait()
		select {
		case <-source.closeStarted:
			t.Fatal("one remaining client lost its PCM")
		default:
		}
		manager.unsubscribe()
		receive(t, source.closed)
		waitAudioView(t, manager, false)
	})
}

func TestAudioSelectedVideoChangeInvalidatesBeforeOwnedCloseCompletes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old, current := newAudioTestSource(1), newAudioTestSource(2)
		gate := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(gate) })
		old.closeGate = gate
		opened := make(chan string, 4)
		resolved := make(chan string, 4)
		manager, video := audioManagerFixture(t, AudioOptions{
			ResolveAudio: func(_ context.Context, selected string) (string, error) {
				resolved <- selected
				return selected + "-pcm", nil
			},
			OpenSource: func(_ context.Context, path string) (AudioSource, error) {
				opened <- path
				if path == "/dev/video-a-pcm" {
					return old, nil
				}
				return current, nil
			},
		})
		t.Cleanup(unblock)
		manager.subscribe()
		if selected := receive(t, resolved); selected != "/dev/video-a" {
			t.Fatal(selected)
		}
		receive(t, opened)
		previous := waitAudioView(t, manager, true)
		if err := video.SetDevice(t.Context(), "/dev/video-b"); err != nil {
			t.Fatal(err)
		}
		// This check requires no scheduling of the watcher: accepted settings
		// alone invalidate delivery against the atomic current generation.
		if manager.current(previous) {
			t.Fatal("old audio remained deliverable after settings acceptance")
		}
		view, _ := manager.view(video.Info())
		if view.run != nil || view.config.Status != "unavailable" {
			t.Fatal("old source exposed while close still owns its PCM")
		}
		receive(t, old.closeStarted)
		synctest.Wait()
		select {
		case path := <-opened:
			t.Fatalf("opened replacement %s before old close returned", path)
		default:
		}
		unblock()
		if selected := receive(t, resolved); selected != "/dev/video-b" {
			t.Fatal(selected)
		}
		if path := receive(t, opened); path != "/dev/video-b-pcm" {
			t.Fatal(path)
		}
		if view := waitAudioView(t, manager, true); view.raw.Generation != 2 {
			t.Fatal(view)
		}
		manager.unsubscribe()
	})
}

func TestAudioExplicitPathReopensOnResolutionAndSkipsAutoResolver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := newAudioTestSource(1), newAudioTestSource(2)
		opened := make(chan string, 3)
		var count atomic.Int32
		manager, video := audioManagerFixture(t, AudioOptions{
			Device: "/dev/fixed",
			ResolveAudio: func(context.Context, string) (string, error) {
				t.Error("fixed path performed auto selection")
				return "", nil
			},
			OpenSource: func(_ context.Context, path string) (AudioSource, error) {
				opened <- path
				if count.Add(1) == 1 {
					return first, nil
				}
				return second, nil
			},
		})
		manager.subscribe()
		receive(t, opened)
		old := waitAudioView(t, manager, true)
		if err := video.SetResolution(t.Context(), 640, 480); err != nil {
			t.Fatal(err)
		}
		if manager.current(old) {
			t.Fatal("resolution generation retained old audio admission")
		}
		if path := receive(t, opened); path != "/dev/fixed" {
			t.Fatal(path)
		}
		if view := waitAudioView(t, manager, true); view.raw.Generation != 2 {
			t.Fatal(view)
		}
		manager.unsubscribe()
	})
}

func TestAudioOpenFailureDoesNotSpinAndNewActivationCanRepair(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newAudioTestSource(2)
		attempts := make(chan struct{}, 4)
		var count atomic.Int32
		manager, _ := audioManagerFixture(t, AudioOptions{Device: "/dev/fixed", OpenSource: func(context.Context, string) (AudioSource, error) {
			attempts <- struct{}{}
			if count.Add(1) == 1 {
				return nil, alsa.ErrBusy
			}
			return source, nil
		}})
		manager.subscribe()
		receive(t, attempts)
		synctest.Wait()
		if count.Load() != 1 {
			t.Fatal("failed source retried without a settings/demand transition")
		}
		waitAudioView(t, manager, false)
		manager.unsubscribe()
		manager.subscribe()
		receive(t, attempts)
		waitAudioView(t, manager, true)
		manager.unsubscribe()
	})
}

func TestAudioSwitchCancelsInFlightOpenAndClosesLateSourceBeforeReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		late, current := newAudioTestSource(1), newAudioTestSource(2)
		started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		var count atomic.Int32
		manager, video := audioManagerFixture(t, AudioOptions{Device: "/dev/fixed", OpenSource: func(ctx context.Context, _ string) (AudioSource, error) {
			if count.Add(1) == 1 {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release // An executing driver open retains ownership despite cancel.
				return late, nil
			}
			return current, nil
		}})
		t.Cleanup(unblock)
		manager.subscribe()
		receive(t, started)
		if err := video.SetDevice(t.Context(), "/dev/video-b"); err != nil {
			t.Fatal(err)
		}
		receive(t, canceled)
		if count.Load() != 1 {
			t.Fatal("replacement bypassed retained open ownership")
		}
		unblock()
		receive(t, late.closed)
		if view := waitAudioView(t, manager, true); view.raw.Generation != 2 {
			t.Fatal(view)
		}
		manager.unsubscribe()
	})
}

func TestAudioOffAndResolverFailuresNeverOpenPCM(t *testing.T) {
	for _, device := range []string{"off", ""} {
		t.Run(device, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var opens atomic.Int32
				manager, _ := audioManagerFixture(t, AudioOptions{
					Device:       device,
					ResolveAudio: func(context.Context, string) (string, error) { return "", errors.New("no USB audio") },
					OpenSource:   func(context.Context, string) (AudioSource, error) { opens.Add(1); return nil, nil },
				})
				manager.subscribe()
				synctest.Wait()
				view, _ := manager.view(manager.video.Info())
				want := "unavailable"
				if device == "off" {
					want = "off"
				}
				if view.config.Status != want || opens.Load() != 0 {
					t.Fatal(view.config, opens.Load())
				}
				manager.unsubscribe()
			})
		})
	}
}
