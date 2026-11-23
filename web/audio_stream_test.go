//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/alsa"
)

func sourceAudioOptions(source *audioTestSource) *AudioOptions {
	return &AudioOptions{Device: "/dev/fake-audio", OpenSource: func(context.Context, string) (AudioSource, error) { return source, nil }}
}

func TestAudioHeaderClockSequenceGenerationAndPCM(t *testing.T) {
	block := alsa.Block{
		Config:   alsa.Config{Rate: 96000, Channels: 1, Generation: 1<<63 + 3},
		Sequence: 1<<63 + 7, TimestampNS: 1234567890123456789, Frames: 1920,
	}
	header := audioHeader(block)
	if header[0] != 2 || header[1] != 1 || binary.LittleEndian.Uint16(header[2:]) != 64 || binary.LittleEndian.Uint32(header[4:]) != 5 ||
		binary.LittleEndian.Uint64(header[8:]) != block.Config.Generation || binary.LittleEndian.Uint64(header[16:]) != block.Sequence ||
		binary.LittleEndian.Uint64(header[24:]) != uint64(block.TimestampNS) || binary.LittleEndian.Uint32(header[56:]) != 1920 || binary.LittleEndian.Uint32(header[60:]) != 20000000 {
		t.Fatalf("audio wire metadata lost precision or clock semantics: %x", header)
	}
	if !bytes.Equal(header[32:56], make([]byte, 24)) {
		t.Fatal("audio header populated video geometry")
	}
	if validAudioBlock(block) {
		t.Fatal("missing PCM data was accepted")
	}
	block.Data = make([]byte, block.Frames*block.Config.Channels*2)
	if !validAudioBlock(block) {
		t.Fatal("valid mono96kHz PCM was rejected")
	}
	block.Config.Rate = 0
	if validAudioBlock(block) {
		t.Fatal("zero-rate metadata was accepted")
	}
}

func TestAudioConfigBeforePCMAndRecoveryGeneration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newAudioTestSource(1<<63 + 3)
		fixture := newFixture(t, Options{Audio: sourceAudioOptions(source)})
		conn := newFakeSocket()
		startAudioWriter(t, fixture, conn)
		config := receiveAudioConfig(t, conn, "available")
		if config.Generation != "9223372036854775811" || config.SampleRate != 48000 || config.Channels != 2 {
			t.Fatal(config)
		}
		source.publish(17)
		message := receive(t, conn.output)
		if message.kind != websocket.MessageBinary || message.data[0] != 2 || binary.LittleEndian.Uint64(message.data[8:]) != 1<<63+3 ||
			binary.LittleEndian.Uint32(message.data[56:]) != 960 || !bytes.Equal(message.data[64:], bytes.Repeat([]byte{17}, 960*4)) {
			t.Fatal("audio PCM/header changed in transit")
		}
		// A recovery racing the previous dequeue may legitimately drop its block.
		synctest.Wait()
		source.recover(1<<63 + 4)
		source.publish(23)
		config = receiveAudioConfig(t, conn, "available")
		if config.Generation != "9223372036854775812" {
			t.Fatal(config)
		}
		message = receive(t, conn.output)
		if message.data[0] != 2 || binary.LittleEndian.Uint64(message.data[8:]) != 1<<63+4 || binary.LittleEndian.Uint64(message.data[16:]) != 0 || message.data[64] != 23 {
			t.Fatal("recovery retained old-generation PCM or source sequence")
		}
	})
}

func TestAudioRecoveryDuringConfigWriteSendsNewConfigBeforeDequeue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newAudioTestSource(1)
		fixture := newFixture(t, Options{Audio: sourceAudioOptions(source)})
		conn := newFakeSocket()
		blocked, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		var once sync.Once
		conn.writer = func(_ context.Context, kind websocket.MessageType) (io.WriteCloser, error) {
			return &messageWriter{finish: func(data []byte) error {
				if kind == websocket.MessageText && bytes.Contains(data, []byte(`"status":"available"`)) {
					once.Do(func() { close(blocked); <-release })
				}
				conn.output <- socketMessage{kind, bytes.Clone(data)}
				return nil
			}}, nil
		}
		startAudioWriter(t, fixture, conn)
		t.Cleanup(unblock)
		receive(t, blocked)
		source.publish(1)
		source.recover(2)
		source.publish(2)
		unblock()
		if config := receiveAudioConfig(t, conn, "available"); config.Generation != "1" {
			t.Fatal(config)
		}
		if config := receiveAudioConfig(t, conn, "available"); config.Generation != "2" {
			t.Fatal("recovery PCM was dequeued before replacement config", config)
		}
		message := receive(t, conn.output)
		if message.data[0] != 2 || binary.LittleEndian.Uint64(message.data[8:]) != 2 || message.data[64] != 2 {
			t.Fatal("old configuration leaked PCM")
		}
	})
}

