//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/linkdata/autopaws/alsa"
	"github.com/linkdata/autopaws/capture"
)

// AudioSource owns one PCM. Subscribe and Config are short, nonblocking state
// operations. Close retains ownership until all underlying reads/close finish.
type AudioSource interface {
	Config() alsa.Config
	Subscribe() AudioSubscription
	Close() error
}

// AudioSubscription owns the source's sole100ms queue for one web connection.
// Acquire Ready BEFORE TryNext to avoid missing a concurrent publication.
// TryNext expires stale samples and never waits for a future block.
type AudioSubscription interface {
	Ready() <-chan struct{}
	TryNext() (alsa.Block, bool, error)
	Close()
}

// AudioOptions enables audio explicitly. A nil Options.Audio never probes or
// opens audio. Device is empty for selected-video USB matching, "off", or a
// fixed PCM path/selector. Defaults use the platform backend. On macOS audio
// requires an explicit avfoundation:NAME; Windows requires an explicit dshow:
// selector because USB pairing is unavailable for these media backends.
type AudioOptions struct {
	Device, SysRoot string
	OpenSource      func(context.Context, string) (AudioSource, error)
	ResolveAudio    func(context.Context, string) (string, error)
	retryInterval   time.Duration
}

type audioKey struct{ video, activation uint64 }

type audioRun struct {
	key    audioKey
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	source AudioSource // audioManager.mu
	failed bool        // audioManager.mu
}

type audioManager struct {
	video   *capture.Manager
	options AudioOptions
	logger  *slog.Logger
	ctx     context.Context
	done    chan struct{}
	mu      sync.Mutex
	changed chan struct{}
	clients int
	active  uint64
	run     *audioRun
}

func newAudioManager(ctx context.Context, video *capture.Manager, options AudioOptions, logger *slog.Logger) *audioManager {
	manager := &audioManager{
		video: video, options: audioDefaults(options), logger: logger,
		ctx: ctx, done: make(chan struct{}), changed: make(chan struct{}),
	}
	go manager.control()
	return manager
}

func (manager *audioManager) notifyLocked() {
	close(manager.changed)
	manager.changed = make(chan struct{})
}

func (manager *audioManager) subscribe() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.clients == 0 {
		manager.active++
	}
	manager.clients++
	manager.notifyLocked()
}

func (manager *audioManager) unsubscribe() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.clients--
	manager.notifyLocked()
}

// The control watcher never performs hardware I/O. It can cancel/invalidate
// immediately while the one source owner retains an executing Open/Close.
func (manager *audioManager) control() {
	defer close(manager.done)
	var attempted audioKey
	for {
		video, videoChanged := manager.video.Watch()
		manager.mu.Lock()
		key := audioKey{video.Generation, manager.active}
		want := manager.ctx.Err() == nil && manager.clients != 0 && manager.options.Device != "off" &&
			!errors.Is(video.Err, capture.ErrClosed)
		if manager.run != nil && (!want || manager.run.key != key) && manager.run.ctx.Err() == nil {
			manager.run.cancel()
			manager.notifyLocked()
		}
		if manager.run == nil && want && key != attempted {
			ctx, cancel := context.WithCancel(manager.ctx)
			run := &audioRun{key: key, ctx: ctx, cancel: cancel, done: make(chan struct{})}
			manager.run, attempted = run, key
			go manager.own(run, video.Device)
		}
		run := manager.run
		changed := manager.changed
		manager.mu.Unlock()
		if manager.ctx.Err() != nil && run == nil {
			return
		}
		var ended <-chan struct{}
		if run != nil {
			ended = run.done
		}
		var retry <-chan time.Time
		if run == nil && want && key == attempted && manager.options.retryInterval > 0 {
			retry = time.After(manager.options.retryInterval)
		}
		stop := manager.ctx.Done()
		if run != nil && manager.ctx.Err() != nil {
			stop = nil // Retain ownership without spinning on cancellation.
		}
		select {
		case <-stop:
		case <-videoChanged:
		case <-changed:
		case <-retry:
			attempted = audioKey{}
		case <-ended:
			manager.mu.Lock()
			manager.run = nil
			manager.notifyLocked()
			manager.mu.Unlock()
		}
	}
}

