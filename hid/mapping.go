package hid

import (
	"context"
	"errors"
	"image"
	"math"
	"sync"
)

var (
	ErrMapping            = errors.New("invalid mouse mapping")
	ErrMappingUnavailable = errors.New("mouse mapping unavailable")
	ErrMappingChanged     = errors.New("mouse mapping changed")
)

type MappingSpan struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type MappingGeometry struct {
	Offset Point       `json:"offset"`
	Width  int         `json:"width"`
	Height int         `json:"height"`
	Span   MappingSpan `json:"span"`
}

type MappingTransform struct {
	ScaleX     float64 `json:"scale_x"`
	ScaleY     float64 `json:"scale_y"`
	TranslateX float64 `json:"translate_x"`
	TranslateY float64 `json:"translate_y"`
}

type MappingSet struct {
	Mode     string           `json:"mode,omitempty"`
	Geometry *MappingGeometry `json:"geometry,omitempty"`
}

// Sequence is the capture manager's delivery sequence, shared by all viewers.
type MappingCapture struct {
	Device     string          `json:"device"`
	Width      int             `json:"width"`
	Height     int             `json:"height"`
	Crop       image.Rectangle `json:"crop"`
	Generation uint64          `json:"generation"`
	Sequence   uint64          `json:"sequence"`
}

type MappingState struct {
	Mode      string            `json:"mode"`
	Revision  uint64            `json:"revision"`
	Ready     bool              `json:"ready"`
	Geometry  *MappingGeometry  `json:"geometry,omitempty"`
	Transform *MappingTransform `json:"transform,omitempty"`
	Capture   MappingCapture    `json:"capture"`
	Message   string            `json:"message,omitempty"`
}

type mouseMapping struct {
	mu         sync.Mutex
	update     chan struct{}
	state      MappingState
	changed    chan struct{}
	transition bool
	nextAction uint64
	actions    map[uint64]context.CancelFunc
}

func newMouseMapping() *mouseMapping {
	identity := MappingTransform{ScaleX: 1, ScaleY: 1}
	return &mouseMapping{state: MappingState{Mode: "legacy", Revision: 1, Ready: true, Transform: &identity}, changed: make(chan struct{}), update: make(chan struct{}, 1), actions: make(map[uint64]context.CancelFunc)}
}

func (mapping *mouseMapping) acquireUpdate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case mapping.update <- struct{}{}:
	}
	if err := ctx.Err(); err != nil {
		<-mapping.update
		return err
	}
	return nil
}

func (mapping *mouseMapping) notifyLocked() {
	close(mapping.changed)
	mapping.changed = make(chan struct{})
}

func (mapping *mouseMapping) snapshotLocked() MappingState {
	state := mapping.state
	if state.Geometry != nil {
		value := *state.Geometry
		state.Geometry = &value
	}
	if state.Transform != nil {
		value := *state.Transform
		state.Transform = &value
	}
	return state
}

func (mouse *Mouse) Mapping() MappingState { state, _ := mouse.WatchMapping(); return state }
func (mouse *Mouse) WatchMapping() (MappingState, <-chan struct{}) {
	mapping := mouse.mapping
	mapping.mu.Lock()
	defer mapping.mu.Unlock()
	return mapping.snapshotLocked(), mapping.changed
}

// Mapping transitions cancel old actions before waiting for their release cleanup.
func (mouse *Mouse) changeMapping(ctx context.Context, change func(*mouseMapping)) error {
	mapping := mouse.mapping
	if err := mapping.acquireUpdate(ctx); err != nil {
		return err
	}
	defer func() { <-mapping.update }()
	return mouse.changeMappingLocked(ctx, change)
}

func (mouse *Mouse) changeMappingLocked(ctx context.Context, change func(*mouseMapping)) error {
	mapping := mouse.mapping
	if err := ctx.Err(); err != nil {
		return err
	}
	mapping.mu.Lock()
	mapping.transition = true
	mapping.state.Revision++
	mapping.state.Ready = false
	for _, cancel := range mapping.actions {
		cancel()
	}
	mapping.notifyLocked()
	mapping.mu.Unlock()
	err := mouse.releaseMappingInput(ctx)
	mapping.mu.Lock()
	defer mapping.mu.Unlock()
	mapping.transition = false
	if err == nil {
		change(mapping)
	} else {
		mapping.state.Ready = mapping.state.Mode == "legacy"
		if !mapping.state.Ready {
			mapping.state.Transform = nil
		}
		mapping.state.Message = err.Error()
	}
	mapping.notifyLocked()
	return err
}

