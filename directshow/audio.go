package directshow

import (
	"context"

	"github.com/linkdata/autopaws/internal/ffmpegmedia"
)

type (
	Audio             = ffmpegmedia.Audio
	AudioSubscription = ffmpegmedia.AudioSubscription
)

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
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "dshow", "-audio_buffer_size", "20"}
	return ffmpegmedia.OpenAudio(ctx, append(args, inputArguments(device)...))
}
