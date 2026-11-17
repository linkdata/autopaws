package main

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
)

func TestScalePixel(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, test := range []struct{ value, source, target, want int }{
		{0, 1920, 1920, 0},
		{1919, 1920, 1920, 1919},
		{2, 5, 1920, 960},
		{4, 5, 1920, 1919},
		{1, 3, 2, 1},
		{1, 4, 3, 1},
		{2, 4, 3, 1},
		{0, 1, 1920, 0},
		{999, 1000, 1, 0},
		{0, 1, 1, 0},
		{maxInt - 1, maxInt, maxInt, maxInt - 1},
		{maxInt - 2, maxInt, maxInt - 1, maxInt - 3},
	} {
		if got, err := scalePixel(test.value, test.source, test.target); err != nil || got != test.want {
			t.Errorf("scalePixel(%d,%d,%d)=%d,%v; want %d", test.value, test.source, test.target, got, err, test.want)
		}
	}
	for _, test := range [][3]int{{0, 0, 1}, {0, 1, 0}, {-1, 2, 2}, {2, 2, 2}, {1, 1, 2}, {0, -1, 1}} {
		if _, err := scalePixel(test[0], test[1], test[2]); err == nil {
			t.Errorf("invalid pixel/axis accepted: %v", test)
		}
	}
}

func FuzzScalePixel(f *testing.F) {
	for _, seed := range [][3]int{{0, 1, 1}, {2, 5, 1920}, {99, 100, 1}, {1<<62 - 1, 1<<62 + 1, 1<<62 + 5}} {
		f.Add(seed[0], seed[1], seed[2])
	}
	f.Fuzz(func(t *testing.T, value, source, target int) {
		got, err := scalePixel(value, source, target)
		if source <= 0 || target <= 0 || value < 0 || value >= source {
			if err == nil {
				t.Fatal("invalid arguments accepted")
			}
			return
		}
		if err != nil || got < 0 || got >= target {
			t.Fatalf("valid pixel mapped outside target: %d, %v", got, err)
		}
		if source == 1 || target == 1 {
			if got != 0 {
				t.Fatal("single-pixel axis did not map to zero")
			}
			return
		}
		// Independent arbitrary-precision oracle, including half-up rounding.
		numerator := new(big.Int).Mul(big.NewInt(int64(value)), big.NewInt(int64(target-1)))
		denominator := big.NewInt(int64(source - 1))
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(numerator, denominator, remainder)
		if remainder.Lsh(remainder, 1).Cmp(denominator) >= 0 {
			quotient.Add(quotient, big.NewInt(1))
		}
		if int64(got) != quotient.Int64() {
			t.Fatalf("mapping=%d; exact result=%s", got, quotient)
		}
	})
}

func TestScreenStateCommitAndInvalidation(t *testing.T) {
	state := &screenState{}
	if _, err := state.snapshot(); err == nil {
		t.Fatal("empty state supplied coordinates")
	}
	dimensions := screenDimensions{5, 3, 1920, 1080}
	epoch := state.beginCapture()
	if err := state.commit(context.Background(), epoch, dimensions); err != nil {
		t.Fatal(err)
	}
	if got, err := state.snapshot(); err != nil || got != dimensions {
		t.Fatalf("snapshot=%+v,%v", got, err)
	}
	state.invalidate()
	if err := state.commit(context.Background(), epoch, dimensions); err == nil {
		t.Fatal("capture preceding configuration change restored stale coordinates")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := state.commit(ctx, state.beginCapture(), dimensions); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled capture committed: %v", err)
	}
	if _, err := state.snapshot(); err == nil {
		t.Fatal("invalid/canceled capture populated coordinates")
	}
}

func TestScreenStateConcurrentAccess(t *testing.T) {
	state := &screenState{}
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			for range 100 {
				epoch := state.beginCapture()
				_ = state.commit(context.Background(), epoch, screenDimensions{i + 1, i + 1, i + 2, i + 2})
				_, _ = state.snapshot()
				state.invalidate()
			}
		})
	}
	workers.Wait()
}
