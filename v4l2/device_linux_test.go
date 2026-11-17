//go:build linux && (amd64 || arm64)

package v4l2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type fakeRaw struct {
	control func(func(uintptr)) error
	read    func(func(uintptr) bool) error
}

func (raw *fakeRaw) Control(call func(uintptr)) error {
	if raw.control != nil {
		return raw.control(call)
	}
	call(123)
	return nil
}

func (raw *fakeRaw) Read(call func(uintptr) bool) error {
	if raw.read != nil {
		return raw.read(call)
	}
	if !call(123) {
		return syscall.EAGAIN
	}
	return nil
}

func (*fakeRaw) Write(func(uintptr) bool) error { panic("unexpected RawConn.Write") }

type fakeFile struct {
	raw         *fakeRaw
	rawErr      error
	closeErr    error
	deadlineErr error
	deadline    func(time.Time) error
	mu          sync.Mutex
	closes      int
}

func (file *fakeFile) SyscallConn() (syscall.RawConn, error) { return file.raw, file.rawErr }
func (file *fakeFile) SetReadDeadline(deadline time.Time) error {
	if file.deadline != nil {
		return file.deadline(deadline)
	}
	return file.deadlineErr
}

func (file *fakeFile) Close() error {
	file.mu.Lock()
	defer file.mu.Unlock()
	file.closes++
	return file.closeErr
}

type fakeCapture struct {
	file       *fakeFile
	caps       capability
	formats    []uint32
	actual     pixFormat
	count      uint32
	requested  uint32
	fail       string
	failure    error
	after      func(uintptr, unsafe.Pointer) error
	mapFailure int
	unmapErr   error

	mu          sync.Mutex
	events      []string
	mapped      [][]byte
	unmapped    [][]byte
	descriptors []buffer
}

func fakeOperations() (*fakeCapture, operations) {
	fake := &fakeCapture{
		file:    &fakeFile{raw: &fakeRaw{}},
		caps:    capability{Capabilities: capDeviceCaps | capVideoCapture | capStreaming, DeviceCaps: capVideoCapture | capStreaming},
		formats: []uint32{PixFmtMJPEG, 'Y' | 'U'<<8 | 'Y'<<16 | 'V'<<24},
		actual:  pixFormat{Width: 1280, Height: 720, PixelFormat: PixFmtMJPEG, BytesPerLine: 17, SizeImage: 4096},
		count:   2, failure: errors.New("injected failure"), mapFailure: -1,
	}
	ops := operations{
		open: func(path string, flags int) (file, error) {
			if path != "/fake/video" || flags != os.O_RDWR|syscall.O_NONBLOCK {
				panic(fmt.Sprintf("unexpected open: %s/%#x", path, flags))
			}
			fake.event("open")
			return fake.file, nil
		},
		ioctl: fake.ioctl,
		mmap: func(_ int, offset int64, length, prot, flags int) ([]byte, error) {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if offset != int64(len(fake.mapped)*os.Getpagesize()) || length != 8 || prot != syscall.PROT_READ|syscall.PROT_WRITE || flags != syscall.MAP_SHARED {
				panic("wrong MMAP arguments")
			}
			fake.events = append(fake.events, "mmap")
			if len(fake.mapped) == fake.mapFailure {
				return nil, fake.failure
			}
			mapped := []byte{1, 2, 3, 4, 5, 6, 7, 8}
			fake.mapped = append(fake.mapped, mapped)
			return mapped, nil
		},
		munmap: func(mapped []byte) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.events = append(fake.events, "munmap")
			fake.unmapped = append(fake.unmapped, mapped)
			return fake.unmapErr
		},
	}
	return fake, ops
}

func (fake *fakeCapture) event(event string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.events = append(fake.events, event)
}

