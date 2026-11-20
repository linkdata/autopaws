//go:build darwin && (amd64 || arm64)

package web

import (
	"context"
	"testing"
	"time"
)

func TestAudioRetryDefaultsOnlyForNativeExplicitMacDevice(t *testing.T) {
	for _, test := range []struct {
		name    string
		options AudioOptions
		want    time.Duration
	}{
		{name: "explicit", options: AudioOptions{Device: "avfoundation:fixture"}, want: time.Second},
		{name: "automatic", options: AudioOptions{}},
		{name: "off", options: AudioOptions{Device: "off"}},
		{name: "custom", options: AudioOptions{
			Device:     "avfoundation:fixture",
			OpenSource: func(context.Context, string) (AudioSource, error) { return nil, nil },
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := audioDefaults(test.options).retryInterval; got != test.want {
				t.Fatalf("retry interval = %v, want %v", got, test.want)
			}
		})
	}
}
