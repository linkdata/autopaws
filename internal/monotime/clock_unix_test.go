//go:build linux || darwin

package monotime

import "testing"

func TestHostClock(t *testing.T) {
	first, err := Now()
	if err != nil || first <= 0 {
		t.Fatalf("CLOCK_MONOTONIC=%d,%v", first, err)
	}
	second, err := Now()
	if err != nil || second < first {
		t.Fatalf("CLOCK_MONOTONIC went backwards: %d,%d,%v", first, second, err)
	}
}
