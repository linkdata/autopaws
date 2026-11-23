package monotime

import (
	"math"
	"testing"
)

func TestWindowsCounterNanoseconds(t *testing.T) {
	for _, test := range []struct {
		counter, frequency, want int64
	}{
		{0, 10_000_000, 0},
		{25_000_001, 10_000_000, 2_500_000_100},
		{math.MaxInt64 - 1, math.MaxInt64, 999_999_999},
	} {
		got, err := counterNanoseconds(test.counter, test.frequency)
		if err != nil || got != test.want {
			t.Fatalf("counter %d / %d: %d, %v; want %d", test.counter, test.frequency, got, err, test.want)
		}
	}
	for _, values := range [][2]int64{{-1, 1}, {1, 0}, {math.MaxInt64, 1}} {
		if _, err := counterNanoseconds(values[0], values[1]); err == nil {
			t.Fatalf("accepted invalid counter conversion: %v", values)
		}
	}
}

func TestWindowsMonotonicClock(t *testing.T) {
	first, err := Now()
	if err != nil || first < 0 {
		t.Fatalf("first timestamp: %d, %v", first, err)
	}
	second, err := Now()
	if err != nil || second < first {
		t.Fatalf("clock failed to advance: %d -> %d, %v", first, second, err)
	}
}
