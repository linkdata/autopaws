package directshow

import (
	"context"
	"fmt"

	"github.com/linkdata/autopaws/internal/ffmpegmedia"
	"github.com/linkdata/autopaws/v4l2"
)

type Video = ffmpegmedia.Video

func OpenVideo(ctx context.Context, path string, options v4l2.Options) (*Video, error) {
	if options.Width == 0 || options.Height == 0 {
		return nil, v4l2.ErrOptions
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
		"-hide_banner", "-loglevel", "error", "-f", "dshow",
		"-video_size", fmt.Sprintf("%dx%d", options.Width, options.Height), "-framerate", "30", "-vcodec", "mjpeg",
	}
	return ffmpegmedia.OpenVideo(ctx, append(args, inputArguments(device)...))
}