func (fake *fakeCapture) ioctl(_ uintptr, request uintptr, pointer unsafe.Pointer) error {
	name := ""
	switch request {
	case vidiocQuerycap:
		name = "QUERYCAP"
	case vidiocEnumFmt:
		name = fmt.Sprintf("ENUM_FMT%d", (*fmtdesc)(pointer).Index)
	case vidiocSFmt:
		name = "S_FMT"
	case vidiocReqbufs:
		name = "REQBUFS"
	case vidiocQuerybuf:
		name = fmt.Sprintf("QUERYBUF%d", (*buffer)(pointer).Index)
	case vidiocQbuf:
		name = fmt.Sprintf("QBUF%d", (*buffer)(pointer).Index)
	case vidiocDqbuf:
		name = "DQBUF"
	case vidiocStreamon:
		name = "STREAMON"
	case vidiocStreamoff:
		name = "STREAMOFF"
	default:
		panic("unexpected ioctl")
	}
	fake.event(name)
	if fake.fail == name {
		return fake.failure
	}
	switch request {
	case vidiocQuerycap:
		*(*capability)(pointer) = fake.caps
	case vidiocEnumFmt:
		descriptor := (*fmtdesc)(pointer)
		if descriptor.Index >= uint32(len(fake.formats)) {
			return syscall.EINVAL
		}
		descriptor.PixelFormat = fake.formats[descriptor.Index]
		copy(descriptor.Description[:], "driver format")
	case vidiocSFmt:
		requested := (*format)(pointer)
		if requested.Type != bufTypeVideoCapture || requested.Pix.Width != 1920 || requested.Pix.Height != 1080 || requested.Pix.PixelFormat != PixFmtMJPEG {
			panic("wrong requested format")
		}
		requested.Pix = fake.actual
	case vidiocReqbufs:
		requested := (*requestbuffers)(pointer)
		fake.requested = requested.Count
		if requested.Type != bufTypeVideoCapture || requested.Memory != memoryMmap {
			panic("wrong REQBUFS arguments")
		}
		requested.Count = fake.count
	case vidiocQuerybuf:
		descriptor := (*buffer)(pointer)
		descriptor.Length, descriptor.Offset = 8, descriptor.Index*uint32(os.Getpagesize())
	case vidiocQbuf:
		descriptor := (*buffer)(pointer)
		if *descriptor != (buffer{Index: descriptor.Index, Type: bufTypeVideoCapture, Memory: memoryMmap}) {
			panic("QBUF reused an untrusted returned descriptor")
		}
	case vidiocDqbuf:
		fake.mu.Lock()
		if len(fake.descriptors) == 0 {
			fake.mu.Unlock()
			return syscall.EAGAIN
		}
		*(*buffer)(pointer) = fake.descriptors[0]
		fake.descriptors = fake.descriptors[1:]
		fake.mu.Unlock()
	case vidiocStreamon, vidiocStreamoff:
		if *(*uint32)(pointer) != bufTypeVideoCapture {
			panic("wrong stream type")
		}
	}
	if fake.after != nil {
		return fake.after(request, pointer)
	}
	return nil
}

func (fake *fakeCapture) snapshot() ([]string, int, int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.events...), len(fake.mapped), len(fake.unmapped)
}

func openFake(t *testing.T) (*Device, *fakeCapture) {
	t.Helper()
	fake, ops := fakeOperations()
	device, err := open(context.Background(), "/fake/video", Options{1920, 1080}, ops)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = device.Close() })
	return device, fake
}

func frameDescriptor(index, used uint32) buffer {
	return buffer{Index: index, BytesUsed: used, Type: bufTypeVideoCapture, Memory: memoryMmap, Timestamp: syscall.Timeval{Sec: 123, Usec: 456}, Flags: TimestampMonotonic | TimestampStartOfExposure, Sequence: 77}
}

func countEvent(events []string, name string) int {
	count := 0
	for _, event := range events {
		if event == name {
			count++
		}
	}
	return count
}

