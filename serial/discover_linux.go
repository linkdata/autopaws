//go:build linux && (amd64 || arm64)

package serial

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// FindUSBDevice walks a sysfs device directory or symlink to its first USB
// ancestor. It never walks outside sysRoot/devices. An empty sysRoot means /sys.
// Missing paths return filesystem errors; devices without a USB ancestor return
// ErrNoUSB. The returned bus path is suitable for correlating interfaces during
// discovery, but is not a persistent device identifier across physical moves.
func FindUSBDevice(sysRoot, devicePath string) (USBDevice, error) {
	if sysRoot == "" {
		sysRoot = "/sys"
	}
	devices, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "devices"))
	if err != nil {
		return USBDevice{}, err
	}
	devices, err = filepath.Abs(devices)
	if err != nil {
		return USBDevice{}, err
	}
	device, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return USBDevice{}, err
	}
	device, err = filepath.Abs(device)
	if err != nil {
		return USBDevice{}, err
	}
	rel, err := filepath.Rel(devices, device)
	if err != nil {
		return USBDevice{}, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return USBDevice{}, fmt.Errorf("%s: %w", devicePath, ErrNoUSB)
	}
	for dir := device; ; dir = filepath.Dir(dir) {
		vid, vidErr := os.ReadFile(filepath.Join(dir, "idVendor"))
		if vidErr != nil && !errors.Is(vidErr, fs.ErrNotExist) {
			return USBDevice{}, vidErr
		}
		if vidErr == nil {
			pid, pidErr := os.ReadFile(filepath.Join(dir, "idProduct"))
			if pidErr != nil && !errors.Is(pidErr, fs.ErrNotExist) {
				return USBDevice{}, pidErr
			}
			if pidErr == nil {
				return USBDevice{
					VID:     strings.ToLower(strings.TrimSpace(string(vid))),
					PID:     strings.ToLower(strings.TrimSpace(string(pid))),
					BusPath: dir,
				}, nil
			}
		}
		if dir == devices {
			return USBDevice{}, fmt.Errorf("%s: %w", devicePath, ErrNoUSB)
		}
	}
}

// Discover returns supported CH340/CH341 /dev/ttyUSB paths in numeric order.
// sysRoot is /sys in production; an empty value selects that default. Missing or
// broken per-node device links are skipped, as are non-USB and unsupported ttys.
// Other filesystem errors are returned instead of appearing as no devices.
func Discover(sysRoot string) ([]string, error) {
	if sysRoot == "" {
		sysRoot = "/sys"
	}
	entries, err := os.ReadDir(filepath.Join(sysRoot, "class", "tty"))
	if err != nil {
		return nil, err
	}
	type port struct {
		path  string
		index int
	}
	var matches []port
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), "ttyUSB")
		if !ok || suffix == "" || strings.Trim(suffix, "0123456789") != "" {
			continue
		}
		index, err := strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		usb, err := FindUSBDevice(sysRoot, filepath.Join(sysRoot, "class", "tty", entry.Name(), "device"))
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrNoUSB) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if usb.VID == "1a86" && (usb.PID == "7523" || usb.PID == "7522") {
			matches = append(matches, port{path: "/dev/" + entry.Name(), index: index})
		}
	}
	slices.SortFunc(matches, func(a, b port) int {
		if order := cmp.Compare(a.index, b.index); order != 0 {
			return order
		}
		return strings.Compare(a.path, b.path)
	})
	paths := make([]string, len(matches))
	for i, match := range matches {
		paths[i] = match.path
	}
	return paths, nil
}

func discover(_ context.Context, sysRoot string) ([]string, error) {
	return Discover(sysRoot)
}
