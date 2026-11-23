//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/alsa"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/v4l2"
)

func TestAudioRealWebSocketPCMAndCloseRetainSourceOwnershipWithoutHoldingSockets(t *testing.T) {
	source := newAudioTestSource(1)
	gate := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(gate) })
	source.closeGate = gate
	fixture := newFixture(t, Options{Audio: sourceAudioOptions(source)})
	t.Cleanup(unblock)
	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for {
		kind, data := readViewer(t, conn)
		var config struct{ Audio audioConfig }
		if kind != websocket.MessageText || json.Unmarshal(data, &config) != nil {
			t.Fatal("first media preceded audio configuration")
		}
		if config.Audio.Status == "available" {
			break
		}
	}
	source.publish(42)
	kind, data := readViewer(t, conn)
	if kind != websocket.MessageBinary || data[0] != 2 || binary.LittleEndian.Uint64(data[8:]) != 1 ||
		!bytes.Equal(data[64:], bytes.Repeat([]byte{42}, 960*4)) {
		t.Fatal("real WebSocket lost audio data or metadata")
	}
	fixture.server.Close()
	receive(t, source.closeStarted)
	if err := fixture.controller.Close(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { fixture.server.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("Wait abandoned the retained source Close")
	default:
	}
	readCtx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if _, _, err := conn.Read(readCtx); err == nil || errors.Is(readCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("source Close prevented independent socket cancellation: %v", err)
	}
	unblock()
	receive(t, waited)
}

func TestAudioBadFactoriesAndMalformedBlocksBecomeUnavailable(t *testing.T) {
	for _, mode := range []string{"nil-source", "bad-config", "bad-block"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				source := newAudioTestSource(1)
				options := sourceAudioOptions(source)
				switch mode {
				case "nil-source":
					options.OpenSource = func(context.Context, string) (AudioSource, error) { return nil, nil }
				case "bad-config":
					source.config.Channels = 3
				}
				fixture := newFixture(t, Options{Audio: options})
				conn := newFakeSocket()
				startAudioWriter(t, fixture, conn)
				if mode == "bad-block" {
					receiveAudioConfig(t, conn, "available")
					source.mu.Lock()
					for subscription := range source.subs {
						subscription.queue = []alsa.Block{{
							Config: source.config, Frames: 960,
							TimestampNS: time.Now().UnixNano(), Data: []byte{1},
						}}
						subscription.frames = 960
						subscription.signal()
					}
					source.mu.Unlock()
				}
				receiveAudioConfig(t, conn, "unavailable")
				synctest.Wait()
				fixture.source.frames <- sourceFrame(sourceJPEG(t), 1)
				if message := receive(t, conn.output); message.data[0] != 1 {
					t.Fatal("invalid audio source disabled video")
				}
			})
		})
	}
}

func TestAudioWriterDropsOldReadinessImmediatelyWhileVideoSwitchRetainsClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old, current := newAudioTestSource(1), newAudioTestSource(2)
		gate := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(gate) })
		old.closeGate = gate
		video, err := capture.New(capture.Options{
			Device: "/dev/fake-video", Width: 128, Height: 96,
			OpenSource: func(context.Context, string, v4l2.Options) (capture.Source, error) {
				return nil, capture.ErrUnavailable
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		controller := hid.NewController(&inputTransport{})
		mouse, _ := hid.NewMouse(controller, hid.MouseOptions{Width: 128, Height: 96})
		opened := make(chan struct{}, 4)
		server, err := New(controller, mouse, video, Options{Audio: &AudioOptions{
			Device: "/dev/fixed",
			OpenSource: func(context.Context, string) (AudioSource, error) {
				opened <- struct{}{}
				if len(opened) == 1 {
					return old, nil
				}
				return current, nil
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			unblock()
			server.Close()
			_ = controller.Close()
			server.Wait()
			_ = video.Close()
		})
		conn := newFakeSocket()
		startAudioWriter(t, fixture{server: server, capture: video}, conn)
		receiveAudioConfig(t, conn, "available")
		if err := video.SetResolution(t.Context(), 640, 480); !errors.Is(err, capture.ErrUnavailable) {
			t.Fatal("fake video failure should still accept the new generation", err)
		}
		receive(t, old.closeStarted)
		receiveAudioConfig(t, conn, "unavailable")
		old.publish(1)
		synctest.Wait() // A closed old Ready channel must not cause a select spin.
		if len(opened) != 1 {
			t.Fatal("source reopened before retained close completed")
		}
		select {
		case message := <-conn.output:
			t.Fatalf("old source remained deliverable: %x", message.data)
		default:
		}
		unblock()
		if config := receiveAudioConfig(t, conn, "available"); config.Generation != "2" {
			t.Fatal(config)
		}
		current.publish(2)
		if message := receive(t, conn.output); message.data[0] != 2 || binary.LittleEndian.Uint64(message.data[8:]) != 2 {
			t.Fatal("replacement PCM was not isolated from the old generation")
		}
	})
}

func TestAudioStalledWriterIsBoundedAndDoesNotStopAnotherClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newAudioTestSource(1)
		fixture := newFixture(t, Options{Audio: sourceAudioOptions(source)})
		slow, fast := newFakeSocket(), newFakeSocket()
		blocked := make(chan struct{})
		var once sync.Once
		slow.writer = func(ctx context.Context, kind websocket.MessageType) (io.WriteCloser, error) {
			return &messageWriter{finish: func(data []byte) error {
				if kind == websocket.MessageBinary && data[0] == 2 {
					once.Do(func() { close(blocked) })
					<-ctx.Done()
					return ctx.Err()
				}
				slow.output <- socketMessage{kind, bytes.Clone(data)}
				return nil
			}}, nil
		}
		slowDone, fastDone := make(chan struct{}), make(chan struct{})
		go func() { fixture.server.serveConnection(slow); close(slowDone) }()
		go func() { fixture.server.serveConnection(fast); close(fastDone) }()
		receiveAudioConfig(t, slow, "available")
		receiveAudioConfig(t, fast, "available")
		source.publish(1)
		receive(t, blocked)
		if message := receive(t, fast.output); message.data[0] != 2 {
			t.Fatal("other client did not receive PCM")
		}
		for tag := byte(2); tag <= 8; tag++ {
			time.Sleep(20 * time.Millisecond)
			source.publish(tag)
			if message := receive(t, fast.output); message.data[64] != tag {
				t.Fatal("slow writer blocked another subscriber")
			}
		}
		time.Sleep(time.Second)
		receive(t, slowDone)
		select {
		case <-fast.closed:
			t.Fatal("audio timeout closed a different client")
		case <-source.closeStarted:
			t.Fatal("one subscriber leaving closed another subscriber's PCM")
		default:
		}
		source.mu.Lock()
		if source.maximum > 4800 || len(source.subs) != 1 {
			t.Errorf("slow writer exceeded its100ms queue or retained a subscription: max%d active%d", source.maximum, len(source.subs))
		}
		source.mu.Unlock()
		fixture.server.Close()
		receive(t, fastDone)
	})
}
