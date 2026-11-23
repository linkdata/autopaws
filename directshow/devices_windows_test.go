package directshow

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/linkdata/autopaws/v4l2"
)

func TestParseDevicesAndStableSelectors(t *testing.T) {
	output := `[in#0 @ 0x1] "Integrated Camera" (none)
[in#0 @ 0x1]   Alternative name "@device_pnp_ignored"
[in#0 @ 0x1] "Capture" (video)
[in#0 @ 0x1]   Alternative name "@device_pnp_\\?\usb#one"
[in#0 @ 0x1] "Capture" (video)
[in#0 @ 0x1]   Alternative name "@device_pnp_\\?\usb#two"
[in#0 @ 0x1] "HDMI Audio" (audio)
[in#0 @ 0x1]   Alternative name "@device_cm_audio"
Error opening input file dummy.
`
	devices, ok := parseDevices(output)
	want := []Device{
		{Path: `dshow:@device_pnp_\\?\usb#one`, Name: "Capture"},
		{Path: `dshow:@device_pnp_\\?\usb#two`, Name: "Capture"},
		{Path: "dshow:@device_cm_audio", Name: "HDMI Audio", Audio: true},
	}
	if !ok || !reflect.DeepEqual(devices, want) {
		t.Fatalf("parsed devices = %#v, %t; want %#v", devices, ok, want)
	}
	if selected, err := selectDevice(devices, want[1].Path, false); err != nil || selected != want[1] {
		t.Fatalf("canonical selection: %+v, %v", selected, err)
	}
	if selected, err := selectDevice(devices, "dshow:HDMI Audio", true); err != nil || selected != want[2] {
		t.Fatalf("unique friendly selection: %+v, %v", selected, err)
	}
	for _, test := range []struct {
		path  string
		audio bool
	}{
		{"dshow:Capture", false}, {"dshow:missing", false}, {want[0].Path, true}, {"Capture", false}, {"dshow:", false},
	} {
		if _, err := selectDevice(devices, test.path, test.audio); !errors.Is(err, ErrDevice) {
			t.Fatalf("accepted missing, ambiguous or wrong-media selector %q: %v", test.path, err)
		}
	}
}

func TestLegacyAndEmptyDeviceListings(t *testing.T) {
	legacy := `[dshow @ 0x1] DirectShow video devices
[dshow @ 0x1] "Capture"
[dshow @ 0x1] Alternative name "video-id"
[dshow @ 0x1] DirectShow audio devices
[dshow @ 0x1] "Audio"
[dshow @ 0x1] Alternative name "audio-id"`
	devices, ok := parseDevices(legacy)
	if !ok || len(devices) != 2 || devices[0].Audio || !devices[1].Audio {
		t.Fatalf("legacy discovery: %+v, %t", devices, ok)
	}
	devices, ok = parseDevices("[dshow @ x] Could not enumerate video devices (or none found).\n[dshow @ x] Could not enumerate audio only devices (or none found).")
	if !ok || len(devices) != 0 {
		t.Fatalf("empty discovery: %+v, %t", devices, ok)
	}
	for _, invalid := range []string{
		"Unknown input format: 'dshow'",
		`[dshow @ x] "Capture" (video)`,
		"[dshow @ x] \"Capture\" (video)\n[dshow @ x] Alternative name \"\"",
	} {
		if _, ok := parseDevices(invalid); ok {
			t.Fatalf("accepted failed/incomplete discovery: %q", invalid)
		}
	}
}

func TestDeviceWithVideoAndAudioPins(t *testing.T) {
	for _, media := range []string{"video, audio", "audio, video"} {
		devices, ok := parseDevices("[dshow @ x] \"Capture\" (" + media + ")\n[dshow @ x] Alternative name \"combined-id\"")
		if !ok || len(devices) != 2 {
			t.Fatalf("combined discovery: %+v, %t", devices, ok)
		}
		for _, audio := range []bool{false, true} {
			device, err := selectDevice(devices, "dshow:combined-id", audio)
			if err != nil || device.Audio != audio || device.Name != "Capture" {
				t.Fatalf("combined selection (%t): %+v, %v", audio, device, err)
			}
		}
	}
}

func TestInputArgumentsPreserveIdentifiers(t *testing.T) {
	device := Device{Path: `dshow:@device_pnp_\\?\usb#it's=capture`}
	want := []string{"-i", `video=@device_pnp_\\?\usb#it's=capture`}
	if got := inputArguments(device); !reflect.DeepEqual(got, want) {
		t.Fatalf("input arguments = %q; want %q", got, want)
	}
	device.Audio = true
	if got := inputArguments(device); got[1] != "audio="+want[1][len("video="):] {
		t.Fatalf("audio input arguments = %q", got)
	}
	if _, err := OpenVideo(context.Background(), device.Path, v4l2.Options{}); !errors.Is(err, v4l2.ErrOptions) {
		t.Fatalf("accepted zero video dimensions: %v", err)
	}
	for _, invalid := range []string{"dshow:has:separator", "dshow:has\x00nul", "dshow:"} {
		if _, err := selectDevice([]Device{{Path: invalid, Name: "Unsafe"}}, invalid, false); !errors.Is(err, ErrDevice) {
			t.Fatalf("accepted unrepresentable identifier %q: %v", invalid, err)
		}
	}
}
