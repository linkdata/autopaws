package hid

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

type frameEvent struct {
	Frame []byte
	At    time.Duration
}

type recordingTransport struct {
	mu          sync.Mutex
	start       time.Time
	generation  uint64
	prepares    int
	writes      int
	closes      int
	events      []frameEvent
	prepareHook func(context.Context, int) (uint64, error)
	writeHook   func(context.Context, int, []byte) error
	closeError  error
}

func (transport *recordingTransport) Prepare(ctx context.Context) (uint64, error) {
	transport.mu.Lock()
	transport.prepares++
	n := transport.prepares
	hook, generation := transport.prepareHook, transport.generation
	transport.mu.Unlock()
	if hook != nil {
		return hook(ctx, n)
	}
	return generation, ctx.Err()
}

func (transport *recordingTransport) WriteFrame(ctx context.Context, frame []byte) error {
	transport.mu.Lock()
	transport.writes++
	n := transport.writes
	transport.events = append(transport.events, frameEvent{Frame: bytes.Clone(frame), At: time.Since(transport.start)})
	hook := transport.writeHook
	transport.mu.Unlock()
	if hook != nil {
		return hook(ctx, n, frame)
	}
	return ctx.Err()
}

func (transport *recordingTransport) Close() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.closes++
	return transport.closeError
}

func (transport *recordingTransport) snapshot() (int, int, int, []frameEvent) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.prepares, transport.writes, transport.closes, append([]frameEvent(nil), transport.events...)
}

func keyboardFixture(t *testing.T, name string) (*Controller, *Keyboard, *recordingTransport) {
	t.Helper()
	transport := &recordingTransport{start: time.Now(), generation: 1}
	controller := NewController(transport)
	return controller, NewKeyboard(controller, mustLoad(t, name, "")), transport
}

func duration(value time.Duration) *time.Duration { return &value }

func TestKeyboardActionGoldens(t *testing.T) {
	var golden struct {
		Cases []struct {
			Name    string            `json:"name"`
			Method  string            `json:"method"`
			Args    []json.RawMessage `json:"args"`
			Options struct {
				Raw          bool     `json:"raw"`
				CharDelay    *float64 `json:"char_delay"`
				DefaultDelay *int     `json:"default_delay_ms"`
			} `json:"options"`
			Events []struct {
				FrameHex string `json:"frame_hex"`
				DelayMS  int    `json:"delay_ms"`
			} `json:"events"`
		} `json:"cases"`
	}
	readJSON(t, "keyboard_actions.json", &golden)
	if len(golden.Cases) != 10 {
		t.Fatalf("keyboard action cases = %d; want 10", len(golden.Cases))
	}
	for _, vector := range golden.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, keyboard, transport := keyboardFixture(t, "us104")
				var err error
				switch vector.Method {
				case "type_text":
					var text string
					decodeArgument(t, vector.Args[0], &text)
					options := TypeOptions{Raw: vector.Options.Raw}
					if vector.Options.CharDelay != nil {
						options.CharDelay = duration(time.Duration(*vector.Options.CharDelay * float64(time.Second)))
					}
					err = keyboard.TypeText(context.Background(), text, options)
				case "send_key":
					var key string
					var modifiers []string
					decodeArgument(t, vector.Args[0], &key)
					if len(vector.Args) > 1 {
						decodeArgument(t, vector.Args[1], &modifiers)
					}
					err = keyboard.SendKey(context.Background(), key, modifiers)
				case "send_key_sequence":
					var steps []struct {
						Key       string   `json:"key"`
						Modifiers []string `json:"modifiers"`
						Delay     *int     `json:"delay_ms"`
					}
					decodeArgument(t, vector.Args[0], &steps)
					plan := make([]KeyStep, len(steps))
					for i, step := range steps {
						plan[i] = KeyStep{Key: step.Key, Modifiers: step.Modifiers}
						if step.Delay != nil {
							plan[i].Delay = duration(time.Duration(*step.Delay) * time.Millisecond)
						}
					}
					var defaultDelay *time.Duration
					if vector.Options.DefaultDelay != nil {
						defaultDelay = duration(time.Duration(*vector.Options.DefaultDelay) * time.Millisecond)
					}
					err = keyboard.SendKeySequence(context.Background(), plan, defaultDelay)
				default:
					t.Fatalf("unknown fixture method %q", vector.Method)
				}
				if err != nil {
					t.Fatal(err)
				}
				var want []frameEvent
				var elapsed time.Duration
				for _, event := range vector.Events {
					if event.FrameHex != "" {
						frame, err := hex.DecodeString(event.FrameHex)
						if err != nil {
							t.Fatal(err)
						}
						want = append(want, frameEvent{Frame: frame, At: elapsed})
					} else {
						elapsed += time.Duration(event.DelayMS) * time.Millisecond
					}
				}
				prepares, _, _, got := transport.snapshot()
				if prepares != 1 || !reflect.DeepEqual(got, want) || time.Since(transport.start) != elapsed {
					t.Fatalf("prepares=%d events=%v elapsed=%s; want prepares=1 events=%v elapsed=%s", prepares, got, time.Since(transport.start), want, elapsed)
				}
			})
		})
	}
}

