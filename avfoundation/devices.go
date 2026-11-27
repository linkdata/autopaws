// Package avfoundation uses the FFmpeg command's AVFoundation input on macOS.
// FFmpeg must be installed separately. Discovery lists devices without opening
// streams; macOS controls camera and microphone access for the launching app.
package avfoundation

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
	ErrDevice = errors.New("avfoundation: device missing or ambiguous")
	ErrClosed = ffmpegmedia.ErrClosed
)

// Device describes one camera/capture input or microphone input. Paths select
// exact names and are resolved again when a stream opens, because indices may
// change when devices are attached. Duplicate names require disconnecting the
// other identically named device before selection.
type Device struct {
	Path, Name string
	Audio      bool
}

// FFmpeg 9.1 appends device UIDs and optional serial numbers after the name.
var deviceLine = regexp.MustCompile(`^\[[^]]*\]\s*\[[0-9]+\]\s*(.+?)(?:\s+\[uid:[^]]*\](?:\s+\[serial:[^]]*\])?)?$`)

// Discover enumerates FFmpeg's AVFoundation inputs, excluding host displays.
// FFmpeg deliberately exits nonzero after a successful device listing.
func Discover(ctx context.Context) ([]Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	ffmpegmedia.ConfigureCommand(cmd)
	cmd.WaitDelay = time.Second
	var output ffmpegmedia.Diagnostics
	cmd.Stderr = &output
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	devices, parsed := parseDevices(output.String())
	if parsed {
		return devices, nil
	}
	return nil, fmt.Errorf("avfoundation discovery: %w: %s", errors.Join(ErrDevice, err), output.String())
}

func parseDevices(output string) ([]Device, bool) {
	devices := make([]Device, 0)
	var section string
	var video, audio bool
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.Contains(line, "AVFoundation video devices:"):
			section, video = "video", true
			continue
		case strings.Contains(line, "AVFoundation audio devices:"):
			section, audio = "audio", true
			continue
		}
		match := deviceLine.FindStringSubmatch(line)
		if section == "" || match == nil {
			continue
		}
		name := strings.TrimSpace(match[1])
		if section == "video" && strings.HasPrefix(name, "Capture screen ") {
			continue
		}
		devices = append(devices, Device{Path: "avfoundation:" + name, Name: name, Audio: section == "audio"})
	}
	return devices, video && audio && scanner.Err() == nil
}

// FFmpeg matches names as prefixes; selectDevice rejects ambiguous selectors.
func inputArguments(device Device) []string {
	input := device.Name + ":none"
	if device.Audio {
		input = "none:" + device.Name
	}
	return []string{"-i", input}
}

func validateSelector(path string) error {
	if !strings.HasPrefix(path, "avfoundation:") || len(path) == len("avfoundation:") {
		return fmt.Errorf("%w: expected avfoundation: selector", ErrDevice)
	}
	return nil
}

func selectDevice(devices []Device, path string, audio bool) (Device, error) {
	var result Device
	count := 0
	for _, device := range devices {
		if device.Path == path && device.Audio == audio {
			result = device
			count++
		}
	}
	if count != 1 {
		return Device{}, fmt.Errorf("%w: %q (%d matches)", ErrDevice, path, count)
	}
	// FFmpeg parses ':' as an input separator, leading numbers as indices,
	// and these reserved prefixes as aliases. They cannot select literal
	// names safely.
	name := strings.TrimLeft(result.Name, "+-")
	if name == "" || name[0] >= '0' && name[0] <= '9' || strings.Contains(result.Name, ":") ||
		strings.HasPrefix(result.Name, "none") || strings.HasPrefix(result.Name, "default") {
		return Device{}, fmt.Errorf("%w: FFmpeg cannot select literal name %q", ErrDevice, result.Name)
	}
	for _, other := range devices {
		if other.Audio == result.Audio && other.Path != result.Path && strings.HasPrefix(other.Name, result.Name) {
			return Device{}, fmt.Errorf("%w: %q also matches %q", ErrDevice, result.Name, other.Name)
		}
	}
	return result, nil
}
