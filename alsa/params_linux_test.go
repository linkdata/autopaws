//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"unsafe"
)

func TestNegotiationPrefersStereoAndExactRateBeforeFallback(t *testing.T) {
	for _, test := range []struct {
		name         string
		stereo, hz48 bool
		fallback     uint32
		channels     uint32
		rate         uint32
	}{
		{"stereo 48k", true, true, 44_100, 2, 48_000},
		{"mono 48k", false, true, 44_100, 1, 48_000},
		{"stereo discrete rate hole", true, false, 44_100, 2, 44_100},
		{"96k mono", false, false, 96_000, 1, 96_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake, ops := fakeOperations()
			fake.stereo, fake.has48, fake.fallback = test.stereo, test.hz48, test.fallback
			config, err := negotiate(context.Background(), ops, 42)
			if err != nil || config.Channels != test.channels || config.Rate != test.rate || config.PeriodFrames != test.rate/50 {
				t.Fatalf("configuration=%+v,%v", config, err)
			}
		})
	}
}

func TestSetupFailuresAlwaysCloseOwnedDescriptor(t *testing.T) {
	for _, stage := range []string{"open", "blocking", "refine base", "refine 2 channels", "refine rate", "HW_PARAMS", "SW_PARAMS", "PREPARE"} {
		t.Run(stage, func(t *testing.T) {
			fake, ops := fakeOperations()
			fake.fail = stage
			capture, err := open(context.Background(), "/fake/pcm", ops)
			if capture != nil || !errors.Is(err, fake.failure) {
				t.Fatalf("Open=%v,%v", capture, err)
			}
			wantCloses := 1
			if stage == "open" {
				wantCloses = 0
			}
			if _, closes := fake.snapshot(); closes != wantCloses {
				t.Fatalf("close count=%d; want %d", closes, wantCloses)
			}
		})
	}
	fake, ops := fakeOperations()
	fake.stereo, fake.mono = false, false
	if _, err := open(context.Background(), "/fake/pcm", ops); !errors.Is(err, ErrSettings) {
		t.Fatalf("unsupported channel count=%v", err)
	}
	fake, ops = fakeOperations()
	fake.fail = "refine 1 channels"
	fake.stereo = false
	if _, err := open(context.Background(), "/fake/pcm", ops); !errors.Is(err, fake.failure) {
		t.Fatalf("mono fallback error=%v", err)
	}
	fake, ops = fakeOperations()
	fake.fail, fake.failure = "open", syscall.EBUSY
	if _, err := open(context.Background(), "/fake/pcm", ops); !errors.Is(err, ErrBusy) || !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("busy error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := open(ctx, "unused", kernel{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open=%v", err)
	}
	fake, ops = fakeOperations()
	originalOpen := ops.open
	ops.open = func(path string, flags int) (int, error) {
		fd, err := originalOpen(path, flags)
		cancel()
		return fd, err
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if _, err := open(ctx, "/fake/pcm", ops); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel after open=%v", err)
	}
	if events, closes := fake.snapshot(); len(events) != 2 || events[0] != "open" || events[1] != "close" || closes != 1 {
		t.Fatalf("cancelled startup lost ownership: %v/%d", events, closes)
	}
}

func TestNegotiatedSettingsAreVerified(t *testing.T) {
	for _, change := range []func(*hwParams){
		func(params *hwParams) { params.Masks[paramAccess].Bits[0] = 0 },
		func(params *hwParams) { params.Masks[paramFormat].Bits[0] = 0 },
		func(params *hwParams) { params.Masks[paramSubformat].Bits[0] = 0 },
		func(params *hwParams) { params.setRange(paramRate, 0, 0) },
		func(params *hwParams) { params.setRange(paramRate, 48_000, 48_001) },
		func(params *hwParams) { params.Intervals[paramChannels-firstInterval].Flags = intervalOpenMin },
		func(params *hwParams) { params.setRange(paramChannels, 3, 3) },
		func(params *hwParams) { params.setRange(paramChannels, 1, 1) },
		func(params *hwParams) { params.setRange(paramPeriodSize, 960, 961) },
		func(params *hwParams) { params.setRange(paramBufferSize, 3840, 3841) },
		func(params *hwParams) { params.setRange(paramPeriods, 4, 5) },
		func(params *hwParams) { params.setRange(paramPeriods, 3, 3) },
		func(params *hwParams) { params.setRange(paramPeriods, 9, 9) },
		func(params *hwParams) { params.setRange(paramPeriodSize, 0, 0) },
		func(params *hwParams) { params.setRange(paramPeriodSize, 1<<20+1, 1<<20+1) },
		func(params *hwParams) { params.setRange(paramPeriodSize, 48, 48) },
		func(params *hwParams) {
			params.setRange(paramPeriodSize, 4801, 4801)
			params.setRange(paramBufferSize, 19_204, 19_204)
		},
		func(params *hwParams) { params.setRange(paramBufferSize, 959, 959) },
		func(params *hwParams) { params.setRange(paramBufferSize, 7681, 7681) },
		func(params *hwParams) { params.setRange(paramRate, 44_100, 44_100) },
	} {
		fake, ops := fakeOperations()
		fake.mutate = func(request uintptr, pointer unsafe.Pointer) {
			if request == ioctlHWParams {
				change((*hwParams)(pointer))
			}
		}
		if _, err := open(context.Background(), "/fake/pcm", ops); !errors.Is(err, ErrSettings) {
			t.Fatalf("invalid HW_PARAMS accepted: %v", err)
		}
	}
	for _, change := range []func(*swParams){
		func(params *swParams) { params.TimestampMode = 0 },
		func(params *swParams) { params.TimestampType = 0 },
		func(params *swParams) { params.AvailableMin = 0 },
		func(params *swParams) { params.StartThreshold = 0 },
		func(params *swParams) { params.StopThreshold = 0 },
	} {
		fake, ops := fakeOperations()
		fake.mutate = func(request uintptr, pointer unsafe.Pointer) {
			if request == ioctlSWParams {
				change((*swParams)(pointer))
			}
		}
		if _, err := open(context.Background(), "/fake/pcm", ops); !errors.Is(err, ErrSettings) {
			t.Fatalf("invalid SW_PARAMS accepted: %v", err)
		}
	}
}
