//go:build linux && (amd64 || arm64)

package serial

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"testing/synctest"
)

func TestPrepareAutoDetectsMovedPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSysfsFixture(t)
		usb := f.usb("devices/usb1/1-1", "1a86", "7523")
		f.tty("ttyUSB0", usb)
		bad, good := &fakeDevice{}, &fakeDevice{}
		p, paths := fakePort(t, Options{SysRoot: f.root}, bad, good)
		if _, err := p.Prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		bad.write = func([]byte) (int, error) { return 0, syscall.ENODEV }
		if err := p.WriteFrame(context.Background(), []byte{1}); !errors.Is(err, syscall.ENODEV) {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(f.root, "class/tty/ttyUSB0")); err != nil {
			t.Fatal(err)
		}
		f.tty("ttyUSB7", usb)
		if generation, err := p.Prepare(context.Background()); generation != 2 || err != nil {
			t.Fatalf("recovery = %d, %v", generation, err)
		}
		if !slices.Equal(*paths, []string{"/dev/ttyUSB0", "/dev/ttyUSB7"}) {
			t.Fatalf("open paths = %v", *paths)
		}
	})
}

func TestPrepareSelectionAndOpenErrors(t *testing.T) {
	f := newSysfsFixture(t)
	p, paths := fakePort(t, Options{SysRoot: f.root})
	if _, err := p.Prepare(context.Background()); !errors.Is(err, ErrNotFound) || len(*paths) != 0 {
		t.Fatalf("no device = %v, opens %v", err, *paths)
	}
	q, paths := fakePort(t, Options{})
	if _, err := q.Prepare(context.Background()); !errors.Is(err, syscall.ENODEV) || len(*paths) != 1 {
		t.Fatalf("open failure = %v, opens %v", err, *paths)
	}
}
