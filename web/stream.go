//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/alsa"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/hid"
)

func audioConfigMessage(config audioConfig, mapping mappingConfig) []byte {
	// HID validates mapping numbers before publishing a snapshot.
	data, _ := json.Marshal(struct {
		Type    string        `json:"type"`
		Audio   audioConfig   `json:"audio"`
		Mapping mappingConfig `json:"mapping"`
	}{Type: "config", Audio: config, Mapping: mapping})
	return data
}

func validAudioBlock(block alsa.Block) bool {
	return validAudioConfig(block.Config) && block.Frames != 0 && block.TimestampNS >= 0 &&
		uint64(len(block.Data)) == uint64(block.Frames)*uint64(block.Config.Channels)*2 &&
		uint64(block.Frames)*uint64(time.Second)/uint64(block.Config.Rate) <= uint64(alsa.ServerQueueBudget)
}

// write is the connection's sole application writer. Audio is dequeued only
// immediately before its write: no PCM bridge goroutine or extra queue exists.
func (server *Server) write(ctx context.Context, conn socket, videoSubscription *capture.Subscription, input *browserInput) error {
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
	var mappingRevision, mappingGeneration, barrierSequence uint64
	var mappingReady bool
	var lastDetail string
	var lastMapping string
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
		state, mappingChanged := server.mouse.WatchMapping()
		stable := state
		stable.Capture = hid.MappingCapture{} // Ordinary capture sequence advances are not config changes.
		encoded, _ := json.Marshal(stable)
		mappingState := string(encoded)
		var detail string
		var statusChanged <-chan struct{}
		if input != nil {
			detail, statusChanged = input.statusWatch()
		}
		pointer := pointerConfig(state, video.Generation, barrierSequence, detail)
		mappingChange := !configured || state.Revision != mappingRevision || video.Generation != mappingGeneration ||
			pointer.Ready != mappingReady
		if mappingChange {
			barrierSequence = sequence
			if sequence != 0 {
				barrierSequence = max(barrierSequence, state.Capture.Sequence)
			}
			pointer.AfterSequence = strconv.FormatUint(barrierSequence, 10)
			if input != nil {
				input.mu.Lock()
				input.barrier.revision, input.barrier.generation, input.barrier.sequence = state.Revision, video.Generation, barrierSequence
				input.mu.Unlock()
			}
		}
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
		if !configured || view.config != lastConfig || mappingChange || mappingState != lastMapping || detail != lastDetail {
			if err := server.writeMessage(ctx, conn, websocket.MessageText, audioConfigMessage(view.config, pointer)); err != nil {
				return err
			}
			lastConfig, configured = view.config, true
			mappingRevision, mappingGeneration = state.Revision, video.Generation
			mappingReady = pointer.Ready
			lastDetail = detail
			lastMapping = mappingState
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
			currentVideo := server.capture.Info()
			if metadata.Generation != currentVideo.Generation {
				pendingVideo = nil
				continue
			}
			if err := server.mouse.SetMappingCapture(ctx, hid.MappingCapture{
				Device: currentVideo.Device,
				Width:  metadata.Width, Height: metadata.Height, Crop: metadata.Crop,
				Generation: metadata.Generation, Sequence: metadata.DeliverySequence,
			}); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			// A failed HID release can still bind healthy video. Verify its context below.
			bound := server.mouse.Mapping()
			if bound.Capture.Device != currentVideo.Device || bound.Capture.Generation != metadata.Generation ||
				bound.Capture.Width != metadata.Width || bound.Capture.Height != metadata.Height || bound.Capture.Crop != metadata.Crop {
				pendingVideo = nil
				continue
			}
			if bound.Revision != state.Revision {
				continue
			}
			if metadata.DeliverySequence <= barrierSequence {
				pendingVideo = nil
				continue
			}
			sequence = metadata.DeliverySequence
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
		case <-mappingChanged:
		case <-statusChanged:
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
