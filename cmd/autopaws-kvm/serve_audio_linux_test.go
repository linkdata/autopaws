//go:build linux && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/alsa"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/ch9329"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/serial"
	"github.com/linkdata/autopaws/v4l2"
	"github.com/linkdata/autopaws/web"
)

type runtimeAudioSource struct {
	closeStarted chan struct{}
	closed       chan struct{}
	finish       <-chan struct{}
	once         sync.Once
}

func (*runtimeAudioSource) Config() alsa.Config {
	return alsa.Config{Generation: 1, Rate: 48000, Channels: 2, PeriodFrames: 960, BufferFrames: 7680}
}

func (*runtimeAudioSource) Subscribe() web.AudioSubscription {
	return &runtimeAudioSubscription{ready: make(chan struct{})}
}

func (source *runtimeAudioSource) Close() error {
	source.once.Do(func() {
		close(source.closeStarted)
		<-source.finish
		close(source.closed)
	})
	return nil
}

type runtimeAudioSubscription struct {
	ready  chan struct{}
	closed atomic.Bool
}

func (subscription *runtimeAudioSubscription) Ready() <-chan struct{} { return subscription.ready }

func (subscription *runtimeAudioSubscription) TryNext() (alsa.Block, bool, error) {
	if subscription.closed.Load() {
		return alsa.Block{}, false, alsa.ErrClosed
	}
	return alsa.Block{}, false, nil
}

func (subscription *runtimeAudioSubscription) Close() {
	if subscription.closed.CompareAndSwap(false, true) {
		close(subscription.ready)
	}
}

func TestRuntimeJoinsRetainedAudioAfterClosingInputVideoAndNetwork(t *testing.T) {
	finishAudio := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(finishAudio) })
	defer unblock()
	audio := &runtimeAudioSource{closeStarted: make(chan struct{}), closed: make(chan struct{}), finish: finishAudio}
	video := newRuntimeVideoSource()
	port := &runtimeClosingTransport{runtimeTransport: &runtimeTransport{}, closed: make(chan struct{})}
	pressed := make(chan struct{})
	var pressedOnce sync.Once
	port.write = func(ctx context.Context, frame []byte) error {
		if bytes.Equal(frame, ch9329.Keyboard(0, 4)) {
			pressedOnce.Do(func() { close(pressed) })
		}
		return ctx.Err()
	}
	cfg := runtimeConfig()
	cfg.Web, cfg.CaptureDevice, cfg.AudioDevice = "127.0.0.1:0", "/dev/fake-video", "/dev/fixed-pcm"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	bound := make(chan net.Listener, 2)
	done := make(chan error, 1)
	var audioSettings atomic.Int32
	go func() {
		defer close(done)
		done <- serveWith(ctx, cfg, io.Discard, runtimeFactories{
			newTransport: func(serial.Options) (hid.Transport, error) { return port, nil },
			newCapture: func(options capture.Options) (*capture.Manager, error) {
				options.OpenSource = func(context.Context, string, v4l2.Options) (capture.Source, error) { return video, nil }
				return capture.New(options)
			},
			audioOptions: func(device string) *web.AudioOptions {
				audioSettings.Add(1)
				if device != "/dev/fixed-pcm" {
					t.Error("runtime did not forward configured audio selection", device)
				}
				return &web.AudioOptions{Device: device, OpenSource: func(_ context.Context, selected string) (web.AudioSource, error) {
					if selected != device {
						t.Error("explicit audio path was replaced", selected)
					}
					return audio, nil
				}}
			},
			listen: func(ctx context.Context, network, address string) (net.Listener, error) {
				listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
				if err == nil {
					bound <- listener
				}
				return listener, err
			},
		})
	}()
	t.Cleanup(func() {
		cancel()
		unblock()
		if err := receiveRuntime(t, done); err != nil {
			t.Error(err)
		}
	})
	receiveRuntime(t, bound) // API binds first; neither endpoint accepts yet.
	webListener := receiveRuntime(t, bound)
	connectCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	conn, _, err := websocket.Dial(connectCtx, "ws://"+webListener.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for {
		kind, data, err := conn.Read(connectCtx)
		var config struct {
			Audio struct{ Status string }
		}
		if err != nil || kind != websocket.MessageText || json.Unmarshal(data, &config) != nil {
			t.Fatalf("audio configuration was not delivered: %s,%v", data, err)
		}
		if config.Audio.Status == "available" {
			break
		}
	}
	receiveRuntime(t, video.started)
	runtimeViewerInput(t, conn, `{"type":"keydown","code":"KeyA"}`)
	receiveRuntime(t, pressed)
	cancel()
	receiveRuntime(t, audio.closeStarted)
	// Closing audio must not prevent either other hardware owner from
	// completing cleanup or prevent the hijacked connection from closing.
	receiveRuntime(t, port.closed)
	receiveRuntime(t, video.closed)
	readCtx, stopRead := context.WithTimeout(t.Context(), time.Second)
	defer stopRead()
	if _, _, err := conn.Read(readCtx); err == nil || errors.Is(readCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("retained PCM close kept WebSocket open: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("runtime abandoned retained PCM close: %v", err)
	default:
	}
	unblock()
	if err := receiveRuntime(t, done); err != nil {
		t.Fatal(err)
	}
	receiveRuntime(t, audio.closed)
	if audioSettings.Load() != 1 || video.closes.Load() != 1 {
		t.Fatal("runtime created/released an owner more than once")
	}
}

func TestRuntimeAudioFactoryIsExplicitAndStartupBindFailureJoins(t *testing.T) {
	for _, webEnabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "API only", true: "pre-bind web audio"}[webEnabled], func(t *testing.T) {
			cfg := runtimeConfig()
			if webEnabled {
				cfg.Web = "127.0.0.1:0"
			}
			port := &runtimeTransport{}
			failure := errors.New("startup bind failed")
			var selections, opens atomic.Int32
			binds := 0
			done := make(chan error, 1)
			go func() {
				done <- serveWith(t.Context(), cfg, io.Discard, runtimeFactories{
					newTransport: func(serial.Options) (hid.Transport, error) { return port, nil },
					newCapture:   capture.New,
					audioOptions: func(device string) *web.AudioOptions {
						selections.Add(1)
						return &web.AudioOptions{Device: device, OpenSource: func(context.Context, string) (web.AudioSource, error) {
							opens.Add(1)
							return nil, errors.New("no subscriber should open PCM")
						}}
					},
					listen: func(ctx context.Context, network, address string) (net.Listener, error) {
						binds++
						if webEnabled && binds == 1 {
							return (&net.ListenConfig{}).Listen(ctx, network, address)
						}
						return nil, failure
					},
				})
			}()
			if err := receiveRuntime(t, done); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			want := int32(0)
			if webEnabled {
				want = 1
			}
			_, closes, _ := port.snapshot()
			if selections.Load() != want || opens.Load() != 0 || closes != 1 || binds != int(want)+1 {
				t.Fatalf("audio was implicit/eager or startup cleanup was skipped: options%d opens%d closes%d", selections.Load(), opens.Load(), closes)
			}
		})
	}
}
