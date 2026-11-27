package avfoundation

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/linkdata/autopaws/v4l2"
)

func TestMalformedSelectorsSkipDiscovery(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, test := range []struct {
		name string
		open func(string) error
	}{
		{"video", func(path string) error {
			_, err := OpenVideo(context.Background(), path, v4l2.Options{Width: 128, Height: 96})
			return err
		}},
		{"audio", func(path string) error {
			_, err := OpenAudio(context.Background(), path)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, path := range []string{"", "absent-device", "avfoundation:"} {
				err := test.open(path)
				var commandErr *exec.Error
				if !errors.Is(err, ErrDevice) || errors.As(err, &commandErr) {
					t.Errorf("malformed selector %q reached discovery: %v", path, err)
				}
			}
			err := test.open("avfoundation:Capture")
			var commandErr *exec.Error
			if !errors.As(err, &commandErr) {
				t.Fatalf("valid selector did not reach discovery: %v", err)
			}
		})
	}
}