func decodeArgument(t *testing.T, data json.RawMessage, output any) {
	t.Helper()
	if err := json.Unmarshal(data, output); err != nil {
		t.Fatal(err)
	}
}

func TestKeyboardValidationBeforePreparation(t *testing.T) {
	_, keyboard, transport := keyboardFixture(t, "us104")
	cases := []struct {
		name string
		call func() error
		want error
	}{
		{"late character", func() error { return keyboard.TypeText(context.Background(), "abcé", TypeOptions{}) }, ErrUnsupported},
		{"unsupported tag", func() error { return keyboard.TypeText(context.Background(), "a{é}", TypeOptions{}) }, ErrUnsupported},
		{"literal tag marker", func() error { return keyboard.TypeText(context.Background(), "a\x01x", TypeOptions{}) }, ErrUnsupported},
		{"negative character delay", func() error {
			return keyboard.TypeText(context.Background(), "a", TypeOptions{CharDelay: duration(-1)})
		}, ErrDelay},
		{"large character delay", func() error {
			return keyboard.TypeText(context.Background(), "a", TypeOptions{CharDelay: duration(MaxDelay + 1)})
		}, ErrDelay},
		{"unknown key", func() error { return keyboard.SendKey(context.Background(), "unknown", nil) }, ErrKeycode},
		{"unknown modifier", func() error { return keyboard.SendKey(context.Background(), "a", []string{"hyper"}) }, ErrModifier},
		{"unsupported key", func() error { return keyboard.SendKey(context.Background(), "é", nil) }, ErrUnsupported},
		{"late step", func() error {
			return keyboard.SendKeySequence(context.Background(), []KeyStep{{Key: "a"}, {Key: "unknown"}}, nil)
		}, ErrKeycode},
		{"late modifier", func() error {
			return keyboard.SendKeySequence(context.Background(), []KeyStep{{Key: "a"}, {Key: "b", Modifiers: []string{"hyper"}}}, nil)
		}, ErrModifier},
		{"late delay", func() error {
			return keyboard.SendKeySequence(context.Background(), []KeyStep{{Key: "a"}, {Key: "b", Delay: duration(-1)}}, nil)
		}, ErrDelay},
		{"default delay", func() error {
			return keyboard.SendKeySequence(context.Background(), nil, duration(MaxDelay+1))
		}, ErrDelay},
		{"sequence length", func() error {
			return keyboard.SendKeySequence(context.Background(), make([]KeyStep, MaxSequenceSteps+1), nil)
		}, ErrSequence},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, test.want) {
				t.Fatalf("error=%v; want %v", err, test.want)
			}
		})
	}
	if err := keyboard.TypeText(context.Background(), "åabc", TypeOptions{}); !strings.Contains(err.Error(), "rune 0") || !strings.Contains(err.Error(), "å") {
		t.Fatalf("unsupported-character location missing: %v", err)
	}
	for _, call := range []func() error{
		func() error { return keyboard.TypeText(context.Background(), "", TypeOptions{}) },
		func() error { return keyboard.SendKeySequence(context.Background(), nil, nil) },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := keyboard.TypeText(ctx, "a", TypeOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
		t.Fatalf("validation/empty/cancelled input touched hardware: prepare=%d write=%d", prepares, writes)
	}
}

