package hid

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Layout-loading errors can be matched with errors.Is. ErrLayout matches every
// invalid layout; the remaining errors identify specific validation failures.
var (
	ErrLayout        = errors.New("invalid keyboard layout")
	ErrUnknownLayout = errors.New("unknown keyboard layout")
	ErrModifier      = errors.New("unknown keyboard modifier")
	ErrKeycode       = errors.New("invalid HID keycode")
	ErrCharacter     = errors.New("layout key must be one Unicode character")
)

// Stroke is one keyboard report's modifier bitmask and HID keycode.
type Stroke struct {
	Modifier byte
	Keycode  byte
}

// Layout is an immutable character-to-stroke map. A zero Layout is empty.
// Its built-in maps follow Linux/XKB; other target variants use custom files.
type Layout struct {
	characters map[rune][]Stroke
}

// Lookup returns a copy of the strokes for character. It is safe to call
// concurrently; changing the returned slice does not change the layout.
func (layout *Layout) Lookup(character rune) ([]Stroke, bool) {
	strokes, ok := layout.characters[character]
	return slices.Clone(strokes), ok
}

//go:embed layouts/*.yaml
var builtins embed.FS

// LoadLayout loads name.yaml from userDirectory when present, otherwise from
// the embedded layouts. Its overrides apply to a fresh US104 base, even when
// a user file replaces a built-in layout with the same name. A selected user
// file's read or parse error is returned without falling back to a built-in.
func LoadLayout(name, userDirectory string) (*Layout, error) {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("%w: %w %q", ErrLayout, ErrUnknownLayout, name)
	}
	if userDirectory != "" {
		path := filepath.Join(userDirectory, name+".yaml")
		_, err := os.Lstat(path)
		if err == nil {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("%w: read %s: %w", ErrLayout, path, err)
			}
			layout, err := decodeLayout(data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			return layout, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: inspect %s: %w", ErrLayout, path, err)
		}
	}
	data, err := builtins.ReadFile("layouts/" + name + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("%w: %w %q", ErrLayout, ErrUnknownLayout, name)
	}
	return decodeLayout(data)
}

func decodeLayout(data []byte) (*Layout, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLayout, err)
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode || len(root.Content) != 2 ||
		root.Content[0].Kind != yaml.ScalarNode || root.Content[0].Tag != "!!str" ||
		root.Content[0].Value != "overrides" || root.Content[1].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: expected only an overrides mapping", ErrLayout)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrLayout, err)
		}
		return nil, fmt.Errorf("%w: expected one YAML document", ErrLayout)
	}

	layout := &Layout{characters: us104()}
	seen := make(map[rune]bool)
	entries := root.Content[1].Content
	for i := 0; i < len(entries); i += 2 {
		key, value := entries[i], entries[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || utf8.RuneCountInString(key.Value) != 1 {
			return nil, fmt.Errorf("%w: line %d: %w", ErrLayout, key.Line, ErrCharacter)
		}
		character, _ := utf8.DecodeRuneInString(key.Value)
		if seen[character] {
			return nil, fmt.Errorf("%w: line %d: duplicate character %q", ErrLayout, key.Line, character)
		}
		seen[character] = true
		strokes, err := decodeStrokes(value)
		if err != nil {
			return nil, fmt.Errorf("%w: line %d for %q: %w", ErrLayout, value.Line, character, err)
		}
		layout.characters[character] = strokes
	}
	return layout, nil
}

func decodeStrokes(node *yaml.Node) ([]Stroke, error) {
	if node.Kind != yaml.SequenceNode || len(node.Content) == 0 {
		return nil, errors.New("expected a stroke or nonempty stroke sequence")
	}
	pairs := node.Content
	if pairs[0].Kind == yaml.ScalarNode {
		pairs = []*yaml.Node{node}
	}
	strokes := make([]Stroke, len(pairs))
	for i, pair := range pairs {
		if pair.Kind != yaml.SequenceNode || len(pair.Content) != 2 ||
			pair.Content[0].Kind != yaml.ScalarNode || pair.Content[0].Tag != "!!str" {
			return nil, errors.New("expected [modifier, keycode]")
		}
		modifier, err := decodeModifier(pair.Content[0].Value)
		if err != nil {
			return nil, err
		}
		keycode, err := decodeKeycode(pair.Content[1])
		if err != nil {
			return nil, err
		}
		strokes[i] = Stroke{Modifier: modifier, Keycode: keycode}
	}
	return strokes, nil
}

func decodeModifier(text string) (byte, error) {
	if strings.EqualFold(strings.TrimSpace(text), "none") {
		return 0, nil
	}
	var bits byte
	for name := range strings.SplitSeq(text, "+") {
		bit, ok := Modifier(strings.TrimSpace(name))
		if !ok {
			return 0, fmt.Errorf("%w: %q", ErrModifier, name)
		}
		bits |= bit
	}
	return bits, nil
}

func decodeKeycode(node *yaml.Node) (byte, error) {
	if node.Kind == yaml.ScalarNode {
		switch node.Tag {
		case "!!int":
			var value byte
			if err := node.Decode(&value); err == nil {
				return value, nil
			}
		case "!!str":
			if value, ok := hexKeycode(node.Value); ok {
				return value, nil
			}
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrKeycode, node.Value)
}

func us104() map[rune][]Stroke {
	characters := make(map[rune][]Stroke, 97)
	for i, lower := range "abcdefghijklmnopqrstuvwxyz" {
		characters[lower] = []Stroke{{Keycode: byte(0x04 + i)}}
		characters[lower-'a'+'A'] = []Stroke{{Modifier: 0x02, Keycode: byte(0x04 + i)}}
	}
	for i, digit := range "1234567890" {
		characters[digit] = []Stroke{{Keycode: byte(0x1e + i)}}
	}
	for i, symbol := range "!@#$%^&*()" {
		characters[symbol] = []Stroke{{Modifier: 0x02, Keycode: byte(0x1e + i)}}
	}
	for _, pair := range []struct {
		keycode byte
		plain   rune
		shifted rune
	}{
		{0x2d, '-', '_'},
		{0x2e, '=', '+'},
		{0x2f, '[', '{'},
		{0x30, ']', '}'},
		{0x31, '\\', '|'},
		{0x33, ';', ':'},
		{0x34, '\'', '"'},
		{0x35, '`', '~'},
		{0x36, ',', '<'},
		{0x37, '.', '>'},
		{0x38, '/', '?'},
	} {
		characters[pair.plain] = []Stroke{{Keycode: pair.keycode}}
		characters[pair.shifted] = []Stroke{{Modifier: 0x02, Keycode: pair.keycode}}
	}
	characters[' '] = []Stroke{{Keycode: 0x2c}}
	characters['\t'] = []Stroke{{Keycode: 0x2b}}
	characters['\n'] = []Stroke{{Keycode: 0x28}}
	return characters
}
