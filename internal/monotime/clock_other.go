//go:build !linux && !darwin

package monotime

import "errors"

func Now() (int64, error) {
	return 0, errors.New("CLOCK_MONOTONIC unavailable on this platform")
}
