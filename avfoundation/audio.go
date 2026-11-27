package avfoundation

import (
	"context"

	"github.com/linkdata/autopaws/internal/ffmpegmedia"
)

type (
	Audio             = ffmpegmedia.Audio
	AudioSubscription = ffmpegmedia.AudioSubscription
)

// OpenAudio opens one explicitly selected avfoundation:<exact name> input.
// ctx controls startup; the returned source lives until Close.
func OpenAudio(ctx context.Context, path string) (*Audio, error) {
	if err := validateSelector(path); err != nil {
		return nil, err
	}
	devices, err := Discover(ctx)
	if err != nil {
		return nil, err
	}
	device, err := selectDevice(devices, path, true)
	if err != nil {
		return nil, err
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "avfoundation"}
	args = append(args, inputArguments(device)...)
	return ffmpegmedia.OpenAudio(ctx, args)
}