func TestOpenNegotiatesActualFormatAndOwnsCachedFormats(t *testing.T) {
	device, fake := openFake(t)
	if got, want := device.Format(), (Format{1280, 720, PixFmtMJPEG, 17, 4096}); got != want {
		t.Fatalf("format=%+v; want %+v", got, want)
	}
	formats := device.Formats()
	if len(formats) != 2 || formats[0].FourCC != PixFmtMJPEG || formats[0].Description != "driver format" {
		t.Fatalf("formats=%+v", formats)
	}
	formats[0].FourCC = 0
	if device.Formats()[0].FourCC != PixFmtMJPEG || fake.requested != 4 {
		t.Fatal("cached formats aliased caller memory or incorrect requested count")
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	if err := device.Close(); err != nil || fake.file.closes != 1 {
		t.Fatalf("idempotent close=%v; calls=%d", err, fake.file.closes)
	}
	if _, err := device.ReadFrame(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after close=%v", err)
	}
	if cString([]byte("unterminated")) != "unterminated" {
		t.Fatal("unterminated C description lost bytes")
	}
}

func TestOpenRejectsInvalidInputWithoutOpening(t *testing.T) {
	for _, options := range []Options{{}, {Width: 1}, {Height: 1}} {
		if _, err := open(context.Background(), "unused", options, operations{}); !errors.Is(err, ErrOptions) {
			t.Fatalf("options=%+v: %v", options, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := open(ctx, "unused", Options{1, 1}, operations{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open=%v", err)
	}
	if _, err := Open(context.Background(), t.TempDir()+"/missing", Options{1, 1}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path=%v", err)
	}
}

func TestEverySetupFailureCleansAcquiredMappingsAndFile(t *testing.T) {
	for _, stage := range []string{"raw", "deadline", "QUERYCAP", "ENUM_FMT0", "ENUM_FMT1", "S_FMT", "REQBUFS", "QUERYBUF0", "QUERYBUF1", "mmap0", "mmap1", "QBUF0", "QBUF1", "STREAMON"} {
		t.Run(stage, func(t *testing.T) {
			fake, ops := fakeOperations()
			switch stage {
			case "raw":
				fake.file.rawErr = fake.failure
			case "deadline":
				fake.file.deadlineErr = fake.failure
			case "mmap0":
				fake.mapFailure = 0
			case "mmap1":
				fake.mapFailure = 1
			default:
				fake.fail = stage
			}
			device, err := open(context.Background(), "/fake/video", Options{1920, 1080}, ops)
			if device != nil || !errors.Is(err, fake.failure) {
				t.Fatalf("Open=%v,%v", device, err)
			}
			events, mapped, unmapped := fake.snapshot()
			if mapped != unmapped || fake.file.closes != 1 {
				t.Fatalf("resources leaked: maps=%d unmaps=%d closes=%d events=%v", mapped, unmapped, fake.file.closes, events)
			}
			queued := countEvent(events, "QBUF0") != 0
			if (countEvent(events, "STREAMOFF") != 0) != queued {
				t.Fatalf("queued buffers not stopped before teardown: %v", events)
			}
		})
	}
}

func TestCapabilitiesFormatsAndDescriptorsAreValidated(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fakeCapture)
		want   error
	}{
		{"metadata_device_caps", func(fake *fakeCapture) { fake.caps.DeviceCaps = capStreaming }, ErrNotCapture},
		{"no_streaming", func(fake *fakeCapture) { fake.caps.DeviceCaps = capVideoCapture }, ErrNotCapture},
		{"no_mjpg", func(fake *fakeCapture) { fake.formats = []uint32{0} }, ErrUnsupportedFormat},
		{"substituted_format", func(fake *fakeCapture) { fake.actual.PixelFormat = 0 }, ErrUnsupportedFormat},
		{"zero_width", func(fake *fakeCapture) { fake.actual.Width = 0 }, ErrOptions},
		{"zero_height", func(fake *fakeCapture) { fake.actual.Height = 0 }, ErrOptions},
		{"zero_buffers", func(fake *fakeCapture) { fake.count = 0 }, ErrInvalidBuffer},
		{"too_many_buffers", func(fake *fakeCapture) { fake.count = maxFrames + 1 }, ErrInvalidBuffer},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake, ops := fakeOperations()
			test.change(fake)
			if device, err := open(context.Background(), "/fake/video", Options{1920, 1080}, ops); device != nil || !errors.Is(err, test.want) {
				t.Fatalf("Open=%v,%v; want %v", device, err, test.want)
			}
		})
	}
	for _, change := range []func(*buffer){
		func(buffer *buffer) { buffer.Index++ },
		func(buffer *buffer) { buffer.Type++ },
		func(buffer *buffer) { buffer.Memory++ },
		func(buffer *buffer) { buffer.Length = 0 },
		func(buffer *buffer) { buffer.Offset++ },
	} {
		fake, ops := fakeOperations()
		fake.after = func(request uintptr, arg unsafe.Pointer) error {
			if request == vidiocQuerybuf {
				change((*buffer)(arg))
			}
			return nil
		}
		if device, err := open(context.Background(), "/fake/video", Options{1920, 1080}, ops); device != nil || !errors.Is(err, ErrInvalidBuffer) {
			t.Fatalf("invalid QUERYBUF accepted: %v,%v", device, err)
		}
	}
	for _, count := range []uint32{1, 6, maxFrames} {
		fake, ops := fakeOperations()
		fake.count = count
		fake.caps.Capabilities = capVideoCapture | capStreaming // older nodes without DEVICE_CAPS
		device, err := open(context.Background(), "/fake/video", Options{1920, 1080}, ops)
		if err != nil {
			t.Fatal(err)
		}
		if len(device.buffers) != int(count) {
			t.Fatalf("actual count=%d; mapped=%d", count, len(device.buffers))
		}
		if err := device.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCopyBeforeRequeueAndDropMarkedFrames(t *testing.T) {
	device, fake := openFake(t)
	errorFrame, emptyFrame, valid := frameDescriptor(0, 3), frameDescriptor(0, 0), frameDescriptor(0, 3)
	errorFrame.Flags |= bufFlagError
	fake.descriptors = []buffer{errorFrame, emptyFrame, valid}
	fake.after = func(request uintptr, _ unsafe.Pointer) error {
		if request == vidiocQbuf {
			copy(fake.mapped[0], []byte{9, 8, 7})
		}
		return nil
	}
	frame, err := device.ReadFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame.Data, []byte{9, 8, 7}) || frame.Timestamp != (Timestamp{123, 456}) || frame.Flags != valid.Flags || frame.Sequence != 77 {
		t.Fatalf("frame=%+v", frame)
	}
	// Overwrite during the next QBUF, after copying a different buffer.
	fake.descriptors = []buffer{frameDescriptor(1, 2)}
	second, err := device.ReadFrame(context.Background())
	if err != nil || !bytes.Equal(second.Data, []byte{1, 2}) {
		t.Fatalf("second=%+v,%v", second, err)
	}
	for i := range fake.mapped[0] {
		fake.mapped[0][i] = 0
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame.Data, []byte{9, 8, 7}) || !bytes.Equal(second.Data, []byte{1, 2}) {
		t.Fatal("returned bytes aliased a reused/unmapped buffer")
	}
	events, _, _ := fake.snapshot()
	if countEvent(events, "DQBUF") != 4 || countEvent(events, "QBUF0") != 4 {
		t.Fatalf("bad/empty buffers were not requeued: %v", events)
	}
}

func TestFrameIsCopiedBeforeItsBufferReturnsToDriver(t *testing.T) {
	device, fake := openFake(t)
	fake.descriptors = []buffer{frameDescriptor(0, 3)}
	fake.after = func(request uintptr, _ unsafe.Pointer) error {
		if request == vidiocQbuf {
			copy(fake.mapped[0], []byte{9, 9, 9})
		}
		return nil
	}
	frame, err := device.ReadFrame(context.Background())
	if err != nil || !bytes.Equal(frame.Data, []byte{1, 2, 3}) || !bytes.Equal(fake.mapped[0][:3], []byte{9, 9, 9}) {
		t.Fatalf("frame copied after QBUF: data=%v mapped=%v error=%v", frame.Data, fake.mapped[0], err)
	}
}

func TestMalformedFramesRequeueKnownBuffersThenBecomeTerminal(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*buffer)
		want   error
		queued bool
	}{
		{"invalid_index", func(buffer *buffer) { buffer.Index = 2 }, ErrInvalidBuffer, false},
		{"invalid_type", func(buffer *buffer) { buffer.Type = 0 }, ErrInvalidBuffer, false},
		{"invalid_memory", func(buffer *buffer) { buffer.Memory = 0 }, ErrInvalidBuffer, false},
		{"oversize_payload", func(buffer *buffer) { buffer.BytesUsed = 9 }, ErrInvalidBuffer, true},
		{"negative_seconds", func(buffer *buffer) { buffer.Timestamp.Sec = -1 }, ErrTimestamp, true},
		{"negative_microseconds", func(buffer *buffer) { buffer.Timestamp.Usec = -1 }, ErrTimestamp, true},
		{"large_microseconds", func(buffer *buffer) { buffer.Timestamp.Usec = 1_000_000 }, ErrTimestamp, true},
		{"timestamp_overflow", func(buffer *buffer) { buffer.Timestamp.Sec = math.MaxInt64 }, ErrTimestamp, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			device, fake := openFake(t)
			descriptor := frameDescriptor(0, 3)
			test.change(&descriptor)
			fake.descriptors = []buffer{descriptor}
			if frame, err := device.ReadFrame(context.Background()); frame.Data != nil || !errors.Is(err, test.want) {
				t.Fatalf("ReadFrame=%+v,%v", frame, err)
			}
			if _, err := device.ReadFrame(context.Background()); !errors.Is(err, test.want) {
				t.Fatalf("faulted source resumed: %v", err)
			}
			events, _, _ := fake.snapshot()
			wantQueues := 1
			if test.queued {
				wantQueues++
			}
			if countEvent(events, "DQBUF") != 1 || countEvent(events, "QBUF0") != wantQueues {
				t.Fatalf("descriptor handling=%v", events)
			}
		})
	}
}

