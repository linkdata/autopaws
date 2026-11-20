//go:build darwin && (amd64 || arm64)

package web

import (
	"context"
	"fmt"
	"time"

	"github.com/linkdata/autopaws/avfoundation"
)

type avfoundationSource struct{ *avfoundation.Audio }

func (source avfoundationSource) Subscribe() AudioSubscription { return source.Audio.Subscribe() }

func audioDefaults(options AudioOptions) AudioOptions {
	if options.OpenSource == nil {
		if options.Device != "" && options.Device != "off" {
			options.retryInterval = time.Second
		}
		options.OpenSource = func(ctx context.Context, path string) (AudioSource, error) {
			source, err := avfoundation.OpenAudio(ctx, path)
			if err != nil {
				return nil, err
			}
			return avfoundationSource{source}, nil
		}
	}
	if options.ResolveAudio == nil {
		// AVFoundation does not expose enough USB ancestry to safely pair inputs.
		options.ResolveAudio = func(context.Context, string) (string, error) {
			return "", fmt.Errorf("macOS audio requires an explicit -audio-device avfoundation:NAME selection")
		}
	}
	return options
}
