//go:build (linux || darwin || windows) && (amd64 || arm64)

package ffmpegmedia

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/linkdata/autopaws/alsa"
	"github.com/linkdata/autopaws/internal/monotime"
)

func queueAudio() *Audio {
	return &Audio{
		config: alsa.Config{Rate: audioRate, Channels: 2, PeriodFrames: audioPeriod, Generation: 1},
		subs:   make(map[*AudioSubscription]struct{}),
	}
}

func TestAudioQueuesBoundedIndependentAndExpiring(t *testing.T) {
	source := queueAudio()
	first, second := source.Subscribe(), source.Subscribe()
	defer first.Close()
	defer second.Close()
	ready := first.Ready()
	now, err := monotime.Now()
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= 12; sequence++ {
		source.publish(alsa.Block{
			Data: []byte{byte(sequence)}, Frames: audioPeriod, Sequence: sequence,
			TimestampNS: now, Config: source.config,
		})
	}
	select {
	case <-ready:
	default:
		t.Fatal("publication did not wake Ready")
	}
	if len(first.queue) != 10 || len(second.queue) != 10 {
		t.Fatalf("queue exceeded budget: %d / %d", len(first.queue), len(second.queue))
	}
	block, ok, err := first.TryNext()
	if err != nil || !ok || block.Sequence != 3 {
		t.Fatalf("old blocks retained: %+v %t %v", block, ok, err)
	}
	block.Data[0] = 99
	other, ok, err := second.TryNext()
	if err != nil || !ok || other.Data[0] != 3 {
		t.Fatalf("subscription data not independently owned: %+v %t %v", other, ok, err)
	}
	first.queue = nil
	source.publish(alsa.Block{Data: []byte{1}, Frames: audioPeriod, TimestampNS: now - int64(time.Second), Config: source.config})
	if _, ok, err := first.TryNext(); err != nil || ok {
		t.Fatalf("stale block replayed: %t %v", ok, err)
	}
}

func TestAudioFinishAndSubscriptionClose(t *testing.T) {
	source := queueAudio()
	subscription := source.Subscribe()
	ready := subscription.Ready()
	source.finish(io.ErrUnexpectedEOF)
	select {
	case <-ready:
	default:
		t.Fatal("finish did not wake Ready")
	}
	if _, ok, err := subscription.TryNext(); ok || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("terminal error missing: %t %v", ok, err)
	}
	late := source.Subscribe()
	if _, _, err := late.TryNext(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("late subscription lost terminal error: %v", err)
	}
	subscription.Close()
	subscription.Close()
	if _, _, err := subscription.TryNext(); !errors.Is(err, ErrClosed) {
		t.Fatalf("subscription close lost error: %v", err)
	}
	if len(source.subs) != 0 {
		t.Fatal("closed subscription retained")
	}
}

func audioHelper(mode string, recorded **exec.Cmd) func(context.Context) *exec.Cmd {
	return func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAudioHelperProcess$")
		// Race instrumentation's exit sleep must not consume the stop deadline.
		cmd.Env = append(os.Environ(), "AUTOPAWS_AUDIO_HELPER="+mode, "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0")
		*recorded = cmd
		return cmd
	}
}

func TestAudioHelperProcess(t *testing.T) {
	mode := os.Getenv("AUTOPAWS_AUDIO_HELPER")
	if mode == "" {
		return
	}
	if mode == "error" {
		_, _ = os.Stderr.WriteString("fixture microphone failure")
		os.Exit(2)
	}
	if mode == "first" || mode == "graceful" {
		if _, err := os.Stdout.Write(make([]byte, audioBytes)); err != nil {
			os.Exit(3)
		}
	}
	if mode == "graceful" {
		var command [2]byte
		if _, err := io.ReadFull(os.Stdin, command[:]); err != nil || string(command[:]) != "q\n" {
			os.Exit(4)
		}
		os.Exit(0)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestAudioProcessLifetime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	source, err := openAudioCommand(ctx, audioHelper("first", &cmd))
	if err != nil {
		t.Fatal(err)
	}
	if config := source.Config(); config.Rate != 48000 || config.Channels != 2 || config.Generation == 0 {
		t.Fatalf("unexpected config: %+v", config)
	}
	// Startup context cancellation must not kill the returned stream.
	cancel()
	source.mu.Lock()
	ended := source.ended
	source.mu.Unlock()
	if ended {
		t.Fatal("stream ended with startup context")
	}
	subscription := source.Subscribe()
	closed := make(chan error, 1)
	go func() { closed <- source.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not kill and join a child that ignores stdin")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("Close returned without reaping the process")
	}
	if cmd.ProcessState.Success() {
		t.Fatal("fixture ignoring stdin exited without the forced-stop fallback")
	}
	if _, _, err := subscription.TryNext(); !errors.Is(err, ErrClosed) {
		t.Fatalf("source Close did not end subscription: %v", err)
	}
}

func TestAudioStartupFailureAndCancellation(t *testing.T) {
	var cmd *exec.Cmd
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := openAudioCommand(ctx, audioHelper("error", &cmd)); err == nil || !strings.Contains(err.Error(), "fixture microphone failure") {
		t.Fatalf("missing startup diagnostics: %v", err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("startup failure leaked process")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := openAudioCommand(ctx, audioHelper("block", &cmd)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup cancellation error: %v", err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("startup cancellation leaked process")
	}
}

func TestAudioFFmpegPCMFixture(t *testing.T) {
	ffmpegRequired(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	source, err := openAudioCommand(ctx, func(ctx context.Context) *exec.Cmd {
		cmd = exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
			"-re", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-ac", "2", "-ar", "48000",
			"-c:a", "pcm_s16le", "-f", "s16le", "-flush_packets", "1", "pipe:1")
		return cmd
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	subscription := source.Subscribe()
	defer subscription.Close()
	for {
		ready := subscription.Ready()
		block, ok, err := subscription.TryNext()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			if len(block.Data) != audioBytes || block.Frames != audioPeriod || block.TimestampNS < 0 {
				t.Fatalf("invalid output block: %+v", block)
			}
			for _, value := range block.Data {
				if value != 0 {
					if err := source.Close(); err != nil {
						t.Fatal(err)
					}
					if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
						t.Fatalf("FFmpeg did not stop gracefully: %v", cmd.ProcessState)
					}
					return
				}
			}
			t.Fatal("tone output contained only silence")
		}
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
