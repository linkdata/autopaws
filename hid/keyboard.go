package hid

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/linkdata/autopaws/ch9329"
)

// MaxDelay bounds character and sequence-step delays. MaxSequenceSteps bounds
// a key sequence; the wire API validates its numeric units before conversion.
const (
	MaxDelay         = time.Minute
	MaxSequenceSteps = 4096
)

var (
	ErrUnsupported = errors.New("unsupported keyboard character")
	ErrDelay       = errors.New("keyboard delay out of range")
	ErrSequence    = errors.New("too many keyboard sequence steps")
)

// TypeOptions controls literal typing and the delay after each character/tag,
// including the last. Nil CharDelay uses 20 ms; an explicit zero omits the delay.
type TypeOptions struct {
	Raw       bool
	CharDelay *time.Duration
}

// KeyStep describes a key and its delay after execution, including the last step.
// A nil Delay uses the sequence default; an explicit zero omits the delay.
type KeyStep struct {
	Key       string
	Modifiers []string
	Delay     *time.Duration
}

// Keyboard resolves text using its currently selected [Layout].
//
// Methods may be called concurrently. Each text, key or sequence call uses one
// layout snapshot. A Keyboard must not be copied after first use.
type Keyboard struct {
	controller *Controller
	layout     atomic.Pointer[Layout]
}

// NewKeyboard creates a keyboard sharing controller's action gate.
//
// Nil dependencies are programming errors and panic; a zero [Layout] is valid.
func NewKeyboard(controller *Controller, layout *Layout) *Keyboard {
	if controller == nil || layout == nil {
		panic("hid: nil keyboard dependency")
	}
	keyboard := &Keyboard{controller: controller}
	keyboard.layout.Store(layout)
	return keyboard
}

// SetLayout selects an immutable [Layout].
//
// It does not prepare or send to the transport and may run concurrently with
// keyboard operations. Each operation snapshots its layout before resolving
// input. A nil layout is a programming error and panics.
func (keyboard *Keyboard) SetLayout(layout *Layout) {
	if layout == nil {
		panic("hid: nil keyboard layout")
	}
	keyboard.layout.Store(layout)
}

type keyGroup struct {
	strokes []Stroke
	delay   time.Duration
}

func delayValue(value *time.Duration, fallback time.Duration) (time.Duration, error) {
	delay := fallback
	if value != nil {
		delay = *value
	}
	if delay < 0 || delay > MaxDelay {
		return 0, fmt.Errorf("%w: %s (allowed 0..%s)", ErrDelay, delay, MaxDelay)
	}
	return delay, nil
}

// TypeText validates the entire text before preparing hardware, then types it as
// one action. Raw mode treats tags/braces literally and normalizes CRLF/CR to LF.
func (keyboard *Keyboard) TypeText(ctx context.Context, text string, options TypeOptions) error {
	delay, err := delayValue(options.CharDelay, 20*time.Millisecond)
	if err != nil {
		return err
	}
	layout := keyboard.layout.Load()
	var plan []keyGroup
	for _, token := range tokenize(text, options.Raw) {
		var strokes []Stroke
		if token.tag {
			parts := strings.Split(token.text, "+")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
			}
			strokes, err = layout.resolveKey(parts[len(parts)-1], parts[:len(parts)-1])
		} else if options.Raw && token.text == "\n" {
			strokes, err = layout.resolveKey("enter", nil)
		} else {
			character, _ := utf8.DecodeRuneInString(token.text)
			var ok bool
			strokes, ok = layout.Lookup(character)
			if !ok {
				err = fmt.Errorf("%w: %q", ErrUnsupported, character)
			}
		}
		if err != nil {
			return fmt.Errorf("rune %d: %w", token.position, err)
		}
		plan = append(plan, keyGroup{strokes: strokes, delay: delay})
	}
	return keyboard.play(ctx, plan)
}

// SendKey sends one named/hex key or layout character with optional modifiers.
// Explicit modifiers apply to every stroke of a multi-stroke layout character.
func (keyboard *Keyboard) SendKey(ctx context.Context, key string, modifiers []string) error {
	strokes, err := keyboard.layout.Load().resolveKey(key, modifiers)
	if err != nil {
		return err
	}
	return keyboard.play(ctx, []keyGroup{{strokes: strokes}})
}

// SendKeySequence validates and executes all steps as one action. A nil default
// delay uses 100 ms; each step may override it, including with an explicit zero.
func (keyboard *Keyboard) SendKeySequence(ctx context.Context, steps []KeyStep, defaultDelay *time.Duration) error {
	if len(steps) > MaxSequenceSteps {
		return ErrSequence
	}
	delay, err := delayValue(defaultDelay, 100*time.Millisecond)
	if err != nil {
		return err
	}
	layout := keyboard.layout.Load()
	plan := make([]keyGroup, len(steps))
	for i, step := range steps {
		strokes, err := layout.resolveKey(step.Key, step.Modifiers)
		if err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		stepDelay, err := delayValue(step.Delay, delay)
		if err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		plan[i] = keyGroup{strokes: strokes, delay: stepDelay}
	}
	return keyboard.play(ctx, plan)
}

func (layout *Layout) resolveKey(key string, names []string) ([]Stroke, error) {
	var bits byte
	for _, name := range names {
		bit, ok := Modifier(name)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrModifier, name)
		}
		bits |= bit
	}
	if code, ok := SpecialKey(key); ok {
		return []Stroke{{Modifier: bits, Keycode: code}}, nil
	}
	if utf8.RuneCountInString(key) != 1 {
		return nil, fmt.Errorf("%w: %q", ErrKeycode, key)
	}
	character, _ := utf8.DecodeRuneInString(key)
	strokes, ok := layout.Lookup(character)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupported, character)
	}
	for i := range strokes {
		strokes[i].Modifier |= bits
	}
	return strokes, nil
}

func (keyboard *Keyboard) play(ctx context.Context, plan []keyGroup) error {
	if len(plan) == 0 {
		return ctx.Err()
	}
	return keyboard.controller.run(ctx, releaseKeyboard, func(current *action) error {
		for _, group := range plan {
			for _, stroke := range group.strokes {
				if err := current.ctx.Err(); err != nil {
					return err
				}
				if err := current.write(ch9329.Keyboard(stroke.Modifier, stroke.Keycode)); err != nil {
					return err
				}
				if err := wait(current.ctx, 10*time.Millisecond); err != nil {
					return err
				}
				if err := current.write(ch9329.Keyboard(0)); err != nil {
					return err
				}
			}
			if err := wait(current.ctx, group.delay); err != nil {
				return err
			}
		}
		return nil
	})
}

func wait(ctx context.Context, delay time.Duration) error {
	if delay == 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
