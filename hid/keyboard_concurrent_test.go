package hid

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/linkdata/autopaws/ch9329"
)

func TestKeyboardLayoutSwitchKeepsActivePlan(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(context.Context, *Keyboard) error
		want []Stroke
	}{
		{"text", func(ctx context.Context, keyboard *Keyboard) error {
			return keyboard.TypeText(ctx, "@@", TypeOptions{CharDelay: duration(0)})
		}, []Stroke{{Modifier: 2, Keycode: 0x1f}, {Modifier: 2, Keycode: 0x1f}}},
		{"tags", func(ctx context.Context, keyboard *Keyboard) error {
			return keyboard.TypeText(ctx, "@{ctrl+@}", TypeOptions{CharDelay: duration(0)})
		}, []Stroke{{Modifier: 2, Keycode: 0x1f}, {Modifier: 3, Keycode: 0x1f}}},
		{"raw newline", func(ctx context.Context, keyboard *Keyboard) error {
			return keyboard.TypeText(ctx, "@\n@", TypeOptions{Raw: true, CharDelay: duration(0)})
		}, []Stroke{{Modifier: 2, Keycode: 0x1f}, {Keycode: 0x28}, {Modifier: 2, Keycode: 0x1f}}},
		{"key", func(ctx context.Context, keyboard *Keyboard) error {
			return keyboard.SendKey(ctx, "@", nil)
		}, []Stroke{{Modifier: 2, Keycode: 0x1f}}},
		{"sequence", func(ctx context.Context, keyboard *Keyboard) error {
			return keyboard.SendKeySequence(ctx, []KeyStep{{Key: "@"}, {Key: "@"}}, duration(0))
		}, []Stroke{{Modifier: 2, Keycode: 0x1f}, {Modifier: 2, Keycode: 0x1f}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				controller, keyboard, transport := keyboardFixture(t, "us104")
				defer controller.Close()
				layout := mustLoad(t, "se105", "")
				transport.writeHook = func(ctx context.Context, n int, _ []byte) error {
					if n == 1 {
						keyboard.SetLayout(layout)
					}
					return ctx.Err()
				}
				if err := test.call(t.Context(), keyboard); err != nil {
					t.Fatal(err)
				}
				if err := keyboard.SendKey(t.Context(), "@", nil); err != nil {
					t.Fatal(err)
				}
				_, _, _, events := transport.snapshot()
				want := append(test.want, Stroke{Modifier: 0x40, Keycode: 0x1f})
				if len(events) != 2*len(want) {
					t.Fatalf("reports = %d; want %d", len(events), 2*len(want))
				}
				for i, stroke := range want {
					if !bytes.Equal(events[2*i].Frame, ch9329.Keyboard(stroke.Modifier, stroke.Keycode)) ||
						!bytes.Equal(events[2*i+1].Frame, ch9329.Keyboard(0)) {
						t.Fatalf("stroke %d did not use its action's layout", i)
					}
				}
			})
		})
	}
}

func TestKeyboardConcurrentLayoutPlanningUsesOneSnapshot(t *testing.T) {
	controller, keyboard, transport := keyboardFixture(t, "us104")
	defer controller.Close()
	layout, empty := &Layout{characters: map[rune][]Stroke{'a': {{Keycode: 4}}}}, &Layout{}
	started, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		keyboard.SetLayout(empty)
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				keyboard.SetLayout(layout)
				keyboard.SetLayout(empty)
			}
		}
	}()
	defer func() { close(stop); <-done }()
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	steps := make([]KeyStep, 1024)
	for i := range steps {
		steps[i].Key = "a"
	}
	for _, test := range []struct {
		name   string
		call   func() error
		prefix string
	}{
		{"text", func() error {
			return keyboard.TypeText(ctx, strings.Repeat("a", 1024), TypeOptions{})
		}, "rune 0: "},
		{"tags", func() error {
			return keyboard.TypeText(ctx, strings.Repeat("{a}", 1024), TypeOptions{})
		}, "rune 0: "},
		{"raw newline", func() error {
			return keyboard.TypeText(ctx, strings.Repeat("a\n", 512), TypeOptions{Raw: true})
		}, "rune 0: "},
		{"sequence", func() error {
			return keyboard.SendKeySequence(ctx, steps, duration(0))
		}, "step 0: "},
	} {
		t.Run(test.name, func(t *testing.T) {
			for range 64 {
				err := test.call()
				if !errors.Is(err, context.Canceled) && (!errors.Is(err, ErrUnsupported) || !strings.HasPrefix(err.Error(), test.prefix)) {
					t.Fatalf("operation mixed layout snapshots: %v", err)
				}
			}
		})
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
		t.Fatal("canceled planning touched hardware")
	}
}
