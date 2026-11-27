//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/linkdata/autopaws/hid"
)

type mappingCaptureConfig struct {
	hid.MappingCapture
	Generation string `json:"generation"`
	Sequence   string `json:"sequence"`
}

type mappingConfig struct {
	hid.MappingState
	Revision        string               `json:"revision"`
	Capture         mappingCaptureConfig `json:"capture"`
	FrameGeneration string               `json:"frame_generation"`
	AfterSequence   string               `json:"after_sequence"`
	Error           string               `json:"error,omitempty"`
}

func pointerConfig(state hid.MappingState, generation, sequence uint64, detail string) mappingConfig {
	return mappingConfig{
		MappingState: state, Revision: strconv.FormatUint(state.Revision, 10),
		Capture:         mappingCaptureConfig{state.Capture, strconv.FormatUint(state.Capture.Generation, 10), strconv.FormatUint(state.Capture.Sequence, 10)},
		FrameGeneration: strconv.FormatUint(generation, 10),
		AfterSequence:   strconv.FormatUint(sequence, 10), Error: detail,
	}
}

// The reader owns Browser; only status and the writer's frame barrier are shared.
type browserInput struct {
	server  *Server
	browser *hid.Browser
	mu      sync.Mutex
	status  string
	changed chan struct{}
	barrier struct{ revision, generation, sequence uint64 }
}

func (server *Server) newBrowserInput(browser *hid.Browser) *browserInput {
	return &browserInput{server: server, browser: browser, changed: make(chan struct{})}
}

func (input *browserInput) setStatus(detail string) {
	input.mu.Lock()
	defer input.mu.Unlock()
	if input.status != detail {
		input.status = detail
		close(input.changed)
		input.changed = make(chan struct{})
	}
}

func (input *browserInput) statusWatch() (string, <-chan struct{}) {
	input.mu.Lock()
	defer input.mu.Unlock()
	return input.status, input.changed
}

func wireUint(value string) (uint64, error) {
	result, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, ErrInput
	}
	return result, nil
}

func (input *browserInput) checkGoal(event inputEvent) (uint64, error) {
	state := input.server.mouse.Mapping()
	if event.Revision == "" && state.Mode == "legacy" {
		return state.Revision, nil // Preserve existing clients in identity mode.
	}
	revision, err := wireUint(event.Revision)
	if err != nil {
		return 0, hid.ErrMappingChanged
	}
	generation, err := wireUint(event.FrameGeneration)
	if err != nil {
		return 0, hid.ErrMappingChanged
	}
	sequence, err := wireUint(event.FrameSequence)
	if err != nil {
		return 0, hid.ErrMappingChanged
	}
	input.mu.Lock()
	barrier := input.barrier
	input.mu.Unlock()
	if revision != state.Revision || revision != barrier.revision || generation != barrier.generation || sequence <= barrier.sequence {
		return 0, hid.ErrMappingChanged
	}
	return revision, nil
}

func (input *browserInput) apply(ctx context.Context, event inputEvent) error {
	switch event.Type {
	case "mapping_set":
		if event.Mapping == nil {
			return ErrInput
		}
		return input.server.mouse.SetMapping(ctx, *event.Mapping)
	case "mapping_reset":
		return input.server.mouse.ResetMapping(ctx)
	case "mousemove", "mousedown", "mouseup":
		if event.X == nil || event.Y == nil || event.Buttons == nil || *event.Buttons < 0 || *event.Buttons > 7 {
			return ErrInput
		}
		revision, err := input.checkGoal(event)
		if err != nil {
			return err
		}
		if event.Type == "mousemove" {
			return input.browser.Move(hid.WithMappingRevision(ctx, revision), *event.X, *event.Y)
		}
		return input.browser.Buttons(hid.WithMappingRevision(ctx, revision), *event.X, *event.Y, *event.Buttons)
	case "keydown", "keyup":
		if event.Code == nil {
			return ErrInput
		}
		return input.browser.Key(ctx, *event.Code, event.Type == "keydown")
	case "scroll":
		if event.DeltaY == nil {
			return ErrInput
		}
		return input.browser.Scroll(ctx, int(math.Max(-127, math.Min(127, *event.DeltaY))))
	case "release_all":
		return input.browser.Release(ctx)
	case "shortcut":
		if event.Keys == nil {
			return ErrInput
		}
		return input.browser.Shortcut(ctx, *event.Keys)
	default:
		return ErrInput
	}
}

func (input *browserInput) recover(ctx context.Context, err error) error {
	if !errors.Is(err, hid.ErrMapping) && !errors.Is(err, hid.ErrMappingChanged) &&
		!errors.Is(err, hid.ErrMappingUnavailable) && !errors.Is(err, hid.ErrCoordinates) {
		return err
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if releaseErr := input.browser.Release(cleanup); releaseErr != nil {
		return errors.Join(err, releaseErr)
	}
	input.setStatus(err.Error())
	return nil
}
