package hid

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

type layoutGolden struct {
	Layouts map[string][]struct {
		Character string    `json:"character"`
		Strokes   [][2]byte `json:"strokes"`
	} `json:"layouts"`
}

func readJSON(t *testing.T, name string, output any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, output); err != nil {
		t.Fatal(err)
	}
}

func mustLoad(t *testing.T, name, userDirectory string) *Layout {
	t.Helper()
	layout, err := LoadLayout(name, userDirectory)
	if err != nil {
		t.Fatal(err)
	}
	return layout
}

func assertLookup(t *testing.T, layout *Layout, character rune, want []Stroke) {
	t.Helper()
	got, ok := layout.Lookup(character)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("Lookup(%q) = %v, %v; want %v, true", character, got, ok, want)
	}
}

func TestLayoutGoldens(t *testing.T) {
	for fixture, source := range map[string]struct {
		names             []string
		userDirectory     string
		inheritedControls bool
	}{
		"python_layouts.json":         {names: []string{"jp106", "us104"}},
		"xkb_layouts.json":            {names: []string{"de105", "fr105", "se105", "uk105"}, inheritedControls: true},
		"windows_swedish_layout.json": {names: []string{"se105-windows"}, userDirectory: filepath.Join("..", "examples", "layouts"), inheritedControls: true},
	} {
		var golden layoutGolden
		readJSON(t, fixture, &golden)
		if got := slices.Sorted(maps.Keys(golden.Layouts)); !slices.Equal(got, source.names) {
			t.Fatalf("%s layout names = %v; want %v", fixture, got, source.names)
		}
		for name, vectors := range golden.Layouts {
			t.Run(name, func(t *testing.T) {
				layout := mustLoad(t, name, source.userDirectory)
				for _, vector := range vectors {
					want := make([]Stroke, len(vector.Strokes))
					for i, stroke := range vector.Strokes {
						want[i] = Stroke{Modifier: stroke[0], Keycode: stroke[1]}
					}
					assertLookup(t, layout, []rune(vector.Character)[0], want)
				}
				wantCount := len(vectors)
				if source.inheritedControls {
					wantCount += 2 // Tab and LF inherit the US104 base.
				}
				if len(layout.characters) != wantCount {
					t.Fatalf("layout has %d characters; want %d", len(layout.characters), wantCount)
				}
				if got, ok := layout.Lookup('\x00'); ok || got != nil {
					t.Fatalf("unexpected NUL mapping: %v, %v", got, ok)
				}
			})
		}
	}
}

