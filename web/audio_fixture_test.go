//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/alsa"
	"github.com/linkdata/autopaws/capture"
)

type audioTestSource struct {
	mu           sync.Mutex
	config       alsa.Config
	sequence     uint64
	subs         map[*audioTestSubscription]struct{}
	subscribed   chan struct{}
	closeStarted chan struct{}
	closed       chan struct{}
	closeGate    <-chan struct{}
	once         sync.Once
	err          error
	closeErr     error
	maximum      uint64
	subCount     int
	closedSubs   int
}

func newAudioTestSource(generation uint64) *audioTestSource {
	return &audioTestSource{
		config: alsa.Config{
			Generation: generation, Rate: 48000, Channels: 2,
			PeriodFrames: 960, BufferFrames: 3840,
		}, subs: make(map[*audioTestSubscription]struct{}),
		subscribed: make(chan struct{}, 100), closeStarted: make(chan struct{}), closed: make(chan struct{}),
	}
}

func (source *audioTestSource) Config() alsa.Config {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.config
}

func (source *audioTestSource) Subscribe() AudioSubscription {
	source.mu.Lock()
	defer source.mu.Unlock()
	subscription := &audioTestSubscription{source: source, ready: make(chan struct{}), err: source.err}
	source.subs[subscription] = struct{}{}
	source.subCount++
	source.subscribed <- struct{}{}
	return subscription
}

func (source *audioTestSource) Close() error {
	source.once.Do(func() {
		close(source.closeStarted)
		if source.closeGate != nil {
			<-source.closeGate
		}
		source.mu.Lock()
		source.err = alsa.ErrClosed
		for subscription := range source.subs {
			subscription.queue, subscription.frames, subscription.err = nil, 0, alsa.ErrClosed
			subscription.signal()
		}
		source.mu.Unlock()
		close(source.closed)
	})
	return source.closeErr
}

func (source *audioTestSource) publish(tag byte) {
	source.mu.Lock()
	defer source.mu.Unlock()
	block := alsa.Block{
		Config: source.config, Frames: 960, TimestampNS: time.Now().Add(-20 * time.Millisecond).UnixNano(),
		Sequence: source.sequence, Data: bytes.Repeat([]byte{tag}, 960*4),
	}
	source.sequence++
	for subscription := range source.subs {
		maximum := uint64(source.config.Rate) / 10
		for subscription.frames+uint64(block.Frames) > maximum {
			subscription.pop()
		}
		subscription.queue = append(subscription.queue, block)
		subscription.frames += uint64(block.Frames)
		if subscription.frames > source.maximum {
			source.maximum = subscription.frames
		}
		subscription.signal()
	}
}

func (source *audioTestSource) recover(generation uint64) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.config.Generation, source.sequence = generation, 0
	for subscription := range source.subs {
		subscription.queue, subscription.frames = nil, 0
		subscription.signal()
	}
}

func (source *audioTestSource) fail(err error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.err = err
	for subscription := range source.subs {
		subscription.queue, subscription.frames, subscription.err = nil, 0, err
		subscription.signal()
	}
}

type audioTestSubscription struct {
	source *audioTestSource
	ready  chan struct{}
	queue  []alsa.Block
	frames uint64
	closed bool
	err    error
}

func (subscription *audioTestSubscription) signal() {
	close(subscription.ready)
	subscription.ready = make(chan struct{})
}

func (subscription *audioTestSubscription) Ready() <-chan struct{} {
	subscription.source.mu.Lock()
	defer subscription.source.mu.Unlock()
	return subscription.ready
}

func (subscription *audioTestSubscription) pop() alsa.Block {
	block := subscription.queue[0]
	subscription.queue[0] = alsa.Block{}
	subscription.queue = subscription.queue[1:]
	subscription.frames -= uint64(block.Frames)
	return block
}

func (subscription *audioTestSubscription) TryNext() (alsa.Block, bool, error) {
	subscription.source.mu.Lock()
	defer subscription.source.mu.Unlock()
	for len(subscription.queue) != 0 {
		block := subscription.pop()
		duration := int64(uint64(block.Frames) * uint64(time.Second) / uint64(block.Config.Rate))
		if time.Now().UnixNano()-block.TimestampNS-duration > int64(alsa.ServerQueueBudget) {
			continue
		}
		return block, true, nil
	}
	return alsa.Block{}, false, subscription.err
}

func (subscription *audioTestSubscription) Close() {
	subscription.source.mu.Lock()
	defer subscription.source.mu.Unlock()
	if !subscription.closed {
		subscription.closed = true
		subscription.queue, subscription.frames, subscription.err = nil, 0, alsa.ErrClosed
		delete(subscription.source.subs, subscription)
		subscription.source.closedSubs++
		subscription.signal()
	}
}

func startAudioWriter(t *testing.T, fixture fixture, conn *fakeSocket) {
	t.Helper()
	subscription, err := fixture.capture.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- fixture.server.write(ctx, conn, subscription, nil) }()
	t.Cleanup(func() {
		cancel()
		if err := receive(t, done); !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
		_ = subscription.Close()
	})
}

func receiveAudioConfig(t *testing.T, conn *fakeSocket, status string) audioConfig {
	t.Helper()
	for {
		message := receive(t, conn.output)
		if message.kind != websocket.MessageText {
			t.Fatalf("PCM/video arrived before expected %s config", status)
		}
		var config struct {
			Type  string      `json:"type"`
			Audio audioConfig `json:"audio"`
		}
		if err := json.Unmarshal(message.data, &config); err != nil || config.Type != "config" {
			t.Fatalf("bad config %s: %v", message.data, err)
		}
		if config.Audio.Status == status {
			return config.Audio
		}
	}
}

func audioManagerFixture(t *testing.T, options AudioOptions) (*audioManager, *capture.Manager) {
	t.Helper()
	video, err := capture.New(capture.Options{Device: "/dev/video-a", Width: 128, Height: 96})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	manager := newAudioManager(ctx, video, options, nil)
	t.Cleanup(func() {
		cancel()
		receive(t, manager.done)
		if err := video.Close(); err != nil {
			t.Error(err)
		}
	})
	return manager, video
}

func waitAudioView(t *testing.T, manager *audioManager, available bool) audioView {
	t.Helper()
	for {
		view, changed := manager.view(manager.video.Info())
		if (view.run != nil) == available {
			return view
		}
		receive(t, changed)
	}
}
