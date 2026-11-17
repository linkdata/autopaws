//go:build linux && (amd64 || arm64)

package v4l2

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type sysTestRaw struct {
	control func(func(uintptr)) error
	read    func(func(uintptr) bool) error
}

func (raw sysTestRaw) Control(call func(uintptr)) error {
	if raw.control != nil {
		return raw.control(call)
	}
	call(42)
	return nil
}

func (raw sysTestRaw) Read(call func(uintptr) bool) error {
	if raw.read != nil {
		return raw.read(call)
	}
	call(42)
	return nil
}

func (sysTestRaw) Write(func(uintptr) bool) error { panic("unexpected RawConn.Write") }

func sysTestDevice(raw syscall.RawConn, ioctl func(uintptr, uintptr, unsafe.Pointer) error) *Device {
	return &Device{raw: raw, ops: operations{ioctl: ioctl}, lifetime: context.Background()}
}

func TestSystemOperationsWithoutCaptureHardware(t *testing.T) {
	ops := systemOperations()
	file, err := ops.open("/dev/null", os.O_RDWR|syscall.O_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var caps capability
	var ioctlErr error
	if err := raw.Control(func(fd uintptr) { ioctlErr = ops.ioctl(fd, vidiocQuerycap, unsafe.Pointer(&caps)) }); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ioctlErr, syscall.ENOTTY) {
		t.Fatalf("QUERYCAP on /dev/null = %v; want ENOTTY", ioctlErr)
	}
	if _, err := ops.open(filepath.Join(t.TempDir(), "missing"), os.O_RDWR|syscall.O_NONBLOCK); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path error = %v", err)
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := writer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	raw, err = reader.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var queued int32
	if err := raw.Control(func(fd uintptr) { ioctlErr = ops.ioctl(fd, syscall.TIOCINQ, unsafe.Pointer(&queued)) }); err != nil {
		t.Fatal(err)
	}
	if ioctlErr != nil || queued != 3 {
		t.Fatalf("pipe TIOCINQ = %d, %v; want 3, nil", queued, ioctlErr)
	}

	mappedFile, err := os.CreateTemp(t.TempDir(), "mapped")
	if err != nil {
		t.Fatal(err)
	}
	defer mappedFile.Close()
	if err := mappedFile.Truncate(int64(os.Getpagesize())); err != nil {
		t.Fatal(err)
	}
	raw, err = mappedFile.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var mapped []byte
	var mapErr error
	if err := raw.Control(func(fd uintptr) {
		mapped, mapErr = ops.mmap(int(fd), 0, os.Getpagesize(), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	}); err != nil {
		t.Fatal(err)
	}
	if mapErr != nil {
		t.Fatal(mapErr)
	}
	defer func() {
		if err := ops.munmap(mapped); err != nil {
			t.Error(err)
		}
	}()
	copy(mapped, "mmap")
	var disk [4]byte
	if _, err := mappedFile.ReadAt(disk[:], 0); err != nil || string(disk[:]) != "mmap" {
		t.Fatalf("shared mapping write = %q, %v", disk, err)
	}
	if _, err := mappedFile.WriteAt([]byte("file"), 8); err != nil {
		t.Fatal(err)
	}
	if string(mapped[8:12]) != "file" {
		t.Fatalf("file write absent from shared mapping: %q", mapped[8:12])
	}
}

func TestControlRetriesAndRetainsErrors(t *testing.T) {
	failure := errors.New("control failure")
	t.Run("EINTR retry", func(t *testing.T) {
		calls := 0
		device := sysTestDevice(sysTestRaw{}, func(uintptr, uintptr, unsafe.Pointer) error {
			calls++
			if calls == 1 {
				return syscall.EINTR
			}
			return nil
		})
		if err := device.control(context.Background(), vidiocQuerycap, nil); err != nil || calls != 2 {
			t.Fatalf("control = %v after %d calls", err, calls)
		}
	})
	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		device := sysTestDevice(sysTestRaw{}, func(uintptr, uintptr, unsafe.Pointer) error {
			t.Fatal("canceled control reached ioctl")
			return nil
		})
		if err := device.control(ctx, vidiocQuerycap, nil); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("cancellation after ioctl", func(t *testing.T) {
		for _, errno := range []error{nil, syscall.EINTR} {
			ctx, cancel := context.WithCancel(context.Background())
			calls := 0
			device := sysTestDevice(sysTestRaw{}, func(uintptr, uintptr, unsafe.Pointer) error {
				calls++
				cancel()
				return errno
			})
			err := device.control(ctx, vidiocQuerycap, nil)
			if !errors.Is(err, context.Canceled) || errno != nil && !errors.Is(err, errno) || calls != 1 {
				t.Fatalf("control = %v after %d calls", err, calls)
			}
		}
	})
	t.Run("raw control joins ioctl", func(t *testing.T) {
		for _, errno := range []error{syscall.EIO, syscall.EINTR} {
			device := sysTestDevice(sysTestRaw{control: func(call func(uintptr)) error {
				call(42)
				return failure
			}}, func(uintptr, uintptr, unsafe.Pointer) error { return errno })
			if err := device.control(context.Background(), vidiocQuerycap, nil); !errors.Is(err, failure) || !errors.Is(err, errno) {
				t.Fatalf("control lost a failure: %v", err)
			}
		}
	})
	t.Run("raw control before callback", func(t *testing.T) {
		device := sysTestDevice(sysTestRaw{control: func(func(uintptr)) error { return failure }}, func(uintptr, uintptr, unsafe.Pointer) error {
			t.Fatal("failed raw control reached ioctl")
			return nil
		})
		if err := device.control(context.Background(), vidiocQuerycap, nil); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	})
}

func TestDequeueRetriesAndOwnership(t *testing.T) {
	t.Run("EAGAIN parks and EINTR retries", func(t *testing.T) {
		calls := 0
		device := sysTestDevice(sysTestRaw{read: func(call func(uintptr) bool) error {
			if call(42) {
				t.Fatal("EAGAIN did not request polling")
			}
			if !call(42) {
				t.Fatal("successful dequeue requested another poll")
			}
			return nil
		}}, func(_ uintptr, request uintptr, argument unsafe.Pointer) error {
			if request != vidiocDqbuf {
				t.Fatalf("unexpected ioctl %#x", request)
			}
			calls++
			switch calls {
			case 1:
				return syscall.EAGAIN
			case 2:
				return syscall.EINTR
			default:
				descriptor := (*buffer)(argument)
				if descriptor.Type != bufTypeVideoCapture || descriptor.Memory != memoryMmap {
					t.Fatalf("dequeue input = %+v", descriptor)
				}
				descriptor.Index, descriptor.BytesUsed, descriptor.Sequence = 3, 4, 7
				return nil
			}
		})
		descriptor, owned, err := device.dequeue(context.Background())
		if err != nil || !owned || descriptor.Index != 3 || descriptor.BytesUsed != 4 || descriptor.Sequence != 7 || calls != 3 {
			t.Fatalf("dequeue = %+v, %t, %v after %d calls", descriptor, owned, err, calls)
		}
	})
	t.Run("raw read failure", func(t *testing.T) {
		failure := errors.New("raw read failed")
		device := sysTestDevice(sysTestRaw{read: func(func(uintptr) bool) error { return failure }}, nil)
		if _, owned, err := device.dequeue(context.Background()); owned || !errors.Is(err, failure) {
			t.Fatalf("dequeue ownership=%t error=%v", owned, err)
		}
	})
	t.Run("ioctl failure", func(t *testing.T) {
		device := sysTestDevice(sysTestRaw{}, func(uintptr, uintptr, unsafe.Pointer) error { return syscall.ENODEV })
		if _, owned, err := device.dequeue(context.Background()); owned || !errors.Is(err, syscall.ENODEV) {
			t.Fatalf("dequeue ownership=%t error=%v", owned, err)
		}
	})
	t.Run("fatal ioctl retained when cancellation races", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		device := sysTestDevice(sysTestRaw{}, func(uintptr, uintptr, unsafe.Pointer) error {
			cancel()
			return syscall.EIO
		})
		_, owned, err := device.dequeue(ctx)
		if owned || !errors.Is(err, context.Canceled) || !errors.Is(err, syscall.EIO) || !errors.Is(device.readErr, syscall.EIO) {
			t.Fatalf("dequeue ownership=%t error=%v; retained error=%v", owned, err, device.readErr)
		}
	})
	t.Run("canceled retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		device := sysTestDevice(sysTestRaw{}, func(uintptr, uintptr, unsafe.Pointer) error {
			cancel()
			return syscall.EINTR
		})
		if _, owned, err := device.dequeue(ctx); owned || !errors.Is(err, context.Canceled) {
			t.Fatalf("dequeue ownership=%t error=%v", owned, err)
		}
	})
	t.Run("permanent close", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		device := sysTestDevice(sysTestRaw{}, func(uintptr, uintptr, unsafe.Pointer) error {
			t.Fatal("closed dequeue reached ioctl")
			return nil
		})
		device.lifetime = ctx
		if _, owned, err := device.dequeue(context.Background()); owned || !errors.Is(err, ErrClosed) {
			t.Fatalf("dequeue ownership=%t error=%v", owned, err)
		}
	})
}