func TestLayoutPrecedence(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "se105.yaml")
	if err := os.WriteFile(path, []byte("overrides:\n  'å': [none, 0x04]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	custom := mustLoad(t, "se105", directory)
	assertLookup(t, custom, 'å', []Stroke{{Keycode: 0x04}})
	assertLookup(t, custom, '@', []Stroke{{Modifier: 0x02, Keycode: 0x1f}})
	if _, ok := custom.Lookup('ä'); ok {
		t.Fatal("a user file inherited built-in Swedish overrides instead of the US base")
	}
	assertLookup(t, mustLoad(t, "se105", ""), 'å', []Stroke{{Keycode: 0x2f}})
	assertLookup(t, mustLoad(t, "uk105", directory), '\\', []Stroke{{Keycode: 0x64}})
	assertLookup(t, mustLoad(t, "us104", filepath.Join(directory, "missing")), 'a', []Stroke{{Keycode: 0x04}})

	if err := os.WriteFile(path, []byte("overrides: [broken]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLayout("se105", directory); !errors.Is(err, ErrLayout) || !strings.Contains(err.Error(), path) {
		t.Fatalf("malformed user layout fell back or lost its file/error context: %v", err)
	}
	if err := os.Mkdir(filepath.Join(directory, "us104.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLayout("us104", directory); !errors.Is(err, ErrLayout) {
		t.Fatalf("unreadable selected user path fell back: %v", err)
	}
	if err := os.Symlink(filepath.Join(directory, "absent.yaml"), filepath.Join(directory, "uk105.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLayout("uk105", directory); !errors.Is(err, ErrLayout) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling user layout symlink fell back or lost the read error: %v", err)
	}
	blocked := filepath.Join(directory, "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLayout("us104", blocked); !errors.Is(err, ErrLayout) {
		t.Fatalf("invalid user directory fell back: %v", err)
	}

	if err := os.WriteFile(filepath.Join(directory, "custom.yaml"), []byte("overrides: {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertLookup(t, mustLoad(t, "custom", directory), 'a', []Stroke{{Keycode: 0x04}})
	for _, directory := range []string{"", directory} {
		for _, name := range []string{"unknown", "", ".", "..", "a/b", `a\b`, "a\x00"} {
			if _, err := LoadLayout(name, directory); !errors.Is(err, ErrUnknownLayout) || !errors.Is(err, ErrLayout) {
				t.Errorf("LoadLayout(%q, %q) error = %v; want unknown layout", name, directory, err)
			}
		}
	}
}

func TestLayoutSequencesAndKeycodeForms(t *testing.T) {
	layout, err := decodeLayout([]byte(`overrides:
  "é": [[" Shift + RALT ", "0X2c"], [NONE, 0]]
  "😀": [ctrl+lctrl, 255]
  "å": [none, 0x2F]
  "0": [none, 044]
`))
	if err != nil {
		t.Fatal(err)
	}
	assertLookup(t, layout, 'é', []Stroke{{Modifier: 0x42, Keycode: 0x2c}, {}})
	assertLookup(t, layout, '😀', []Stroke{{Modifier: 0x01, Keycode: 0xff}})
	assertLookup(t, layout, 'å', []Stroke{{Keycode: 0x2f}})
	assertLookup(t, layout, '0', []Stroke{{Keycode: 0x24}})
	var empty Layout
	if got, ok := empty.Lookup('a'); ok || got != nil {
		t.Fatalf("zero layout Lookup returned %v, %v", got, ok)
	}
}

func TestInvalidLayout(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want error
	}{
		{"empty", "", ErrLayout},
		{"invalid UTF-8", "\xff", ErrLayout},
		{"syntax", "overrides: [", ErrLayout},
		{"root scalar", "word", ErrLayout},
		{"missing overrides", "{}", ErrLayout},
		{"null overrides", "overrides: null", ErrLayout},
		{"sequence overrides", "overrides: []", ErrLayout},
		{"unknown field", "overrides: {}\nother: true", ErrLayout},
		{"duplicate root field", "overrides: {}\noverrides: {}", ErrLayout},
		{"root merge", "<<: {overrides: {}}", ErrLayout},
		{"root merge alias", "overrides: &base {}\n<<: {overrides: *base}", ErrLayout},
		{"complex root key", "{[overrides]: {}}", ErrLayout},
		{"tagged root key", "!!binary b3ZlcnJpZGVz: {}", ErrLayout},
		{"second document", "overrides: {}\n---\noverrides: {}", ErrLayout},
		{"invalid second document", "overrides: {}\n---\n[", ErrLayout},
		{"duplicate character", "overrides: {'a': [none, 4], 'a': [none, 5]}", ErrLayout},
		{"empty character", "overrides: {'': [none, 4]}", ErrCharacter},
		{"long character", "overrides: {'ab': [none, 4]}", ErrCharacter},
		{"combining sequence", "overrides: {'e\u0301': [none, 4]}", ErrCharacter},
		{"numeric key", "overrides: {1: [none, 4]}", ErrCharacter},
		{"complex key", "overrides: {[a]: [none, 4]}", ErrCharacter},
		{"merge key", "overrides: {<<: {a: [none, 4]}}", ErrCharacter},
		{"scalar stroke", "overrides: {a: word}", ErrLayout},
		{"null stroke", "overrides: {a: null}", ErrLayout},
		{"empty sequence", "overrides: {a: []}", ErrLayout},
		{"empty stroke", "overrides: {a: [[]]}", ErrLayout},
		{"short stroke", "overrides: {a: [none]}", ErrLayout},
		{"long stroke", "overrides: {a: [none, 4, 5]}", ErrLayout},
		{"mixed sequence", "overrides: {a: [[none, 4], 5]}", ErrLayout},
		{"mapping stroke", "overrides: {a: {modifier: none, keycode: 4}}", ErrLayout},
		{"nested mapping", "overrides: {a: [{modifier: none, keycode: 4}]}", ErrLayout},
		{"numeric modifier", "overrides: {a: [1, 4]}", ErrLayout},
		{"empty modifier", "overrides: {a: ['', 4]}", ErrModifier},
		{"unknown modifier", "overrides: {a: [hyper, 4]}", ErrModifier},
		{"none combined", "overrides: {a: [none+shift, 4]}", ErrModifier},
		{"empty combined modifier", "overrides: {a: [ctrl++shift, 4]}", ErrModifier},
		{"negative keycode", "overrides: {a: [none, -1]}", ErrKeycode},
		{"large keycode", "overrides: {a: [none, 256]}", ErrKeycode},
		{"huge keycode", "overrides: {a: [none, 999999999999999999999999999999]}", ErrKeycode},
		{"invalid tagged integer", "overrides: {a: [none, !!int invalid]}", ErrKeycode},
		{"float keycode", "overrides: {a: [none, 4.0]}", ErrKeycode},
		{"bool keycode", "overrides: {a: [none, true]}", ErrKeycode},
		{"null keycode", "overrides: {a: [none, null]}", ErrKeycode},
		{"sequence keycode", "overrides: {a: [none, [4]]}", ErrKeycode},
		{"decimal string", "overrides: {a: [none, '4']}", ErrKeycode},
		{"invalid hex", "overrides: {a: [none, '0xzz']}", ErrKeycode},
		{"large hex", "overrides: {a: [none, '0x100']}", ErrKeycode},
		{"hex underscore", "overrides: {a: [none, '0x_1']}", ErrKeycode},
		{"alias stroke", "overrides: {a: &stroke [none, 4], b: *stroke}", ErrLayout},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			layout, err := decodeLayout([]byte(test.yaml))
			if layout != nil || !errors.Is(err, test.want) || !errors.Is(err, ErrLayout) {
				t.Fatalf("decodeLayout = %v, %v; want nil, %v", layout, err, test.want)
			}
		})
	}
}

func TestImmutableConcurrentLayouts(t *testing.T) {
	layout := mustLoad(t, "se105", "")
	want := []Stroke{{Modifier: 0x40, Keycode: 0x30}, {Keycode: 0x2c}}
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 20 {
				strokes, ok := layout.Lookup('~')
				if !ok || !reflect.DeepEqual(strokes, want) {
					t.Errorf("shared layout Lookup('~') = %v, %v; want %v, true", strokes, ok, want)
					return
				}
				clear(strokes)
				fresh, err := LoadLayout("se105", "")
				if err != nil {
					t.Errorf("concurrent LoadLayout failed: %v", err)
					return
				}
				strokes, ok = fresh.Lookup('~')
				if !ok || !reflect.DeepEqual(strokes, want) {
					t.Errorf("fresh layout Lookup('~') = %v, %v; want %v, true", strokes, ok, want)
					return
				}
			}
		})
	}
	workers.Wait()
	assertLookup(t, layout, '~', want)
}