func TestRequeueFailureRemainsTerminalWhenCancellationRaces(t *testing.T) {
	device, fake := openFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.descriptors = []buffer{frameDescriptor(0, 3)}
	fake.after = func(request uintptr, _ unsafe.Pointer) error {
		if request == vidiocDqbuf {
			cancel()
		}
		if request == vidiocQbuf {
			return fake.failure
		}
		return nil
	}
	if frame, err := device.ReadFrame(ctx); frame.Data != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, fake.failure) {
		t.Fatalf("ReadFrame=%+v,%v", frame, err)
	}
	if _, err := device.ReadFrame(context.Background()); !errors.Is(err, fake.failure) {
		t.Fatalf("requeue failure was forgotten: %v", err)
	}
	events, _, _ := fake.snapshot()
	if countEvent(events, "DQBUF") != 1 || countEvent(events, "QBUF0") != 2 {
		t.Fatalf("cancelled dequeue did not account for its buffer: %v", events)
	}
}

func TestDeviceLossAndQueueFailureRemainMatchable(t *testing.T) {
	for _, stage := range []string{"DQBUF", "QBUF0"} {
		device, fake := openFake(t)
		fake.fail, fake.failure = stage, syscall.ENODEV
		fake.descriptors = []buffer{frameDescriptor(0, 1)}
		if _, err := device.ReadFrame(context.Background()); !errors.Is(err, ErrDisconnected) || !errors.Is(err, syscall.ENODEV) {
			t.Fatalf("%s error=%v", stage, err)
		}
	}
	for _, errno := range []error{syscall.ENODEV, syscall.ENXIO} {
		fake, ops := fakeOperations()
		fake.fail, fake.failure = "QUERYCAP", errno
		if _, err := open(context.Background(), "/fake/video", Options{1920, 1080}, ops); !errors.Is(err, ErrDisconnected) || !errors.Is(err, errno) {
			t.Fatalf("open disconnected=%v", err)
		}
	}
}

