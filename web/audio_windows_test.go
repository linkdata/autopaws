//go:build windows && (amd64 || arm64)

package web

import (
	"strings"
	"testing"
	"time"
)

func TestWindowsAudioDefaultsRequireExplicitSelection(t *testing.T) {
	options := audioDefaults(AudioOptions{Device: "dshow:audio-id"})
	if options.OpenSource == nil || options.ResolveAudio == nil || options.retryInterval != time.Second {
		t.Fatal("missing DirectShow audio defaults or recovery")
	}
	if _, err := options.ResolveAudio(t.Context(), "dshow:video-id"); err == nil || !strings.Contains(err.Error(), "explicit -audio-device") {
		t.Fatalf("implicit Windows audio selection: %v", err)
	}
	if options := audioDefaults(AudioOptions{Device: "off"}); options.retryInterval != 0 {
		t.Fatal("disabled audio configured retries")
	}
}
