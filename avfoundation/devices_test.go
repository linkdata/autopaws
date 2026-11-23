//go:build darwin && (amd64 || arm64)

package avfoundation

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseDevices(t *testing.T) {
	output := `[AVFoundation indev @ 0x1] AVFoundation video devices:
[AVFoundation indev @ 0x1] [0] FaceTime HD Camera
[AVFoundation indev @ 0x1] [1] USB Capture
[AVFoundation indev @ 0x1] [2] Capture screen 0
[AVFoundation indev @ 0x1] AVFoundation audio devices:
[AVFoundation indev @ 0x1] [0] USB Audio
[in#0 @ 0x2] Error opening input: Immediate exit requested
`
	devices, ok := parseDevices(output)
	want := []Device{
		{Path: "avfoundation:FaceTime HD Camera", Name: "FaceTime HD Camera"},
		{Path: "avfoundation:USB Capture", Name: "USB Capture"},
		{Path: "avfoundation:USB Audio", Name: "USB Audio", Audio: true},
	}
	if !ok || !reflect.DeepEqual(devices, want) {
		t.Fatalf("parse = %#v, %v", devices, ok)
	}
	device, err := selectDevice(devices, want[1].Path, false)
	if err != nil || device != want[1] {
		t.Fatalf("select = %+v, %v", device, err)
	}
	if _, err := selectDevice(append(devices, want[1]), want[1].Path, false); !errors.Is(err, ErrDevice) {
		t.Fatalf("ambiguous = %v", err)
	}
	if _, err := selectDevice(devices, want[1].Path, true); !errors.Is(err, ErrDevice) {
		t.Fatalf("wrong media = %v", err)
	}
	if _, ok := parseDevices("camera access denied"); ok {
		t.Fatal("accepted failed listing")
	}
	if got := inputArguments(want[1]); !reflect.DeepEqual(got, []string{"-i", "USB Capture:none"}) {
		t.Fatalf("video input args = %v", got)
	}
	if got := inputArguments(want[0]); !reflect.DeepEqual(got, []string{"-i", "FaceTime HD Camera:none"}) {
		t.Fatalf("camera input args = %v", got)
	}
}

func TestRejectUnsafeNames(t *testing.T) {
	for _, name := range []string{"0 Camera", "123", "+2", "none", "default Input", "USB:Capture", ""} {
		device := Device{Path: "avfoundation:" + name, Name: name}
		if _, err := selectDevice([]Device{device}, device.Path, false); !errors.Is(err, ErrDevice) {
			t.Fatalf("accepted ambiguous FFmpeg name %q: %v", name, err)
		}
	}
	devices := []Device{{Path: "avfoundation:USB", Name: "USB"}, {Path: "avfoundation:USB Capture", Name: "USB Capture"}}
	if _, err := selectDevice(devices, devices[0].Path, false); !errors.Is(err, ErrDevice) {
		t.Fatalf("accepted ambiguous prefix: %v", err)
	}
}