func receiveDevice[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("controlled device operation did not complete")
		var zero T
		return zero
	}
}

func TestCloseWaitsForOwnedOperationBeforeUnmapping(t *testing.T) {
	device, fake := openFake(t)
	entered, finish, woke := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var wake sync.Once
	fake.file.deadline = func(deadline time.Time) error {
		if !deadline.IsZero() {
			wake.Do(func() { close(woke) })
		}
		return nil
	}
	fake.descriptors = []buffer{frameDescriptor(0, 3)}
	fake.after = func(request uintptr, _ unsafe.Pointer) error {
		if request == vidiocQbuf {
			close(entered)
			<-finish // model an executing syscall that cannot be interrupted
		}
		return nil
	}
	read := make(chan error, 1)
	go func() { _, err := device.ReadFrame(context.Background()); read <- err }()
	receiveDevice(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- device.Close() }()
	receiveDevice(t, woke)
	if _, _, unmapped := fake.snapshot(); unmapped != 0 {
		t.Fatal("Close unmapped while a reader owned a kernel operation")
	}
	select {
	case err := <-closed:
		t.Fatalf("Close abandoned an executing operation: %v", err)
	default:
	}
	close(finish)
	if err := receiveDevice(t, read); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after Close intent=%v", err)
	}
	if err := receiveDevice(t, closed); err != nil {
		t.Fatal(err)
	}
	if events, mapped, unmapped := fake.snapshot(); mapped != unmapped || events[len(events)-1] != "munmap" {
		t.Fatalf("incomplete cleanup: maps=%d unmaps=%d events=%v", mapped, unmapped, events)
	}
}