func TestAudioReadyBeforeVideoHasPriority(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newAudioTestSource(1)
		fixture := newFixture(t, Options{Audio: sourceAudioOptions(source)})
		conn := newFakeSocket()
		blocked, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		conn.writer = func(_ context.Context, kind websocket.MessageType) (io.WriteCloser, error) {
			return &messageWriter{finish: func(data []byte) error {
				if kind == websocket.MessageText && bytes.Contains(data, []byte(`"status":"available"`)) {
					close(blocked)
					<-release
				}
				conn.output <- socketMessage{kind, bytes.Clone(data)}
				return nil
			}}, nil
		}
		startAudioWriter(t, fixture, conn)
		t.Cleanup(unblock)
		receive(t, blocked)
		fixture.source.frames <- sourceFrame(sourceJPEG(t), 1)
		source.publish(7)
		synctest.Wait()
		unblock()
		receiveAudioConfig(t, conn, "available")
		if message := receive(t, conn.output); message.data[0] != 2 {
			t.Fatal("waiting JPEG overtook ready PCM")
		}
		if message := receive(t, conn.output); message.data[0] != 1 {
			t.Fatal("video did not follow prioritized audio")
		}
	})
}

func TestAudioLongVideoWriteDropsWholeBacklogBeforeResumingFreshPCM(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newAudioTestSource(1)
		fixture := newFixture(t, Options{Audio: sourceAudioOptions(source)})
		conn := newFakeSocket()
		blocked, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		conn.writer = func(ctx context.Context, kind websocket.MessageType) (io.WriteCloser, error) {
			return &messageWriter{finish: func(data []byte) error {
				if kind == websocket.MessageBinary && data[0] == 1 {
					close(blocked)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				conn.output <- socketMessage{kind, bytes.Clone(data)}
				return nil
			}}, nil
		}
		startAudioWriter(t, fixture, conn)
		t.Cleanup(unblock)
		receiveAudioConfig(t, conn, "available")
		receive(t, source.subscribed)
		fixture.source.frames <- sourceFrame(sourceJPEG(t), 1)
		receive(t, blocked)
		for tag := byte(1); tag <= 6; tag++ {
			time.Sleep(20 * time.Millisecond)
			source.publish(tag)
		}
		unblock()
		if message := receive(t, conn.output); message.data[0] != 1 {
			t.Fatal("unexpected message while JPEG held writer")
		}
		receive(t, source.subscribed) // Old queue was removed and a fresh one attached.
		select {
		case message := <-conn.output:
			t.Fatalf("stalled video replayed an audio backlog: %x", message.data[:64])
		default:
		}
		source.publish(99)
		message := receive(t, conn.output)
		if message.data[0] != 2 || binary.LittleEndian.Uint64(message.data[16:]) != 6 || message.data[64] != 99 {
			t.Fatal("fresh audio did not resume after dropped backlog")
		}
		source.mu.Lock()
		if source.maximum > 4800 || source.subCount != 2 || source.closedSubs != 1 || len(source.subs) != 1 {
			t.Errorf("extra/oversized application queue: max%d subscriptions%d closed%d active%d", source.maximum, source.subCount, source.closedSubs, len(source.subs))
		}
		source.mu.Unlock()
	})
}

func TestAudioUnavailableOffAndFailureLeaveVideoAndInputAlive(t *testing.T) {
	for _, mode := range []string{"off", "no-source", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			source := newAudioTestSource(1)
			options := sourceAudioOptions(source)
			switch mode {
			case "off":
				options.Device = "off"
			case "no-source":
				options.OpenSource = func(context.Context, string) (AudioSource, error) { return nil, alsa.ErrBusy }
			}
			fixture := newFixture(t, Options{Audio: options})
			conn := newFakeSocket()
			startAudioWriter(t, fixture, conn)
			status := "unavailable"
			switch mode {
			case "off":
				status = "off"
			case "terminal":
				receiveAudioConfig(t, conn, "available")
				source.fail(alsa.ErrDisconnected)
			}
			receiveAudioConfig(t, conn, status)
			fixture.source.frames <- sourceFrame(sourceJPEG(t), 1)
			if message := receive(t, conn.output); message.data[0] != 1 {
				t.Fatal("audio unavailability disabled video")
			}
			if err := fixture.mouse.MoveAbs(t.Context(), 500, 500); err != nil {
				t.Fatal("audio unavailability disabled HID input", err)
			}
		})
	}
}

func TestAudioWriteFailurePreservesErrorIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newAudioTestSource(1)
		fixture := newFixture(t, Options{Audio: sourceAudioOptions(source)})
		conn := newFakeSocket()
		failure := errors.New("PCM write failed")
		conn.writer = func(_ context.Context, kind websocket.MessageType) (io.WriteCloser, error) {
			return &messageWriter{finish: func(data []byte) error {
				if kind == websocket.MessageBinary && data[0] == 2 {
					return failure
				}
				conn.output <- socketMessage{kind, bytes.Clone(data)}
				return nil
			}}, nil
		}
		subscription, _ := fixture.capture.Subscribe()
		defer subscription.Close()
		done := make(chan error, 1)
		go func() { done <- fixture.server.write(t.Context(), conn, subscription) }()
		receiveAudioConfig(t, conn, "available")
		source.publish(1)
		if err := receive(t, done); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	})
}