func (mouse *Mouse) releaseMappingInput(ctx context.Context) error {
	controller := mouse.controller
	if err := controller.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-controller.gate }()
	if !controller.prepared {
		return nil
	}
	attempt := controller.beginCleanup(ctx)
	err := controller.cleanup(attempt, releaseBoth)
	controller.endCleanup(attempt)
	return err
}

// SetMapping validates and activates complete display geometry.
func (mouse *Mouse) SetMapping(ctx context.Context, request MappingSet) error {
	if request.Mode != "" && request.Mode != "legacy" && request.Mode != "extended" || request.Mode == "legacy" && request.Geometry != nil || request.Mode == "" && request.Geometry == nil {
		return ErrMapping
	}
	var geometry *MappingGeometry
	var transform *MappingTransform
	if request.Geometry != nil {
		copy := *request.Geometry
		value, err := geometryTransform(copy)
		if err != nil {
			return err
		}
		geometry, transform = &copy, &value
	}
	return mouse.changeMapping(ctx, func(mapping *mouseMapping) {
		state := &mapping.state
		if geometry != nil {
			state.Mode, state.Geometry, state.Transform = "extended", geometry, transform
			state.Ready, state.Message = true, ""
		} else if request.Mode == "legacy" {
			state.Mode, state.Geometry = "legacy", nil
			state.Transform = &MappingTransform{ScaleX: 1, ScaleY: 1}
			state.Ready, state.Message = true, ""
		} else {
			if state.Mode == "legacy" {
				state.Transform = nil
			}
			state.Mode = "extended"
			state.Ready = state.Transform != nil
			state.Message = ""
			if !state.Ready {
				state.Message = "apply display geometry"
			}
		}
	})
}

func (mouse *Mouse) ResetMapping(ctx context.Context) error {
	return mouse.changeMapping(ctx, func(mapping *mouseMapping) {
		state := &mapping.state
		state.Geometry = nil
		if state.Mode == "legacy" {
			state.Transform = &MappingTransform{ScaleX: 1, ScaleY: 1}
			state.Ready = true
			state.Message = ""
		} else {
			state.Transform = nil
			state.Message = "apply display geometry"
		}
	})
}

func (mouse *Mouse) InvalidateMapping(ctx context.Context) error {
	return mouse.changeMapping(ctx, func(mapping *mouseMapping) {
		if mapping.state.Mode == "legacy" {
			mapping.state.Ready = true
			mapping.state.Message = ""
		} else {
			mapping.state.Transform = nil
			mapping.state.Message = "mapping invalidated; reapply display geometry"
		}
	})
}

func (mouse *Mouse) SetMappingCapture(ctx context.Context, capture MappingCapture) error {
	mapping := mouse.mapping
	if err := mapping.acquireUpdate(ctx); err != nil {
		return err
	}
	defer func() { <-mapping.update }()
	mapping.mu.Lock()
	previous := mapping.state.Capture
	if capture.Generation < previous.Generation || capture.Generation == previous.Generation && capture.Sequence < previous.Sequence {
		mapping.mu.Unlock()
		return nil
	}
	if previous == capture {
		mapping.mu.Unlock()
		return nil
	}
	// Filling a selection barrier establishes its first image, rather than
	// invalidating an older image or releasing input a second time.
	firstImage := previous.Device == capture.Device && previous.Generation == capture.Generation && previous.Width == 0 && previous.Height == 0 && previous.Sequence == 0 && capture.Width > 0 && capture.Height > 0
	if previous == (MappingCapture{}) || firstImage {
		mapping.state.Capture = capture
		mapping.notifyLocked()
		mapping.mu.Unlock()
		return nil
	}
	old, new := previous, capture
	old.Sequence = 0
	new.Sequence = 0
	if old == new {
		mapping.state.Capture.Sequence = capture.Sequence
		mapping.mu.Unlock()
		return nil
	}
	revision := mapping.state.Revision
	mapping.mu.Unlock()
	err := mouse.changeMappingLocked(ctx, func(mapping *mouseMapping) {
		mapping.state.Capture = capture
		if mapping.state.Mode == "legacy" {
			mapping.state.Ready = true
			mapping.state.Message = ""
		} else {
			mapping.state.Transform = nil
			mapping.state.Message = "capture changed; reapply display geometry"
		}
	})
	if err != nil {
		mapping.mu.Lock()
		if mapping.state.Revision != revision {
			// Capture changed independently of HID. Record an admitted transition
			// even when release fails, so later frames do not repeat cleanup.
			mapping.state.Capture = capture
			mapping.notifyLocked()
		}
		mapping.mu.Unlock()
	}
	return err
}

