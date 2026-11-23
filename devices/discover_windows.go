//go:build windows && (amd64 || arm64)

package devices

import (
	"context"

	"github.com/linkdata/autopaws/directshow"
	"github.com/linkdata/autopaws/serial"
)

// Options is reserved for platform-specific discovery settings.
type Options struct{}

// Discover lists COM ports and DirectShow inputs without starting streams.
func Discover(ctx context.Context, _ Options) (Inventory, error) {
	ports, err := serial.DiscoverDevices(ctx)
	if err != nil {
		return Inventory{}, err
	}
	result := Inventory{}
	for _, port := range ports {
		result.Serial = append(result.Serial, SerialDevice{Path: port.Path, USB: port.USB})
	}
	media, err := directshow.Discover(ctx)
	if err != nil {
		return Inventory{}, err
	}
	for _, device := range media {
		if device.Audio {
			result.Audio = append(result.Audio, AudioDevice{Path: device.Path, CardID: device.Name})
		} else {
			result.Video = append(result.Video, VideoDevice{Path: device.Path, Name: device.Name})
		}
	}
	return result, nil
}

func DiscoverVideo(ctx context.Context, _ Options) ([]VideoDevice, error) {
	media, err := directshow.Discover(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]VideoDevice, 0)
	for _, device := range media {
		if !device.Audio {
			result = append(result, VideoDevice{Path: device.Path, Name: device.Name})
		}
	}
	return result, nil
}