func TestKeycodeAliases(t *testing.T) {
	var golden struct {
		Modifiers map[string]byte `json:"modifier_aliases"`
		Special   map[string]byte `json:"special_key_aliases"`
		Hex       []struct {
			Input   string `json:"input"`
			Keycode *byte  `json:"keycode"`
		} `json:"special_lookup"`
	}
	readJSON(t, "python_keycodes.json", &golden)
	if len(modifiers) != len(golden.Modifiers) || len(specialKeys) != len(golden.Special) {
		t.Fatalf("Go/Python alias counts differ: modifiers %d/%d, special keys %d/%d",
			len(modifiers), len(golden.Modifiers), len(specialKeys), len(golden.Special))
	}
	hexInputs := make([]string, len(golden.Hex))
	for i, vector := range golden.Hex {
		hexInputs[i] = vector.Input
	}
	slices.Sort(hexInputs)
	if want := []string{"0X2c", "0x", "0x100", "0x87"}; !slices.Equal(hexInputs, want) {
		t.Fatalf("Python hex inputs = %v; want %v", hexInputs, want)
	}
	for name, want := range golden.Modifiers {
		if got, ok := Modifier(strings.ToUpper(name)); !ok || got != want {
			t.Errorf("Modifier(%q) = %x, %v; want %x", name, got, ok, want)
		}
	}
	for name, want := range golden.Special {
		if got, ok := SpecialKey(strings.ToUpper(name)); !ok || got != want {
			t.Errorf("SpecialKey(%q) = %x, %v; want %x", name, got, ok, want)
		}
	}
	for _, vector := range golden.Hex {
		got, ok := SpecialKey(vector.Input)
		if vector.Keycode == nil {
			if ok {
				t.Errorf("SpecialKey(%q) = %x, true; Python rejected it", vector.Input, got)
			}
		} else if !ok || got != *vector.Keycode {
			t.Errorf("SpecialKey(%q) = %x, %v; Python returned %x", vector.Input, got, ok, *vector.Keycode)
		}
	}
	if _, ok := Modifier("unknown"); ok {
		t.Fatal("unknown modifier accepted")
	}
	for input, want := range map[string]byte{"0x00": 0, "0xFF": 0xff, "0x0004": 4} {
		if got, ok := SpecialKey(input); !ok || got != want {
			t.Errorf("SpecialKey(%q) = %x, %v; want %x", input, got, ok, want)
		}
	}
	for _, input := range []string{"", "a", "unknown", "0xZZ", "0x+1", "0x-1", "0x_1", "0x00_87", "0x 1", " 0x87", "0x87 "} {
		if _, ok := SpecialKey(input); ok {
			t.Errorf("SpecialKey(%q) accepted invalid input", input)
		}
	}
}
