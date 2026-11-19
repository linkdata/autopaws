//go:build darwin && (amd64 || arm64)

package devices

import (
	"context"

	"github.com/linkdata/autopaws/avfoundation"
	"github.com/linkdata/autopaws/serial"
)

// Options is reserved for platform-specific discovery settings.
type Options struct{}

// Discover lists serial and AVFoundation inputs without starting capture.
func Discover(ctx context.Context, _ Options) (Inventory, error) {
	ports, err := serial.DiscoverDevices(ctx)
	if err != nil {
		return Inventory{}, err
	}
	result := Inventory{}
	for _, p := range ports {
		result.Serial = append(result.Serial, SerialDevice{Path: p.Path, USB: p.USB})
	}
	media, err := avfoundation.Discover(ctx)
	if err != nil {
		return Inventory{}, err
	}
	for _, d := range media {
		if d.Audio {
			result.Audio = append(result.Audio, AudioDevice{Path: d.Path, CardID: d.Name})
		} else {
			result.Video = append(result.Video, VideoDevice{Path: d.Path, Name: d.Name})
		}
	}
	return result, nil
}

func DiscoverVideo(ctx context.Context, _ Options) ([]VideoDevice, error) {
	media, err := avfoundation.Discover(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]VideoDevice, 0)
	for _, d := range media {
		if !d.Audio {
			result = append(result, VideoDevice{Path: d.Path, Name: d.Name})
		}
	}
	return result, nil
}
