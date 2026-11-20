//go:build (linux || darwin) && (amd64 || arm64)

package web

import (
	"context"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/alsa"
)

func TestAudioRetryBacksOffOpenFailureAndResumesViewer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newAudioTestSource(2)
		attempts := make(chan time.Time, 4)
		var count atomic.Int32
		fixture := newFixture(t, Options{Audio: &AudioOptions{
			Device: "/dev/fake-audio", retryInterval: time.Second,
			OpenSource: func(context.Context, string) (AudioSource, error) {
				attempts <- time.Now()
				if count.Add(1) == 1 {
					return nil, alsa.ErrBusy
				}
				return source, nil
			},
		}})
		conn := newFakeSocket()
		startAudioWriter(t, fixture, conn)
		first := receive(t, attempts)
		receiveAudioConfig(t, conn, "unavailable")
		synctest.Wait()
		time.Sleep(time.Second - time.Nanosecond)
		if count.Load() != 1 {
			t.Fatal("failed open retried before its backoff")
		}
		time.Sleep(time.Nanosecond)
		if retried := receive(t, attempts); retried.Sub(first) < time.Second {
			t.Fatal("failed open retried before its backoff", retried.Sub(first))
		}
		if config := receiveAudioConfig(t, conn, "available"); config.Generation != "2" {
			t.Fatal(config)
		}
		source.publish(23)
		message := receive(t, conn.output)
		if message.kind != websocket.MessageBinary || message.data[0] != 2 || binary.LittleEndian.Uint64(message.data[8:]) != 2 || message.data[64] != 23 {
			t.Fatal("existing viewer did not resume current-generation PCM")
		}
		time.Sleep(3 * time.Second)
		if count.Load() != 2 {
			t.Fatal("healthy audio source was reopened")
		}
	})
}

func TestAudioReadFailureRetriesOnlyAfterCloseAndBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := newAudioTestSource(1), newAudioTestSource(2)
		gate := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(gate) })
		first.closeGate = gate
		attempts := make(chan struct{}, 4)
		var count atomic.Int32
		fixture := newFixture(t, Options{Audio: &AudioOptions{
			Device: "/dev/fake-audio", retryInterval: time.Second,
			OpenSource: func(context.Context, string) (AudioSource, error) {
				attempts <- struct{}{}
				if count.Add(1) == 1 {
					return first, nil
				}
				return second, nil
			},
		}})
		conn := newFakeSocket()
		startAudioWriter(t, fixture, conn)
		t.Cleanup(unblock)
		receive(t, attempts)
		receiveAudioConfig(t, conn, "available")
		first.fail(alsa.ErrDisconnected)
		receive(t, first.closeStarted)
		receiveAudioConfig(t, conn, "unavailable")
		time.Sleep(3 * time.Second)
		if count.Load() != 1 {
			t.Fatal("read failure retried while Close still owned the source")
		}
		unblock()
		receive(t, first.closed)
		synctest.Wait()
		time.Sleep(time.Second - time.Nanosecond)
		if count.Load() != 1 {
			t.Fatal("retry backoff elapsed before Close joined")
		}
		time.Sleep(time.Nanosecond)
		receive(t, attempts)
		if config := receiveAudioConfig(t, conn, "available"); config.Generation != "2" {
			t.Fatal(config)
		}
		second.publish(42)
		message := receive(t, conn.output)
		if message.kind != websocket.MessageBinary || message.data[0] != 2 || binary.LittleEndian.Uint64(message.data[8:]) != 2 || message.data[64] != 42 {
			t.Fatal("read failure did not recover the existing viewer")
		}
	})
}

func TestAudioRetryStopsWithoutDemandOrAfterCancellation(t *testing.T) {
	for _, mode := range []string{"off", "last-viewer", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := make(chan struct{}, 4)
				var count atomic.Int32
				options := &AudioOptions{
					Device: "/dev/fake-audio", retryInterval: time.Second,
					OpenSource: func(context.Context, string) (AudioSource, error) {
						count.Add(1)
						attempts <- struct{}{}
						return nil, alsa.ErrBusy
					},
				}
				if mode == "off" {
					options.Device = "off"
				}
				fixture := newFixture(t, Options{Audio: options})
				manager := fixture.server.audio
				manager.subscribe()
				var want int32
				if mode != "off" {
					receive(t, attempts)
					synctest.Wait()
					want = 1
				}
				switch mode {
				case "canceled":
					fixture.server.Close()
					receive(t, manager.done)
				case "last-viewer":
					manager.unsubscribe()
				}
				time.Sleep(3 * time.Second)
				if got := count.Load(); got != want {
					t.Fatalf("%s allowed a late open: got %d, want %d", mode, got, want)
				}
				if mode != "last-viewer" {
					manager.unsubscribe()
				}
			})
		})
	}
}
