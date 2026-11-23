//go:build windows && (amd64 || arm64)

package web

import (
	"context"
	"fmt"
	"time"

	"github.com/linkdata/autopaws/directshow"
)

type directshowSource struct{ *directshow.Audio }

func (source directshowSource) Subscribe() AudioSubscription { return source.Audio.Subscribe() }

func audioDefaults(options AudioOptions) AudioOptions {
	if options.OpenSource == nil {
		if options.Device != "" && options.Device != "off" {
			options.retryInterval = time.Second
		}
		options.OpenSource = func(ctx context.Context, path string) (AudioSource, error) {
			source, err := directshow.OpenAudio(ctx, path)
			if err != nil {
				return nil, err
			}
			return directshowSource{source}, nil
		}
	}
	if options.ResolveAudio == nil {
		options.ResolveAudio = func(context.Context, string) (string, error) {
			return "", fmt.Errorf("audio on Windows requires an explicit -audio-device dshow:SELECTOR from --list-devices")
		}
	}
	return options
}
