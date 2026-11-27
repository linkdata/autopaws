package avfoundation

import (
	"context"
	"fmt"

	"github.com/linkdata/autopaws/internal/ffmpegmedia"
	"github.com/linkdata/autopaws/v4l2"
)

type Video = ffmpegmedia.Video

// OpenVideo resolves an exact device name and waits for the first encoded
// frame. ctx governs startup; the returned stream lives until Close.
func OpenVideo(ctx context.Context, path string, options v4l2.Options) (*Video, error) {
	if options.Width == 0 || options.Height == 0 {
		return nil, v4l2.ErrOptions
	}
	if err := validateSelector(path); err != nil {
		return nil, err
	}
	devices, err := Discover(ctx)
	if err != nil {
		return nil, err
	}
	device, err := selectDevice(devices, path, false)
	if err != nil {
		return nil, err
	}
	args := []string{
		"-hide_banner", "-loglevel", "error", "-f", "avfoundation",
		"-framerate", "30", "-video_size", fmt.Sprintf("%dx%d", options.Width, options.Height),
	}
	args = append(args, inputArguments(device)...)
	return ffmpegmedia.OpenVideo(ctx, args)
}
