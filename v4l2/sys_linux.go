//go:build linux && (amd64 || arm64)

package v4l2

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
	"unsafe"
)

type file interface {
	SyscallConn() (syscall.RawConn, error)
	SetReadDeadline(time.Time) error
	Close() error
}

// Operations are local to one Open, so tests can substitute ioctls/mappings
// without changing process-global syscall behavior or opening capture hardware.
type operations struct {
	open   func(string, int) (file, error)
	ioctl  func(uintptr, uintptr, unsafe.Pointer) error
	mmap   func(int, int64, int, int, int) ([]byte, error)
	munmap func([]byte) error
}

func systemOperations() operations {
	return operations{
		open: func(path string, flags int) (file, error) {
			fd, err := syscall.Open(path, flags|syscall.O_CLOEXEC, 0)
			if err != nil {
				return nil, &os.PathError{Op: "open", Path: path, Err: err}
			}
			return &streamFile{fd: fd, path: path}, nil
		},
		ioctl: ioctl, mmap: syscall.Mmap, munmap: syscall.Munmap,
	}
}

// Before STREAMON, V4L2 poll can return EPOLLERR. Registering that fd early with
// Go's netpoller can cache "not pollable" and reject RawConn.Read before DQBUF.
// Keep one sole-owned raw fd during unpublished, synchronous setup and transfer
// it to os.File only after the stream is running. No numeric fd is closed behind
// a published os.File or used after its ownership transfers.
type streamFile struct {
	fd   int
	path string
	file *os.File
}

func (file *streamFile) SyscallConn() (syscall.RawConn, error) {
	if file.file != nil {
		return file.file.SyscallConn()
	}
	return setupRaw{file}, nil
}

func (file *streamFile) activate() (syscall.RawConn, error) {
	file.file = os.NewFile(uintptr(file.fd), file.path)
	file.fd = -1 // os.File now exclusively owns the descriptor.
	return file.file.SyscallConn()
}

func (file *streamFile) SetReadDeadline(deadline time.Time) error {
	if file.file == nil {
		return nil // Setup/Probe has no parked read to wake.
	}
	return file.file.SetReadDeadline(deadline)
}

func (file *streamFile) Close() error {
	if file.file != nil {
		return file.file.Close()
	}
	err := syscall.Close(file.fd)
	file.fd = -1
	return err
}

type setupRaw struct{ file *streamFile }

func (raw setupRaw) Control(call func(uintptr)) error {
	call(uintptr(raw.file.fd))
	return nil
}

func (setupRaw) Read(func(uintptr) bool) error  { return os.ErrNoDeadline }
func (setupRaw) Write(func(uintptr) bool) error { return os.ErrNoDeadline }

func ioctl(fd, request uintptr, arg unsafe.Pointer) error {
	// Convert only in the syscall call: passing a uintptr through another Go
	// wrapper would lose the syscall's pointer-liveness/stack-movement guarantee.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

func (device *Device) control(ctx context.Context, request uintptr, arg unsafe.Pointer) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var ioctlErr error
		controlErr := device.raw.Control(func(fd uintptr) { ioctlErr = device.ops.ioctl(fd, request, arg) })
		if errors.Is(ioctlErr, syscall.EINTR) && controlErr == nil && ctx.Err() == nil {
			continue
		}
		return errors.Join(ctx.Err(), controlErr, ioctlErr)
	}
}

func (device *Device) dequeue(ctx context.Context) (buffer, bool, error) {
	descriptor := buffer{Type: bufTypeVideoCapture, Memory: memoryMmap}
	var ioctlErr error
	var owned bool
	readErr := device.raw.Read(func(fd uintptr) bool {
		for {
			if ioctlErr = device.check(ctx); ioctlErr != nil {
				return true
			}
			ioctlErr = device.ops.ioctl(fd, vidiocDqbuf, unsafe.Pointer(&descriptor))
			if errors.Is(ioctlErr, syscall.EINTR) {
				continue
			}
			owned = ioctlErr == nil
			return !errors.Is(ioctlErr, syscall.EAGAIN)
		}
	})
	if ioctlErr != nil && !errors.Is(ioctlErr, syscall.EAGAIN) &&
		!errors.Is(ioctlErr, context.Canceled) && !errors.Is(ioctlErr, context.DeadlineExceeded) && !errors.Is(ioctlErr, ErrClosed) {
		device.readErr = operationError("DQBUF", device.path, ioctlErr)
	}
	return descriptor, owned, errors.Join(device.check(ctx), readErr, ioctlErr)
}