func TestKeyboardSequenceDefaultsAndRunePosition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, keyboard, transport := keyboardFixture(t, "se105")
		if err := keyboard.TypeText(context.Background(), "å😀", TypeOptions{}); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "rune 1") {
			t.Fatalf("expected original rune position 1: %v", err)
		}
		if prepares, writes, _, _ := transport.snapshot(); prepares != 0 || writes != 0 {
			t.Fatalf("partial Unicode validation emitted input: %d/%d", prepares, writes)
		}
		if err := keyboard.SendKeySequence(context.Background(), []KeyStep{{Key: "a"}}, nil); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(transport.start); elapsed != 110*time.Millisecond {
			t.Fatalf("default sequence timing=%s; want 110ms including final step delay", elapsed)
		}
	})
}

func TestKeyboardRawNewlinesIgnoreLayout(t *testing.T) {
	for name, layout := range map[string]*Layout{
		"empty":  {},
		"custom": {characters: map[rune][]Stroke{'\n': {{Keycode: 4}}}},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				transport := &recordingTransport{start: time.Now(), generation: 1}
				keyboard := NewKeyboard(NewController(transport), layout)
				if err := keyboard.TypeText(context.Background(), "\r\n\r\n", TypeOptions{Raw: true, CharDelay: duration(0)}); err != nil {
					t.Fatal(err)
				}
				_, _, _, events := transport.snapshot()
				if len(events) != 4 || !bytes.Equal(events[0].Frame, ch9329.Keyboard(0, 0x28)) || !bytes.Equal(events[2].Frame, ch9329.Keyboard(0, 0x28)) {
					t.Fatalf("raw line endings did not produce Enter: %v", events)
				}
				if name == "custom" {
					if err := keyboard.TypeText(context.Background(), "\n", TypeOptions{}); err != nil {
						t.Fatal(err)
					}
					_, _, _, events = transport.snapshot()
					if !bytes.Equal(events[4].Frame, ch9329.Keyboard(0, 4)) {
						t.Fatalf("nonraw LF ignored the custom layout: %v", events[4])
					}
				}
			})
		})
	}
}

func TestKeyboardStrokeSequences(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, keyboard, transport := keyboardFixture(t, "se105")
		if err := keyboard.TypeText(context.Background(), "^å", TypeOptions{}); err != nil {
			t.Fatal(err)
		}
		_, _, _, events := transport.snapshot()
		want := []frameEvent{
			{ch9329.Keyboard(2, 0x30), 0},
			{ch9329.Keyboard(0), 10 * time.Millisecond},
			{ch9329.Keyboard(0, 0x2c), 10 * time.Millisecond},
			{ch9329.Keyboard(0), 20 * time.Millisecond},
			{ch9329.Keyboard(0, 0x2f), 40 * time.Millisecond},
			{ch9329.Keyboard(0), 50 * time.Millisecond},
		}
		if !reflect.DeepEqual(events, want) || time.Since(transport.start) != 70*time.Millisecond {
			t.Fatalf("dead-key timing=%v elapsed=%s; want %v and 70ms", events, time.Since(transport.start), want)
		}
		if err := keyboard.SendKey(context.Background(), "^", []string{"ctrl"}); err != nil {
			t.Fatal(err)
		}
		_, _, _, events = transport.snapshot()
		if !bytes.Equal(events[6].Frame, ch9329.Keyboard(3, 0x30)) || !bytes.Equal(events[8].Frame, ch9329.Keyboard(1, 0x2c)) {
			t.Fatalf("explicit modifiers were not applied to every stroke: %v", events[6:])
		}
	})
}

func TestDependencyPanics(t *testing.T) {
	for _, construct := range []func(){
		func() { NewController(nil) },
		func() { NewKeyboard(nil, &Layout{}) },
		func() { NewKeyboard(NewController(&recordingTransport{}), nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("nil dependency did not panic")
				}
			}()
			construct()
		}()
	}
}
