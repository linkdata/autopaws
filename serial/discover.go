//go:build (linux || darwin || windows) && (amd64 || arm64)

package serial

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNoUSB means that a device has no supported USB ancestor.
	ErrNoUSB = errors.New("serial: no USB device ancestor")
	// ErrNotFound means that discovery found no supported serial device.
	ErrNotFound = errors.New("serial: no CH340/CH341 serial device found")
	// ErrAmbiguous means that auto-detection found multiple supported devices.
	ErrAmbiguous = errors.New("serial: multiple CH340/CH341 serial devices found")
)

// USBDevice identifies a discovered USB device. VID and PID are lowercase
// hexadecimal strings. BusPath identifies the USB ancestor shared by its
// interfaces on Unix, or the serial Plug and Play instance on Windows.
// Identical devices on different ports have distinct paths.
type USBDevice struct {
	VID     string
	PID     string
	BusPath string
}

// Detect selects the only supported serial device, or returns ErrNotFound or
// ErrAmbiguous. On Linux, an empty sysRoot means /sys; other platforms ignore sysRoot.
func Detect(sysRoot string) (string, error) {
	return detect(context.Background(), sysRoot)
}

func detect(ctx context.Context, sysRoot string) (string, error) {
	paths, err := discover(ctx, sysRoot)
	if err != nil {
		return "", err
	}
	switch len(paths) {
	case 0:
		return "", ErrNotFound
	case 1:
		return paths[0], nil
	default:
		return "", fmt.Errorf("%w: %s", ErrAmbiguous, strings.Join(paths, ", "))
	}
}