func (manager *audioManager) own(run *audioRun, video string) {
	defer close(run.done)
	defer run.cancel()
	path := manager.options.Device
	var err error
	if path == "" {
		path, err = manager.options.ResolveAudio(run.ctx, video)
	}
	if err == nil {
		err = run.ctx.Err()
	}
	var source AudioSource
	if err == nil && path != "" {
		source, err = manager.options.OpenSource(run.ctx, path)
	}
	if err == nil && (source == nil || !validAudioConfig(source.Config())) {
		err = alsa.ErrSettings
	}
	manager.mu.Lock()
	if manager.run == run && run.ctx.Err() == nil {
		run.source, run.failed = source, err != nil
		manager.notifyLocked()
	}
	manager.mu.Unlock()
	if err == nil {
		<-run.ctx.Done()
	} else if run.ctx.Err() == nil && manager.logger != nil {
		manager.logger.Warn("Web audio unavailable", "device", path, "error", err)
	}
	if source != nil {
		if err := source.Close(); err != nil && manager.logger != nil {
			manager.mu.Lock()
			failed := run.failed
			manager.mu.Unlock()
			if failed {
				manager.logger.Debug("Web audio source stopped", "device", path, "error", err)
			} else {
				manager.logger.Warn("Web audio close failed", "device", path, "error", err)
			}
		}
	}
}

func validAudioConfig(config alsa.Config) bool {
	return config.Generation != 0 && config.Rate != 0 && (config.Channels == 1 || config.Channels == 2)
}

type audioConfig struct {
	Status     string `json:"status"`
	Generation string `json:"generation"`
	SampleRate uint32 `json:"sampleRate,omitempty"`
	Channels   uint32 `json:"channels,omitempty"`
}

type audioView struct {
	config audioConfig
	raw    alsa.Config
	run    *audioRun
}

func (manager *audioManager) view(video capture.Info) (audioView, <-chan struct{}) {
	view := audioView{config: audioConfig{Status: "unavailable", Generation: "0"}}
	if manager == nil {
		return view, nil
	}
	manager.mu.Lock()
	changed := manager.changed
	run := manager.run
	if manager.options.Device == "off" {
		view.config.Status = "off"
	} else if run != nil && run.key.video == video.Generation && run.ctx.Err() == nil &&
		!run.failed && run.source != nil && !errors.Is(video.Err, capture.ErrClosed) {
		view.run = run
	}
	manager.mu.Unlock()
	if view.run != nil {
		view.raw = view.run.source.Config()
		view.config = audioConfig{
			Status: "available", Generation: fmt.Sprint(view.raw.Generation),
			SampleRate: view.raw.Rate, Channels: view.raw.Channels,
		}
	}
	return view, changed
}

func (manager *audioManager) current(view audioView) bool {
	video := manager.video.Info()
	manager.mu.Lock()
	current := manager.run == view.run && view.run != nil && view.run.ctx.Err() == nil &&
		!view.run.failed && view.run.key.video == video.Generation && !errors.Is(video.Err, capture.ErrClosed)
	manager.mu.Unlock()
	return current && view.run.source.Config() == view.raw
}

func (manager *audioManager) fail(run *audioRun, err error) {
	manager.mu.Lock()
	failed := false
	if manager.run == run && run.ctx.Err() == nil {
		run.failed = true
		run.cancel()
		manager.notifyLocked()
		failed = true
	}
	manager.mu.Unlock()
	if failed && manager.logger != nil {
		manager.logger.Warn("Web audio capture ended", "error", err)
	}
}
