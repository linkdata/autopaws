// Package directshow uses the shared FFmpeg media pipeline with Windows capture inputs.
package directshow

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/linkdata/autopaws/internal/ffmpegmedia"
)

var (
	ErrDevice = errors.New("directshow: device missing or ambiguous")
	ErrClosed = ffmpegmedia.ErrClosed
)

// Device's Path is a copyable dshow:<alternative-name> selector. Friendly names
// are labels, and may also be selected when they identify exactly one input.
type Device struct {
	Path, Name string
	Audio      bool
}

var (
	logPrefix       = regexp.MustCompile(`^\[[^]]*\]\s*`)
	typedDevice     = regexp.MustCompile(`^"(.*)" \((video|audio|none|video, audio|audio, video)\)$`)
	legacyDevice    = regexp.MustCompile(`^"(.*)"$`)
	alternativeName = regexp.MustCompile(`^Alternative name "(.*)"$`)
)

// Discover lists DirectShow inputs without starting capture. FFmpeg versions
// may exit either zero or nonzero after a successful device listing.
func Discover(ctx context.Context) ([]Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-f", "dshow", "-list_devices", "true", "-i", "dummy")
	ffmpegmedia.ConfigureCommand(cmd)
	cmd.WaitDelay = time.Second
	var output ffmpegmedia.Diagnostics
	cmd.Stderr = &output
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	devices, parsed := parseDevices(output.String())
	if parsed && !output.Truncated() {
		return devices, nil
	}
	return nil, fmt.Errorf("directshow discovery: %w: %s", errors.Join(ErrDevice, err), output.String())
}

func parseDevices(output string) ([]Device, bool) {
	devices := make([]Device, 0)
	var pending []Device
	var awaitingAlternative, recognized, sectionKnown, sectionAudio bool
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 1024), 64<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(logPrefix.ReplaceAllString(scanner.Text(), ""))
		switch {
		case strings.HasPrefix(line, "DirectShow video devices"):
			if awaitingAlternative {
				return nil, false
			}
			sectionKnown, sectionAudio, recognized = true, false, true
			continue
		case strings.HasPrefix(line, "DirectShow audio devices"), strings.HasPrefix(line, "DirectShow audio only devices"):
			if awaitingAlternative {
				return nil, false
			}
			sectionKnown, sectionAudio, recognized = true, true, true
			continue
		case strings.HasPrefix(line, "Could not enumerate video devices"), strings.HasPrefix(line, "Could not enumerate audio only devices"):
			if awaitingAlternative {
				return nil, false
			}
			recognized = true
			continue
		}
		if match := typedDevice.FindStringSubmatch(line); match != nil {
			if awaitingAlternative {
				return nil, false
			}
			recognized = true
			pending = nil
			for _, media := range strings.Split(match[2], ", ") {
				if media != "none" {
					pending = append(pending, Device{Name: match[1], Audio: media == "audio"})
				}
			}
			awaitingAlternative = len(pending) != 0
			continue
		}
		if match := alternativeName.FindStringSubmatch(line); match != nil {
			if awaitingAlternative {
				if match[1] == "" {
					return nil, false
				}
				for _, device := range pending {
					device.Path = "dshow:" + match[1]
					devices = append(devices, device)
				}
				awaitingAlternative = false
			}
			continue
		}
		if match := legacyDevice.FindStringSubmatch(line); sectionKnown && match != nil {
			if awaitingAlternative {
				return nil, false
			}
			pending = []Device{{Name: match[1], Audio: sectionAudio}}
			awaitingAlternative = true
		}
	}
	return devices, recognized && !awaitingAlternative && scanner.Err() == nil
}

func validateSelector(path string) error {
	if !strings.HasPrefix(path, "dshow:") || len(path) == len("dshow:") {
		return fmt.Errorf("%w: expected dshow: selector", ErrDevice)
	}
	return nil
}

func selectDevice(devices []Device, path string, audio bool) (Device, error) {
	if err := validateSelector(path); err != nil {
		return Device{}, err
	}
	var selected Device
	count := 0
	for _, device := range devices {
		if device.Audio == audio && device.Path == path {
			selected, count = device, count+1
		}
	}
	if count == 0 {
		for _, device := range devices {
			if device.Audio == audio && device.Name == strings.TrimPrefix(path, "dshow:") {
				selected, count = device, count+1
			}
		}
	}
	if count != 1 {
		return Device{}, fmt.Errorf("%w: %q (%d matches)", ErrDevice, path, count)
	}
	if name := strings.TrimPrefix(selected.Path, "dshow:"); name == "" || strings.ContainsAny(name, ":\x00") {
		return Device{}, fmt.Errorf("%w: FFmpeg cannot select identifier %q", ErrDevice, selected.Path)
	}
	return selected, nil
}

func inputArguments(device Device) []string {
	kind := "video"
	if device.Audio {
		kind = "audio"
	}
	// DirectShow uses strtok, not FFmpeg's generic quoted-token parser.
	return []string{"-i", kind + "=" + strings.TrimPrefix(device.Path, "dshow:")}
}