type sysTestPipeFile struct {
	*os.File
	mu       sync.Mutex
	deadline func(time.Time) error
	onClose  func()
}

func (file *sysTestPipeFile) SetReadDeadline(deadline time.Time) error {
	file.mu.Lock()
	hook := file.deadline
	file.mu.Unlock()
	if hook != nil {
		return hook(deadline)
	}
	return file.File.SetReadDeadline(deadline)
}

func (file *sysTestPipeFile) setDeadlineHook(hook func(time.Time) error) {
	file.mu.Lock()
	defer file.mu.Unlock()
	file.deadline = hook
}

func (file *sysTestPipeFile) Close() error {
	file.onClose()
	return file.File.Close()
}

type sysTestPipeBackend struct {
	file    *sysTestPipeFile
	writer  *os.File
	waiting chan struct{}
	mapped  []byte
	mu      sync.Mutex
	events  []string
	queued  []buffer
	afterDQ func()
}

func (backend *sysTestPipeBackend) event(event string) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.events = append(backend.events, event)
}

func (backend *sysTestPipeBackend) snapshot() ([]string, []buffer) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return append([]string(nil), backend.events...), append([]buffer(nil), backend.queued...)
}

func sysTestOpenPipe(t *testing.T) (*Device, *sysTestPipeBackend) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	backend := &sysTestPipeBackend{writer: writer, waiting: make(chan struct{}, 8), mapped: []byte("MJPGdata")}
	backend.file = &sysTestPipeFile{File: reader, onClose: func() { backend.event("close") }}
	device, err := open(context.Background(), "pipe-backed-capture", Options{Width: 2, Height: 2}, operations{
		open: func(_ string, flags int) (file, error) {
			if flags != os.O_RDWR|syscall.O_NONBLOCK {
				t.Errorf("open flags = %#x", flags)
			}
			return backend.file, nil
		},
		ioctl: func(fd, request uintptr, argument unsafe.Pointer) error {
			switch request {
			case vidiocQuerycap:
				(*capability)(argument).Capabilities = capVideoCapture | capStreaming
			case vidiocEnumFmt:
				descriptor := (*fmtdesc)(argument)
				if descriptor.Index != 0 {
					return syscall.EINVAL
				}
				descriptor.PixelFormat = PixFmtMJPEG
			case vidiocSFmt:
				(*format)(argument).Pix.SizeImage = uint32(len(backend.mapped))
			case vidiocReqbufs:
				(*requestbuffers)(argument).Count = 1
			case vidiocQuerybuf:
				(*buffer)(argument).Length = uint32(len(backend.mapped))
			case vidiocQbuf:
				backend.mu.Lock()
				backend.queued = append(backend.queued, *(*buffer)(argument))
				backend.events = append(backend.events, "qbuf")
				backend.mu.Unlock()
			case vidiocDqbuf:
				var token [1]byte
				n, err := syscall.Read(int(fd), token[:])
				if errors.Is(err, syscall.EAGAIN) {
					select {
					case backend.waiting <- struct{}{}:
					default:
					}
				}
				if err != nil {
					return err
				}
				if n != 1 {
					return syscall.ENODEV
				}
				descriptor := (*buffer)(argument)
				descriptor.BytesUsed = uint32(len(backend.mapped))
				descriptor.Timestamp = syscall.Timeval{Sec: 1, Usec: 2}
				descriptor.Flags = bufFlagTimestampMonotonic
				descriptor.Sequence = 9
				backend.mu.Lock()
				after := backend.afterDQ
				backend.mu.Unlock()
				if after != nil {
					after()
				}
			case vidiocStreamon:
				backend.event("streamon")
			case vidiocStreamoff:
				backend.event("streamoff")
			default:
				t.Errorf("unexpected ioctl %#x", request)
			}
			return nil
		},
		mmap: func(_ int, offset int64, length, protection, flags int) ([]byte, error) {
			if offset != 0 || length != len(backend.mapped) || protection != syscall.PROT_READ|syscall.PROT_WRITE || flags != syscall.MAP_SHARED {
				t.Errorf("mmap arguments = %d/%d/%#x/%#x", offset, length, protection, flags)
			}
			backend.event("mmap")
			return backend.mapped, nil
		},
		munmap: func(mapped []byte) error {
			if len(mapped) != len(backend.mapped) || &mapped[0] != &backend.mapped[0] {
				t.Error("unmapped a different buffer")
			}
			backend.event("unmap")
			return nil
		},
	})
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := device.Close(); err != nil {
			t.Error(err)
		}
		_ = writer.Close()
	})
	return device, backend
}