func TestCloseIntentSurvivesReaderDeadlineReset(t *testing.T) {
	device, fake := openFake(t)
	resetting, reset, woke := make(chan struct{}), make(chan struct{}), make(chan struct{})
	fake.file.deadline = func(deadline time.Time) error {
		if deadline.IsZero() {
			close(resetting)
			<-reset
		} else {
			close(woke)
		}
		return nil
	}
	read := make(chan error, 1)
	go func() { _, err := device.ReadFrame(context.Background()); read <- err }()
	receiveDevice(t, resetting)
	closed := make(chan error, 1)
	go func() { closed <- device.Close() }()
	receiveDevice(t, woke)
	close(reset)
	if err := receiveDevice(t, read); !errors.Is(err, ErrClosed) {
		t.Fatalf("close/read reset race=%v", err)
	}
	if err := receiveDevice(t, closed); err != nil {
		t.Fatal(err)
	}
	if events, _, _ := fake.snapshot(); countEvent(events, "DQBUF") != 0 {
		t.Fatalf("read entered netpoll after close intent: %v", events)
	}
}

func TestCloseAttemptsAllCleanupAndPreservesFailure(t *testing.T) {
	device, fake := openFake(t)
	stopErr, unmapErr, closeErr, wakeErr := errors.New("stop"), errors.New("unmap"), errors.New("close"), errors.New("wake")
	fake.fail, fake.failure = "STREAMOFF", stopErr
	fake.unmapErr, fake.file.closeErr, fake.file.deadlineErr = unmapErr, closeErr, wakeErr
	err := device.Close()
	for _, want := range []error{stopErr, unmapErr, closeErr, wakeErr} {
		if !errors.Is(err, want) {
			t.Errorf("lost %v from %v", want, err)
		}
	}
	if err != device.Close() {
		t.Fatal("Close did not retain shared result")
	}
	if _, mapped, unmapped := fake.snapshot(); mapped != unmapped || fake.file.closes != 1 {
		t.Fatalf("cleanup stopped early: %d/%d closes=%d", mapped, unmapped, fake.file.closes)
	}
}

func TestTimestampConversionAndFlags(t *testing.T) {
	for _, test := range []struct {
		timestamp Timestamp
		want      int64
	}{
		{Timestamp{}, 0},
		{Timestamp{1, 999999}, 1_999_999_000},
		{Timestamp{math.MaxInt64 / 1_000_000_000, 854775}, 9_223_372_036_854_775_000},
	} {
		if got, err := test.timestamp.Nanoseconds(); err != nil || got != test.want {
			t.Fatalf("timestamp=%+v -> %d,%v; want %d", test.timestamp, got, err, test.want)
		}
	}
	if _, err := (Timestamp{math.MaxInt64 / 1_000_000_000, 854776}).Nanoseconds(); !errors.Is(err, ErrTimestamp) {
		t.Fatalf("overflow at fractional boundary=%v", err)
	}
	if TimestampMonotonic&TimestampMask != TimestampMonotonic || TimestampCopy&TimestampMask != TimestampCopy || TimestampUnknown != 0 ||
		TimestampStartOfExposure&TimestampSourceMask != TimestampStartOfExposure || TimestampEndOfFrame != 0 {
		t.Fatal("timestamp flags lost raw Linux meaning")
	}
}

