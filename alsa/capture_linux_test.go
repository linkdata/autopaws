//go:build linux && (amd64 || arm64)

package alsa

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type readEvent struct {
	err       error
	frames    int64
	state     int32
	delay     int64
	available uint64
	timestamp int64
}

type fakeKernel struct {
	mu           sync.Mutex
	stereo       bool
	mono         bool
	has48        bool
	fallback     uint32
	failure      error
	fail         string
	mutate       func(uintptr, unsafe.Pointer)
	read         chan readEvent
	reading      chan struct{}
	dropped      chan struct{}
	dropOnce     sync.Once
	dropEntered  chan struct{}
	dropSignaled sync.Once
	dropFinish   chan struct{}
	closeErr     error
	activeReads  int
	activeDrops  int
	closes       int
	events       []string
	last         readEvent
	channels     uint32
	now          int64
}

func fakeOperations() (*fakeKernel, kernel) {
	fake := &fakeKernel{
		stereo: true, mono: true, has48: true, fallback: 44_100,
		failure: errors.New("injected kernel failure"), read: make(chan readEvent),
		reading: make(chan struct{}, 32), dropped: make(chan struct{}), now: int64(10 * time.Second),
	}
	return fake, kernel{
		open: func(path string, flags int) (int, error) {
			if path != "/fake/pcm" || flags != syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC {
				panic("PCM open omitted exclusive nonblocking flags")
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.events = append(fake.events, "open")
			if fake.fail == "open" {
				return -1, fake.failure
			}
			return 42, nil
		},
		setNonblock: func(fd int, nonblock bool) error {
			if fd != 42 || nonblock {
				panic("PCM worker must use blocking reads")
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.events = append(fake.events, "blocking")
			if fake.fail == "blocking" {
				return fake.failure
			}
			return nil
		},
		ioctl: fake.ioctl,
		close: func(fd int) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if fd != 42 || fake.activeReads != 0 || fake.activeDrops != 0 {
				panic("fd closed/reused while a kernel operation still owns it")
			}
			fake.events = append(fake.events, "close")
			fake.closes++
			return fake.closeErr
		},
		now: func() (int64, error) {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			return fake.now, nil
		},
	}
}

func (fake *fakeKernel) ioctl(fd int, request uintptr, argument unsafe.Pointer) error {
	if fd != 42 {
		panic("wrong descriptor")
	}
	fake.mu.Lock()
	name := ""
	switch request {
	case ioctlHWRefine:
		params := (*hwParams)(argument)
		channels := params.Intervals[paramChannels-firstInterval]
		rate := params.Intervals[paramRate-firstInterval]
		switch {
		case channels.Min != channels.Max:
			name = "refine base"
		case rate.Min == 48_000 && rate.Max == 48_000:
			name = "refine rate"
		default:
			name = fmt.Sprintf("refine %d channels", channels.Min)
		}
	case ioctlHWParams:
		name = "HW_PARAMS"
	case ioctlSWParams:
		name = "SW_PARAMS"
	case ioctlPrepare:
		name = "PREPARE"
	case ioctlRead:
		name = "READI"
	case ioctlStatus:
		name = "STATUS"
	case ioctlDrop:
		name = "DROP"
	default:
		panic(fmt.Sprintf("unexpected ioctl %#x (explicit START is forbidden)", request))
	}
	fake.events = append(fake.events, name)
	if fake.fail == name {
		if name == "DROP" && fake.dropEntered != nil {
			fake.dropSignaled.Do(func() { close(fake.dropEntered) })
		}
		fake.mu.Unlock()
		return fake.failure
	}
	if request == ioctlRead {
		fake.activeReads++
		fake.mu.Unlock()
		defer func() { fake.mu.Lock(); fake.activeReads--; fake.mu.Unlock() }()
		fake.reading <- struct{}{}
		var event readEvent
		select {
		case event = <-fake.read:
		case <-fake.dropped:
			return syscall.EBADFD
		}
		if event.err != nil {
			return event.err
		}
		fake.mu.Lock()
		fake.last = event
		channels := fake.channels
		fake.mu.Unlock()
		transfer := (*transfer)(argument)
		storage := unsafe.Slice((*byte)(transfer.Buffer), int(transfer.Frames)*int(channels)*2)
		for i := range storage {
			storage[i] = byte(i + 1)
		}
		transfer.Result = event.frames
		return nil
	}
	if request == ioctlDrop {
		fake.activeDrops++
		entered, finish := fake.dropEntered, fake.dropFinish
		fake.mu.Unlock()
		fake.dropOnce.Do(func() { close(fake.dropped) })
		if entered != nil {
			fake.dropSignaled.Do(func() { close(entered) })
		}
		if finish != nil {
			<-finish
		}
		fake.mu.Lock()
		fake.activeDrops--
		fake.mu.Unlock()
		return nil
	}
	defer fake.mu.Unlock()
	switch request {
	case ioctlHWRefine:
		params := (*hwParams)(argument)
		channels := params.Intervals[paramChannels-firstInterval]
		if channels.Min == channels.Max && (channels.Min == 2 && !fake.stereo || channels.Min == 1 && !fake.mono) {
			return syscall.EINVAL
		}
		if name == "refine rate" && !fake.has48 {
			return syscall.EINVAL // 48 kHz can be a hole inside the range.
		}
		if name == "refine base" {
			params.Intervals[paramChannels-firstInterval] = interval{Min: 1, Max: 2}
			params.Intervals[paramRate-firstInterval] = interval{Min: 44_100, Max: 96_000}
		}
	case ioctlHWParams:
		params := (*hwParams)(argument)
		channels := params.Intervals[paramChannels-firstInterval].Min
		rate := params.Intervals[paramRate-firstInterval].Min
		if !fake.has48 {
			rate = fake.fallback
		}
		params.setRange(paramChannels, channels, channels)
		params.setRange(paramRate, rate, rate)
		params.setRange(paramPeriodSize, rate/50, rate/50)
		params.setRange(paramBufferSize, rate/50*4, rate/50*4)
		params.setRange(paramPeriods, 4, 4)
		fake.channels = channels
	case ioctlStatus:
		event := fake.last
		*(*status)(argument) = status{
			State: event.state, Delay: event.delay, Available: event.available,
			Timestamp: syscall.NsecToTimespec(event.timestamp),
		}
	}
	if fake.mutate != nil {
		fake.mutate(request, argument)
	}
	return nil
}

func (fake *fakeKernel) snapshot() ([]string, int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.events...), fake.closes
}

