//go:build linux || darwin

package hid

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestListLayoutsDanglingSymlink(t *testing.T) {
	directory := t.TempDir()
	if err := os.Symlink(filepath.Join(directory, "missing"), filepath.Join(directory, "broken.yaml")); err != nil {
		t.Fatal(err)
	}
	got, err := ListLayouts(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 || got[0].Name != "broken" || got[0].Source != "custom" || got[0].Error == "" {
		t.Fatalf("dangling layout symlink missing its listing error: %+v", got)
	}
}

func TestLayoutPrecedenceDanglingSymlink(t *testing.T) {
	directory := t.TempDir()
	if err := os.Symlink(filepath.Join(directory, "absent.yaml"), filepath.Join(directory, "uk105.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLayout("uk105", directory); !errors.Is(err, ErrLayout) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling user layout symlink fell back or lost the read error: %v", err)
	}
}