// Timers bound broken tests; they do not assert a kernel-operation deadline.
func sysTestReceive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("pipe-backed V4L2 test did not complete")
		var zero T
		return zero
	}
}

type sysTestFrameResult struct {
	frame Frame
	err   error
}

func sysTestRead(device *Device, ctx context.Context) <-chan sysTestFrameResult {
	result := make(chan sysTestFrameResult, 1)
	go func() {
		frame, err := device.ReadFrame(ctx)
		result <- sysTestFrameResult{frame, err}
	}()
	return result
}

func TestPipeReadCancellationAndDeadlineReset(t *testing.T) {
	device, backend := sysTestOpenPipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	first := sysTestRead(device, ctx)
	sysTestReceive(t, backend.waiting)
	cancel()
	if result := sysTestReceive(t, first); !errors.Is(result.err, context.Canceled) || result.frame.Data != nil {
		t.Fatalf("canceled read = %+v", result)
	}
	second := sysTestRead(device, context.Background())
	sysTestReceive(t, backend.waiting)
	if _, err := backend.writer.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	result := sysTestReceive(t, second)
	if result.err != nil || !bytes.Equal(result.frame.Data, backend.mapped) || result.frame.Sequence != 9 {
		t.Fatalf("resumed read = %+v", result)
	}
	_, queued := backend.snapshot()
	if len(queued) != 2 {
		t.Fatalf("queued %d buffers; want initial queue and successful-read requeue", len(queued))
	}
}

