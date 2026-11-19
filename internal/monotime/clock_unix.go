//go:build linux || darwin

// Package monotime reads the host monotonic clock shared by media sources.
package monotime

import (
	"errors"
	"math"

	"golang.org/x/sys/unix"
)

// Now returns nanoseconds on CLOCK_MONOTONIC, independently of process startup.
func Now() (int64, error) {
	var timestamp unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &timestamp); err != nil {
		return 0, err
	}
	if timestamp.Sec < 0 || timestamp.Nsec < 0 || timestamp.Nsec >= 1e9 || timestamp.Sec > (math.MaxInt64-timestamp.Nsec)/1e9 {
		return 0, errors.New("invalid CLOCK_MONOTONIC timestamp")
	}
	return timestamp.Nano(), nil
}