func geometryTransform(geometry MappingGeometry) (MappingTransform, error) {
	if geometry.Width < 2 || geometry.Height < 2 || geometry.Span.Width < 2 || geometry.Span.Height < 2 || geometry.Width > geometry.Span.Width || geometry.Height > geometry.Span.Height {
		return MappingTransform{}, ErrMapping
	}
	// Unsigned differences preserve even desktop spans that cross signed zero.
	dx := uint64(geometry.Offset.X) - uint64(geometry.Span.X)
	dy := uint64(geometry.Offset.Y) - uint64(geometry.Span.Y)
	if geometry.Offset.X < geometry.Span.X || geometry.Offset.Y < geometry.Span.Y || dx > uint64(geometry.Span.Width-geometry.Width) || dy > uint64(geometry.Span.Height-geometry.Height) {
		return MappingTransform{}, ErrMapping
	}
	x, y := float64(dx), float64(dy)
	return MappingTransform{ScaleX: float64(geometry.Width-1) / float64(geometry.Span.Width-1), ScaleY: float64(geometry.Height-1) / float64(geometry.Span.Height-1), TranslateX: 4095 * x / float64(geometry.Span.Width-1), TranslateY: 4095 * y / float64(geometry.Span.Height-1)}, nil
}

type mappingRevisionKey struct{}

func WithMappingRevision(ctx context.Context, revision uint64) context.Context {
	return context.WithValue(ctx, mappingRevisionKey{}, revision)
}

func (mouse *Mouse) prepareMapping(ctx context.Context, points ...Point) ([]Point, uint64, error) {
	mapping := mouse.mapping
	mapping.mu.Lock()
	defer mapping.mu.Unlock()
	state := mapping.state
	if expected, ok := ctx.Value(mappingRevisionKey{}).(uint64); ok && expected != state.Revision {
		return nil, 0, ErrMappingChanged
	}
	if mapping.transition || !state.Ready || state.Transform == nil {
		return nil, 0, ErrMappingUnavailable
	}
	result := make([]Point, len(points))
	for index, point := range points {
		value, err := state.Transform.apply(point)
		if err != nil {
			return nil, 0, err
		}
		result[index] = value
	}
	return result, state.Revision, nil
}

func (transform MappingTransform) apply(point Point) (Point, error) {
	if _, err := normalizedPoint(point.X, point.Y); err != nil {
		return Point{}, err
	}
	x := float64(point.X)*transform.ScaleX + transform.TranslateX
	y := float64(point.Y)*transform.ScaleY + transform.TranslateY
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) || x < -1e-7 || x > 4095+1e-7 || y < -1e-7 || y > 4095+1e-7 {
		return Point{}, ErrCoordinates
	}
	return Point{int(math.Round(x)), int(math.Round(y))}, nil
}

func (mouse *Mouse) runMapped(ctx context.Context, revision uint64, execute func(*action) error) error {
	return mouse.runMappedMask(ctx, revision, releaseMouse, execute)
}

func (mouse *Mouse) runMappedMask(ctx context.Context, revision uint64, release releaseMask, execute func(*action) error) error {
	mapping := mouse.mapping
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	mapping.mu.Lock()
	if mapping.transition || mapping.state.Revision != revision {
		mapping.mu.Unlock()
		return ErrMappingChanged
	}
	mapping.nextAction++
	id := mapping.nextAction
	mapping.actions[id] = cancel
	mapping.mu.Unlock()
	defer func() { mapping.mu.Lock(); delete(mapping.actions, id); mapping.mu.Unlock() }()
	err := mouse.controller.run(ctx, release, func(current *action) error {
		mapping.mu.Lock()
		valid := !mapping.transition && mapping.state.Revision == revision
		mapping.mu.Unlock()
		if !valid {
			return ErrMappingChanged
		}
		return execute(current)
	})
	mapping.mu.Lock()
	changed := mapping.state.Revision != revision
	mapping.mu.Unlock()
	if changed {
		return errors.Join(ErrMappingChanged, err)
	}
	return err
}