func receive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("controlled capture operation did not finish")
		var zero T
		return zero
	}
}

func sendRead(t *testing.T, fake *fakeKernel, event readEvent) {
	t.Helper()
	select {
	case fake.read <- event:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not accept controlled read")
	}
}

func openFake(t *testing.T) (*Capture, *fakeKernel) {
	t.Helper()
	fake, ops := fakeOperations()
	capture, err := open(context.Background(), "/fake/pcm", ops)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	return capture, fake
}

func TestCaptureReadsOnlyValidSamplesAndPreservesMetadata(t *testing.T) {
	capture, fake := openFake(t)
	subscription := capture.Subscribe()
	config := capture.Config()
	if config.Rate != 48_000 || config.Channels != 2 || config.PeriodFrames != 960 || config.BufferFrames != 3840 || config.Generation == 0 {
		t.Fatalf("negotiated config=%+v", config)
	}
	sendRead(t, fake, readEvent{frames: 480, state: stateRunning, delay: 480, available: 480, timestamp: int64(10 * time.Second)})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	block, err := subscription.Next(ctx)
	if err != nil || block.Frames != 480 || len(block.Data) != 480*4 || block.Sequence != 0 || block.Config != config || block.TimestampNS != int64(9980*time.Millisecond) {
		t.Fatalf("block=%+v bytes=%d err=%v", block, len(block.Data), err)
	}
	saved := bytes.Clone(block.Data)
	sendRead(t, fake, readEvent{frames: 960, state: stateRunning, timestamp: int64(10 * time.Second)})
	if _, err := subscription.Next(ctx); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, block.Data) {
		t.Fatal("next read reused returned block storage")
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	if _, closes := fake.snapshot(); closes != 1 {
		t.Fatalf("descriptor closed %d times", closes)
	}
	if _, err := subscription.Next(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed subscription=%v", err)
	}
	if _, err := capture.Subscribe().Next(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("late subscription=%v", err)
	}
}