func TestQueuedReadsCancelWithoutTouchingDevice(t *testing.T) {
	device, fake := openFake(t)
	<-device.gate
	ctx, cancel := context.WithCancel(context.Background())
	observed := make(chan struct{})
	queued := observedContext{Context: ctx, observed: observed, once: &sync.Once{}}
	result := make(chan error, 1)
	go func() { _, err := device.ReadFrame(queued); result <- err }()
	receiveDevice(t, observed)
	cancel()
	if err := receiveDevice(t, result); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	device.release()
	events, _, _ := fake.snapshot()
	if strings.Contains(strings.Join(events, ","), "DQBUF") {
		t.Fatal("cancelled queued caller touched device")
	}
	if !reflect.DeepEqual(device.Format(), Format{1280, 720, PixFmtMJPEG, 17, 4096}) {
		t.Fatal("queued cancellation changed format")
	}
}

type observedContext struct {
	context.Context
	observed chan struct{}
	once     *sync.Once
}

func (ctx observedContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.observed) })
	return ctx.Context.Done()
}

type cancelAtAdmission struct {
	context.Context
	cancel context.CancelFunc
}

func (ctx cancelAtAdmission) Done() <-chan struct{} {
	ctx.cancel()
	return ctx.Context.Done()
}

func TestAdmissionCancellationAndQueuedClosePreserveOwnership(t *testing.T) {
	device, fake := openFake(t)
	for range 128 {
		ctx, cancel := context.WithCancel(context.Background())
		if _, err := device.ReadFrame(cancelAtAdmission{ctx, cancel}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled admission=%v", err)
		}
		cancel()
	}
	<-device.gate
	observed := make(chan struct{})
	ctx := observedContext{Context: context.Background(), observed: observed, once: &sync.Once{}}
	read := make(chan error, 1)
	go func() { _, err := device.ReadFrame(ctx); read <- err }()
	receiveDevice(t, observed)
	closed := make(chan error, 1)
	go func() { closed <- device.Close() }()
	if err := receiveDevice(t, read); !errors.Is(err, ErrClosed) {
		t.Fatalf("queued close=%v", err)
	}
	device.release()
	if err := receiveDevice(t, closed); err != nil {
		t.Fatal(err)
	}
	if events, _, _ := fake.snapshot(); countEvent(events, "DQBUF") != 0 {
		t.Fatalf("cancelled admission touched capture: %v", events)
	}
}

func TestMappingControlAndReturnedLengthFailuresCleanUp(t *testing.T) {
	fake, ops := fakeOperations()
	fake.after = func(request uintptr, _ unsafe.Pointer) error {
		if request == vidiocQuerybuf {
			fake.file.raw.control = func(func(uintptr)) error { return fake.failure }
		}
		return nil
	}
	if _, err := open(context.Background(), "/fake/video", Options{1920, 1080}, ops); !errors.Is(err, fake.failure) || fake.file.closes != 1 {
		t.Fatalf("mmap RawConn ownership failure=%v closes=%d", err, fake.file.closes)
	}
	for _, mapped := range [][]byte{nil, {0}} {
		fake, ops := fakeOperations()
		ops.mmap = func(int, int64, int, int, int) ([]byte, error) { return mapped, nil }
		if _, err := open(context.Background(), "/fake/video", Options{1920, 1080}, ops); !errors.Is(err, ErrInvalidBuffer) || fake.file.closes != 1 {
			t.Fatalf("wrong mmap length=%v closes=%d", err, fake.file.closes)
		}
		if _, _, unmapped := fake.snapshot(); (unmapped == 1) != (len(mapped) != 0) {
			t.Fatalf("invalid mapping was not accounted for: len=%d unmaps=%d", len(mapped), unmapped)
		}
	}
}
