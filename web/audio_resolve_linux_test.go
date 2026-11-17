//go:build linux && (amd64 || arm64)

package web

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/serial"
)

func TestAudioResolverMatchesCurrentVideoAliasAndIdenticalUSBPorts(t *testing.T) {
	root := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(name, target string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, target), path); err != nil {
			t.Fatal(err)
		}
	}
	for _, device := range []struct{ bus, video, card, pcm string }{
		{"1-1", "video0", "card0", "pcmC0D0c"}, {"1-2", "video1", "card1", "pcmC1D2c"},
	} {
		usb := "devices/usb1/" + device.bus
		write(usb+"/idVendor", "2b89\n")
		write(usb+"/idProduct", "5389\n")
		write(usb+"/video/"+device.video+"/name", "identical\n")
		write(usb+"/sound/"+device.card+"/id", "identical\n")
		if err := os.MkdirAll(filepath.Join(root, usb, "sound", device.card, device.pcm), 0o755); err != nil {
			t.Fatal(err)
		}
		link("class/video4linux/"+device.video, usb+"/video/"+device.video)
		link("class/sound/"+device.card, usb+"/sound/"+device.card)
		write("dev/"+device.video, "")
	}
	link("dev/v4l/by-id/selected", "dev/video1")
	path, err := resolveAudio(t.Context(), filepath.Join(root, "dev/v4l/by-id/selected"), root)
	if err != nil || path != "/dev/snd/pcmC1D2c" {
		t.Fatalf("selected alias crossed identical device USB ports: %q,%v", path, err)
	}
	path, err = resolveAudio(t.Context(), filepath.Join(root, "dev/video0"), root)
	if err != nil || path != "/dev/snd/pcmC0D0c" {
		t.Fatalf("explicit selected node lost its USB match: %q,%v", path, err)
	}
	if err := os.RemoveAll(filepath.Join(root, "class/sound")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAudio(t.Context(), filepath.Join(root, "dev/video0"), root); !errors.Is(err, capture.ErrUnavailable) {
		t.Fatal("missing matching PCM did not mark audio unavailable", err)
	}
	write("class/sound", "not a directory")
	if _, err := resolveAudio(t.Context(), filepath.Join(root, "dev/video0"), root); err == nil {
		t.Fatal("sysfs discovery failure was ignored")
	}
	if _, err := resolveAudio(t.Context(), "", root); !errors.Is(err, capture.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := resolveAudio(t.Context(), filepath.Join(root, "missing"), root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := resolveAudio(ctx, filepath.Join(root, "dev/video0"), root); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "devices/usb1/1-1/idVendor")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAudio(t.Context(), filepath.Join(root, "dev/video0"), root); !errors.Is(err, serial.ErrNoUSB) {
		t.Fatal("non-USB video matched an unrelated PCM", err)
	}
}
