//go:build linux && (amd64 || arm64)

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/alsa"
	"github.com/linkdata/autopaws/capture"
)

func audioConfigMessage(config audioConfig) []byte {
	// All fields are primitive strings/uint32, so marshaling cannot fail.
	data, _ := json.Marshal(struct {
		Type  string      `json:"type"`
		Audio audioConfig `json:"audio"`
	}{Type: "config", Audio: config})
	return data
}

func validAudioBlock(block alsa.Block) bool {
	return validAudioConfig(block.Config) && block.Frames != 0 && block.TimestampNS >= 0 &&
		uint64(len(block.Data)) == uint64(block.Frames)*uint64(block.Config.Channels)*2 &&
		uint64(block.Frames)*uint64(time.Second)/uint64(block.Config.Rate) <= uint64(alsa.ServerQueueBudget)
}

// write is the connection's sole application writer. Audio is dequeued only
// immediately before its write: no PCM bridge goroutine or extra queue exists.
func (server *Server) write(ctx context.Context, conn socket, videoSubscription *capture.Subscription) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if server.audio != nil {
		server.audio.subscribe()
		defer server.audio.unsubscribe()
	}
	var audioSubscription AudioSubscription
	var attached *audioRun
	defer func() {
		if audioSubscription != nil {
			audioSubscription.Close()
		}
	}()
	var configured bool
	var lastConfig audioConfig
	var sequence uint64
	var nextVideo time.Time
	var pendingVideo *capture.Frame
	timer := time.NewTimer(0)
	timer.Stop()
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		video, videoChanged := server.capture.Watch()
		view, audioChanged := server.audio.view(video)
		if view.run != attached {
			if audioSubscription != nil {
				audioSubscription.Close()
				audioSubscription = nil
			}
			attached = view.run
			if attached != nil {
				audioSubscription = attached.source.Subscribe()
			}
		}
		if !configured || view.config != lastConfig {
			if err := server.writeMessage(ctx, conn, websocket.MessageText, audioConfigMessage(view.config)); err != nil {
				return err
			}
			lastConfig, configured = view.config, true
			continue // Recheck recovery/config changes after the bounded send.
		}
		var audioReady <-chan struct{}
		if audioSubscription != nil {
			audioReady = audioSubscription.Ready()
			block, ok, err := audioSubscription.TryNext()
			if err != nil {
				server.audio.fail(attached, err)
				continue
			}
			if ok {
				if block.Config != view.raw || !server.audio.current(view) {
					continue // Drop a dequeue racing generation/recovery; never replay.
				}
				if !validAudioBlock(block) {
					server.audio.fail(attached, alsa.ErrSamples)
					continue
				}
				header := audioHeader(block)
				if err := server.writeMessage(ctx, conn, websocket.MessageBinary, header[:], block.Data); err != nil {
					return fmt.Errorf("write audio: %w", err)
				}
				continue
			}
		}
		if pendingVideo != nil && !time.Now().Before(nextVideo) {
			// If audio occupied the writer, replace its held JPEG with the
			// newest frame already waiting in the original one-slot channel.
			select {
			case event, ok := <-videoSubscription.Events():
				if !ok {
					return capture.ErrClosed
				}
				pendingVideo = event.Frame
			default:
			}
			if pendingVideo == nil {
				continue
			}
			metadata := pendingVideo.Metadata()
			if metadata.Generation != server.capture.Info().Generation {
				pendingVideo = nil
				continue
			}
			sequence++
			header := videoHeader(metadata, sequence)
			started := time.Now()
			if err := server.writeMessage(ctx, conn, websocket.MessageBinary, header[:], pendingVideo.JPEG()); err != nil {
				return fmt.Errorf("write video: %w", err)
			}
			pendingVideo = nil
			nextVideo = time.Now().Add(time.Second / time.Duration(server.options.FPS))
			if audioSubscription != nil && time.Since(started) >= alsa.ServerQueueBudget {
				// A long JPEG send must not drain its accumulated audio burst.
				// Remove the old queue before creating the new empty one.
				audioSubscription.Close()
				audioSubscription = nil
				attached = nil
			}
			continue
		}
		videoEvents := videoSubscription.Events()
		var due <-chan time.Time
		if delay := time.Until(nextVideo); delay > 0 {
			videoEvents = nil // Leave video in its latest slot until pacing permits.
			timer.Reset(delay)
			due = timer.C
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-videoChanged:
		case <-audioChanged:
		case <-audioReady:
		case <-due:
		case event, ok := <-videoEvents:
			if !ok {
				timer.Stop()
				return capture.ErrClosed
			}
			pendingVideo = event.Frame // Failure events keep the subscription repairable.
		}
		timer.Stop()
	}
}
