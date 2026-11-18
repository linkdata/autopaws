package hid

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/linkdata/autopaws/ch9329"
)

func TestKeyboardSetLayout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, keyboard, transport := keyboardFixture(t, "us104")
		for _, target := range []struct {
			name     string
			modifier byte
		}{
			{"se105", 0x40},
			{"us104", 0x02},
		} {
			prepares, writes, _, _ := transport.snapshot()
			keyboard.SetLayout(mustLoad(t, target.name, ""))
			if p, w, _, _ := transport.snapshot(); p != prepares || w != writes {
				t.Fatal("selecting a layout touched the transport")
			}
			for _, operation := range []func() error{
				func() error { return keyboard.TypeText(context.Background(), "@", TypeOptions{}) },
				func() error { return keyboard.TypeText(context.Background(), "{ctrl+@}", TypeOptions{}) },
				func() error { return keyboard.SendKey(context.Background(), "@", nil) },
				func() error {
					return keyboard.SendKeySequence(context.Background(), []KeyStep{{Key: "@"}}, nil)
				},
				func() error { return keyboard.SendKey(context.Background(), "enter", nil) },
				func() error { return keyboard.SendKey(context.Background(), "0x1f", nil) },
			} {
				if err := operation(); err != nil {
					t.Fatal(err)
				}
			}
			_, _, _, events := transport.snapshot()
			want := [][]byte{
				ch9329.Keyboard(target.modifier, 0x1f),
				ch9329.Keyboard(target.modifier|0x01, 0x1f),
				ch9329.Keyboard(target.modifier, 0x1f),
				ch9329.Keyboard(target.modifier, 0x1f),
				ch9329.Keyboard(0, 0x28),
				ch9329.Keyboard(0, 0x1f),
			}
			for i, frame := range want {
				if !bytes.Equal(events[writes+2*i].Frame, frame) || !bytes.Equal(events[writes+2*i+1].Frame, ch9329.Keyboard(0)) {
					t.Fatalf("%s operation %d did not use its selected map and release: %+v", target.name, i, events[writes:])
				}
			}
		}
	})
}

func TestListLayouts(t *testing.T) {
	want := []LayoutInfo{
		{Name: "de105", Source: "builtin"},
		{Name: "fr105", Source: "builtin"},
		{Name: "jp106", Source: "builtin"},
		{Name: "se105", Source: "builtin"},
		{Name: "uk105", Source: "builtin"},
		{Name: "us104", Source: "builtin"},
	}
	for _, directory := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		got, err := ListLayouts(directory)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("ListLayouts(%q) = %+v, %v; want %+v", directory, got, err, want)
		}
	}
	directory := t.TempDir()
	for name, data := range map[string]string{
		"custom.yaml": "overrides: {}",
		"se105.yaml":  "overrides: [invalid]",
		"ignore.yml":  "overrides: {}",
		".yaml":       "overrides: {}",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(directory, "missing"), filepath.Join(directory, "broken.yaml")); err != nil {
		t.Fatal(err)
	}
	got, err := ListLayouts(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 || got[0].Name != "broken" || got[0].Source != "custom" || got[0].Error == "" ||
		got[1] != (LayoutInfo{Name: "custom", Source: "custom"}) ||
		got[5].Name != "se105" || got[5].Source != "custom" || got[5].Error == "" {
		t.Fatalf("custom precedence, failures or sorted names lost: %+v", got)
	}
	notDirectory := filepath.Join(directory, "custom.yaml")
	if result, err := ListLayouts(notDirectory); result != nil || !errors.Is(err, ErrLayout) {
		t.Fatalf("invalid directory returned %v, %v", result, err)
	}
}
