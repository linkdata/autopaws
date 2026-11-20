package avfoundation

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/linkdata/autopaws/alsa"
	"github.com/linkdata/autopaws/internal/monotime"
)

const (
	audioRate   = 48000
	audioPeriod = 480 // 10 ms of stereo signed 16-bit PCM.
	audioBytes  = audioPeriod * 2 * 2
)

var audioGenerations atomic.Uint64

// Audio owns one FFmpeg audio input. Its worker continuously drains the pipe;
// each subscription retains at most 100 ms. FFmpeg/OS buffering precedes this
// queue and the estimated receipt timestamps; camera/microphone capture timing
// and synchronization are not available through the raw PCM pipe.
type Audio struct {
	config  alsa.Config
	process *process

	mu    sync.Mutex
	subs  map[*AudioSubscription]struct{}
	err   error
	ended bool
}

// OpenAudio opens one explicitly selected avfoundation:<exact name> input.
// ctx controls startup; the returned source lives until Close.
func OpenAudio(ctx context.Context, path string) (*Audio, error) {
	devices, err := Discover(ctx)
	if err != nil {
		return nil, err
	}
	device, err := selectDevice(devices, path, true)
	if err != nil {
		return nil, err
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-f", "avfoundation"}
	args = append(args, inputArguments(device)...)
	args = append(args, "-vn", "-ac", "2", "-ar", "48000", "-c:a", "pcm_s16le",
		"-f", "s16le", "-flush_packets", "1", "pipe:1")
	return openAudioCommand(ctx, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "ffmpeg", args...)
	})
}

func openAudioCommand(ctx context.Context, command func(context.Context) *exec.Cmd) (*Audio, error) {
	source := &Audio{
		config: alsa.Config{
			Rate: audioRate, Channels: 2, PeriodFrames: audioPeriod,
			BufferFrames: audioRate / 10, Generation: audioGenerations.Add(1),
		},
		subs: make(map[*AudioSubscription]struct{}),
	}
	owner, err := startProcess(ctx, "audio", command, source.run, source.finish)
	if err != nil {
		return nil, err
	}
	source.process = owner
	return source, nil
}

func (source *Audio) run(pipe io.Reader, ready func()) error {
	var sequence uint64
	var err error
	for {
		data := make([]byte, audioBytes)
		if _, err = io.ReadFull(pipe, data); err != nil {
			break
		}
		now, clockErr := monotime.Now()
		if clockErr != nil {
			err = clockErr
			break
		}
		// This estimates the block's first sample from pipe receipt, not the
		// device's capture timestamp.
		timestamp := max(int64(0), now-int64(10*time.Millisecond))
		if sequence == 0 {
			ready()
		}
		sequence++
		source.publish(alsa.Block{
			Data: data, Frames: audioPeriod, Sequence: sequence,
			TimestampNS: timestamp, Config: source.config,
		})
	}
	return err
}

// Config describes the output samples and application queue budget. The raw
// pipe does not expose AVFoundation's hardware buffer size.
func (source *Audio) Config() alsa.Config { return source.config }

func (source *Audio) publish(block alsa.Block) {
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.ended {
		return
	}
	for subscription := range source.subs {
		for len(subscription.queue) >= 10 {
			subscription.pop()
		}
		copy := block
		copy.Data = bytes.Clone(block.Data)
		subscription.queue = append(subscription.queue, copy)
		subscription.signal()
	}
}

func (source *Audio) finish(err error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.ended {
		return
	}
	source.ended, source.err = true, err
	for subscription := range source.subs {
		subscription.queue = nil
		subscription.err = err
		subscription.signal()
	}
}

// Close stops the worker, discards queued samples, and joins the child process.
func (source *Audio) Close() error {
	source.finish(ErrClosed)
	source.process.close()
	return nil
}

// AudioSubscription holds one independent bounded sample queue. Acquire Ready
// before TryNext and wait on it only when no sample or error is ready.
type AudioSubscription struct {
	source *Audio
	queue  []alsa.Block
	notify chan struct{}
	err    error
	closed bool
}

func (source *Audio) Subscribe() *AudioSubscription {
	source.mu.Lock()
	defer source.mu.Unlock()
	subscription := &AudioSubscription{source: source, notify: make(chan struct{})}
	if source.ended {
		subscription.err = source.err
		subscription.signal()
	} else {
		source.subs[subscription] = struct{}{}
	}
	return subscription
}

func (subscription *AudioSubscription) signal() {
	close(subscription.notify)
	subscription.notify = make(chan struct{})
}

func (subscription *AudioSubscription) pop() alsa.Block {
	block := subscription.queue[0]
	subscription.queue[0] = alsa.Block{}
	subscription.queue = subscription.queue[1:]
	return block
}

func (subscription *AudioSubscription) Ready() <-chan struct{} {
	subscription.source.mu.Lock()
	defer subscription.source.mu.Unlock()
	return subscription.notify
}

// TryNext discards blocks whose estimated final sample precedes the queue's
// 100 ms budget. It never waits for future samples.
func (subscription *AudioSubscription) TryNext() (alsa.Block, bool, error) {
	now, err := monotime.Now()
	if err != nil {
		return alsa.Block{}, false, err
	}
	subscription.source.mu.Lock()
	defer subscription.source.mu.Unlock()
	for len(subscription.queue) != 0 {
		block := subscription.pop()
		duration := int64(uint64(block.Frames) * uint64(time.Second) / uint64(block.Config.Rate))
		if now-block.TimestampNS-duration > int64(alsa.ServerQueueBudget) {
			continue
		}
		return block, true, nil
	}
	return alsa.Block{}, false, subscription.err
}

func (subscription *AudioSubscription) Close() {
	subscription.source.mu.Lock()
	defer subscription.source.mu.Unlock()
	if !subscription.closed {
		subscription.closed = true
		subscription.queue = nil
		subscription.err = ErrClosed
		delete(subscription.source.subs, subscription)
		subscription.signal()
	}
}