func TestPipeCloseWakesBlockedReadAndCleansInOrder(t *testing.T) {
	device, backend := sysTestOpenPipe(t)
	read := sysTestRead(device, context.Background())
	sysTestReceive(t, backend.waiting)
	closed := make(chan error, 1)
	go func() { closed <- device.Close() }()
	if result := sysTestReceive(t, read); !errors.Is(result.err, ErrClosed) {
		t.Fatalf("closed read = %+v", result)
	}
	if err := sysTestReceive(t, closed); err != nil {
		t.Fatal(err)
	}
	events, queued := backend.snapshot()
	want := []string{"mmap", "qbuf", "streamon", "streamoff", "unmap", "close"}
	if len(events) != len(want) {
		t.Fatalf("cleanup events = %v", events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("cleanup events = %v; want %v", events, want)
		}
	}
	if len(queued) != 1 {
		t.Fatalf("blocked reader requeued an unowned buffer: %+v", queued)
	}
	if _, err := device.ReadFrame(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after Close = %v", err)
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	if again, _ := backend.snapshot(); len(again) != len(events) {
		t.Fatalf("repeated Close repeated cleanup: %v", again)
	}
}

func TestPipeCancellationAfterDequeueRequeuesOwnedBuffer(t *testing.T) {
	device, backend := sysTestOpenPipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend.mu.Lock()
	backend.afterDQ = cancel
	backend.mu.Unlock()
	read := sysTestRead(device, ctx)
	sysTestReceive(t, backend.waiting)
	if _, err := backend.writer.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if result := sysTestReceive(t, read); !errors.Is(result.err, context.Canceled) || result.frame.Data != nil {
		t.Fatalf("canceled owned-buffer read = %+v", result)
	}
	_, queued := backend.snapshot()
	want := buffer{Type: bufTypeVideoCapture, Memory: memoryMmap}
	if len(queued) != 2 || queued[1] != want {
		t.Fatalf("requeue must use a clean descriptor despite canceled ctx: %+v", queued)
	}
}

func TestPipeReadJoinsCancellationWakeBeforeNextDeadlineReset(t *testing.T) {
	device, backend := sysTestOpenPipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := sysTestRead(device, ctx)
	sysTestReceive(t, backend.waiting)
	wakeStarted, finishWake, nextReset := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(finishWake) })
	t.Cleanup(finish)
	var wake, reset sync.Once
	backend.file.setDeadlineHook(func(deadline time.Time) error {
		err := backend.file.File.SetReadDeadline(deadline)
		if deadline.IsZero() {
			reset.Do(func() { close(nextReset) })
		} else {
			wake.Do(func() {
				close(wakeStarted)
				<-finishWake
			})
		}
		return err
	})
	cancel()
	sysTestReceive(t, wakeStarted)
	second := sysTestRead(device, context.Background())
	select {
	case <-first:
		t.Fatal("read returned before its deadline callback finished")
	case <-nextReset:
		t.Fatal("next read reset the deadline before the old callback finished")
	case <-time.After(20 * time.Millisecond):
	}
	finish()
	if result := sysTestReceive(t, first); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("first read = %+v", result)
	}
	sysTestReceive(t, nextReset)
	if _, err := backend.writer.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if result := sysTestReceive(t, second); result.err != nil || !bytes.Equal(result.frame.Data, backend.mapped) {
		t.Fatalf("second read = %+v", result)
	}
}
