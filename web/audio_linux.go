//go:build linux && (amd64 || arm64)

package web

import (
	"context"
	"path/filepath"

	"github.com/linkdata/autopaws/alsa"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/devices"
	"github.com/linkdata/autopaws/serial"
)

type alsaSource struct{ *alsa.Capture }

func (source alsaSource) Subscribe() AudioSubscription { return source.Capture.Subscribe() }

func audioDefaults(options AudioOptions) AudioOptions {
	if options.OpenSource == nil {
		options.OpenSource = func(ctx context.Context, path string) (AudioSource, error) {
			source, err := alsa.Open(ctx, path)
			if err != nil {
				return nil, err
			}
			return alsaSource{source}, nil
		}
	}
	if options.ResolveAudio == nil {
		root := options.SysRoot
		options.ResolveAudio = func(ctx context.Context, video string) (string, error) {
			return resolveAudio(ctx, video, root)
		}
	}
	return options
}

// Selected /dev/v4l/by-id aliases resolve to the current videoN node before its
// sysfs USB ancestry is matched. Card labels and VID:PID never pair devices.
func resolveAudio(ctx context.Context, video, sysRoot string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if video == "" {
		return "", capture.ErrUnavailable
	}
	path, err := filepath.EvalSymlinks(video)
	if err != nil {
		return "", err
	}
	if sysRoot == "" {
		sysRoot = "/sys"
	}
	usb, err := serial.FindUSBDevice(sysRoot, filepath.Join(sysRoot, "class", "video4linux", filepath.Base(path)))
	if err != nil {
		return "", err
	}
	audios, err := devices.DiscoverAudio(ctx, sysRoot)
	if err != nil {
		return "", err
	}
	audio, found := devices.AudioForVideo(devices.VideoDevice{USB: usb}, audios)
	if !found {
		return "", capture.ErrUnavailable
	}
	return audio.Path, ctx.Err()
}