func TestCloseDropsBeforeJoiningAndRetainsExecutingDrop(t *testing.T) {
	capture, fake := openFake(t)
	receive(t, fake.reading)
	fake.mu.Lock()
	fake.dropEntered, fake.dropFinish = make(chan struct{}), make(chan struct{})
	entered, finish := fake.dropEntered, fake.dropFinish
	fake.mu.Unlock()
	unblock := sync.OnceFunc(func() { close(finish) })
	defer unblock()
	closed := make(chan error, 1)
	go func() { closed <- capture.Close() }()
	receive(t, entered)
	if _, closes := fake.snapshot(); closes != 0 {
		t.Fatal("worker closed fd before DROP returned")
	}
	select {
	case err := <-closed:
		t.Fatalf("Close abandoned an executing kernel operation: %v", err)
	default:
	}
	unblock()
	if err := receive(t, closed); err != nil {
		t.Fatal(err)
	}
}

func TestReadErrorsRecoverOrTerminateWithOwnedClose(t *testing.T) {
	for _, errno := range []error{syscall.EPIPE, syscall.ESTRPIPE, syscall.EINTR, syscall.EAGAIN} {
		t.Run(errno.Error(), func(t *testing.T) {
			capture, fake := openFake(t)
			subscription := capture.Subscribe()
			before := capture.Config()
			sendRead(t, fake, readEvent{err: errno})
			sendRead(t, fake, readEvent{frames: 960, state: stateRunning, timestamp: int64(10 * time.Second)})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			block, err := subscription.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			recovered := errors.Is(errno, syscall.EPIPE) || errors.Is(errno, syscall.ESTRPIPE)
			if (block.Config.Generation != before.Generation) != recovered || block.Sequence != 0 {
				t.Fatalf("recovery generation/sequence=%+v before=%+v", block, before)
			}
			stats := capture.Stats()
			if stats.Overruns != boolCount(errors.Is(errno, syscall.EPIPE)) || stats.Suspends != boolCount(errors.Is(errno, syscall.ESTRPIPE)) {
				t.Fatalf("stats=%+v", stats)
			}
		})
	}
	for _, event := range []readEvent{
		{err: syscall.ENODEV},
		{err: syscall.EIO},
		{frames: -1},
		{frames: 0},
		{frames: 961},
		{frames: 1, state: stateDisconnected},
		{frames: 1, state: 0},
		{frames: 1, state: stateRunning, timestamp: -1},
	} {
		capture, fake := openFake(t)
		subscription := capture.Subscribe()
		sendRead(t, fake, event)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := subscription.Next(ctx)
		cancel()
		if err == nil {
			t.Fatalf("terminal read succeeded: %+v", event)
		}
		receive(t, capture.done)
		if _, closes := fake.snapshot(); closes != 1 {
			t.Fatal("terminal worker did not close fd")
		}
		if errors.Is(event.err, syscall.ENODEV) && !errors.Is(err, ErrDisconnected) {
			t.Fatalf("lost disconnect identity: %v", err)
		}
	}
}

func boolCount(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

func TestStatusOverrunAndSuspendRecoverWithoutEmittingUntimedSamples(t *testing.T) {
	for _, event := range []readEvent{
		{frames: 960, state: stateXRun},
		{frames: 960, state: stateSuspended},
		{frames: 960, state: stateRunning, available: 3840},
	} {
		capture, fake := openFake(t)
		subscription := capture.Subscribe()
		generation := capture.Config().Generation
		sendRead(t, fake, event)
		sendRead(t, fake, readEvent{frames: 960, state: stateRunning, timestamp: int64(10 * time.Second)})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		block, err := subscription.Next(ctx)
		cancel()
		if err != nil || block.Config.Generation == generation || block.Sequence != 0 {
			t.Fatalf("status recovery block=%+v,%v", block, err)
		}
	}
}
