//go:build (linux || darwin) && (amd64 || arm64)

// Package devices discovers KVM serial and capture devices.
package devices

import (
	"errors"
	"strings"

	"github.com/linkdata/autopaws/serial"
	"github.com/linkdata/autopaws/v4l2"
)

// ErrNoVideo means no node passed the supported capture-node filter.
var ErrNoVideo = errors.New("devices: no video capture device found")

// SerialDevice is one supported CH340/CH341 tty.
type SerialDevice struct {
	Path string
	USB  serial.USBDevice
}

// VideoDevice is a Linux VIDEO_CAPTURE node or a macOS AVFoundation input.
type VideoDevice struct {
	Path, Name string
	USB        serial.USBDevice
	Formats    []v4l2.PixelFormat
}

// AudioDevice is a capture input. CardID is the ALSA card ID on Linux and the
// AVFoundation input name on macOS. Card and Device are Linux PCM numbers.
type AudioDevice struct {
	Path, CardID string
	Card, Device int
	USB          serial.USBDevice
}

// Inventory follows platform discovery order (numeric nodes on Linux, lexical
// serial paths and AVFoundation enumeration on macOS). Empty USB identities,
// including macOS media inputs, never correlate video and audio.
type Inventory struct {
	Serial []SerialDevice
	Video  []VideoDevice
	Audio  []AudioDevice
}

// SelectVideo chooses the first non-webcam name in discovery order, or
// the first capture node when every name contains a webcam keyword.
func SelectVideo(videos []VideoDevice) (VideoDevice, error) {
	for _, video := range videos {
		name := strings.ToLower(video.Name)
		webcam := false
		for _, keyword := range []string{"webcam", "camera", "ir camera", "front", "rear", "facetime"} {
			if strings.Contains(name, keyword) {
				webcam = true
				break
			}
		}
		if !webcam {
			return video, nil
		}
	}
	if len(videos) == 0 {
		return VideoDevice{}, ErrNoVideo
	}
	return videos[0], nil
}

// AudioForVideo selects the lowest card/device capture PCM with the same
// nonempty USB ancestor as video. Identical VID:PID/card IDs do not cross-pair.
func AudioForVideo(video VideoDevice, audios []AudioDevice) (AudioDevice, bool) {
	var selected AudioDevice
	found := false
	for _, audio := range audios {
		if video.USB.BusPath == "" || audio.USB.BusPath != video.USB.BusPath {
			continue
		}
		if !found || audio.Card < selected.Card || audio.Card == selected.Card && audio.Device < selected.Device {
			selected, found = audio, true
		}
	}
	return selected, found
}
