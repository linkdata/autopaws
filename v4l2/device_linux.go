//go:build linux && (amd64 || arm64)

package v4l2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Device owns one open capture stream. One producer should call ReadFrame;
// concurrent calls serialize, and cancellation can stop a queued caller. Close
// rejects new work, wakes a parked read and waits for copying/kernel ownership
// before STREAMOFF, unmapping and closing. It has no hard kernel-call deadline.
type Device struct {
	file file
	raw  syscall.RawConn
	ops  operations
	path string

	gate       chan struct{}
	lifetime   context.Context
	cancel     context.CancelFunc
	closeOnce  sync.Once
	closeErr   error
	format     Format
	formats    []PixelFormat
	buffers    [][]byte // gate protects buffers, stopStream and readErr
	stopStream bool
	readErr    error
}

// Open starts a capture stream without acquiring a controlling terminal.
//
// It opens path without discovery, verifies streaming capture and MJPG support,
// negotiates the size and maps four buffers (or the driver's actual count).
// ctx controls startup, not the returned stream lifetime.
func Open(ctx context.Context, path string, options Options) (*Device, error) {
	return open(ctx, path, options, systemOperations())
}

func open(ctx context.Context, path string, options Options, ops operations) (*Device, error) {
	if options.Width == 0 || options.Height == 0 {
		return nil, ErrOptions
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := ops.open(path, os.O_RDWR|syscall.O_NONBLOCK|syscall.O_NOCTTY)
	if err != nil {
		return nil, operationError("open", path, err)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	device := &Device{file: file, ops: ops, path: path, gate: make(chan struct{}, 1), lifetime: lifetime, cancel: cancel}
	device.gate <- struct{}{}
	device.raw, err = file.SyscallConn()
	if err == nil {
		err = device.setup(ctx, options)
	}
	if err != nil {
		return nil, operationError("setup", path, errors.Join(err, device.Close()))
	}
	return device, nil
}

func (device *Device) setup(ctx context.Context, options Options) error {
	info, err := device.queryCapabilities(ctx)
	if err != nil {
		return err
	}
	capabilities := info.effectiveCapabilities()
	if capabilities&(capVideoCapture|capStreaming) != capVideoCapture|capStreaming {
		return ErrNotCapture
	}
	device.formats, err = device.enumFormats(ctx)
	if err != nil {
		return err
	}
	var mjpg bool
	for _, format := range device.formats {
		mjpg = mjpg || format.FourCC == PixFmtMJPEG
	}
	if !mjpg {
		return ErrUnsupportedFormat
	}
	negotiated := format{Type: bufTypeVideoCapture}
	negotiated.Pix = pixFormat{Width: options.Width, Height: options.Height, PixelFormat: PixFmtMJPEG}
	if err := device.control(ctx, vidiocSFmt, unsafe.Pointer(&negotiated)); err != nil {
		return fmt.Errorf("S_FMT: %w", err)
	}
	if negotiated.Pix.PixelFormat != PixFmtMJPEG {
		return ErrUnsupportedFormat
	}
	if negotiated.Pix.Width == 0 || negotiated.Pix.Height == 0 {
		return fmt.Errorf("negotiated zero dimensions: %w", ErrOptions)
	}
	device.format = Format{negotiated.Pix.Width, negotiated.Pix.Height, negotiated.Pix.PixelFormat, negotiated.Pix.BytesPerLine, negotiated.Pix.SizeImage}
	requested := requestbuffers{Count: 4, Type: bufTypeVideoCapture, Memory: memoryMmap}
	if err := device.control(ctx, vidiocReqbufs, unsafe.Pointer(&requested)); err != nil {
		return fmt.Errorf("REQBUFS: %w", err)
	}
	if requested.Count == 0 || requested.Count > maxFrames {
		return fmt.Errorf("REQBUFS count %d: %w", requested.Count, ErrInvalidBuffer)
	}
	for index := uint32(0); index < requested.Count; index++ {
		descriptor := buffer{Index: index, Type: bufTypeVideoCapture, Memory: memoryMmap}
		if err := device.control(ctx, vidiocQuerybuf, unsafe.Pointer(&descriptor)); err != nil {
			return fmt.Errorf("QUERYBUF %d: %w", index, err)
		}
		if descriptor.Index != index || descriptor.Type != bufTypeVideoCapture || descriptor.Memory != memoryMmap ||
			descriptor.Length == 0 || uint64(descriptor.Offset)%uint64(os.Getpagesize()) != 0 {
			return fmt.Errorf("QUERYBUF %d: %w", index, ErrInvalidBuffer)
		}
		var mapped []byte
		var mapErr error
		if err := device.raw.Control(func(fd uintptr) {
			mapped, mapErr = device.ops.mmap(int(fd), int64(descriptor.Offset), int(descriptor.Length), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
		}); err != nil {
			mapErr = errors.Join(mapErr, err)
		}
		if len(mapped) != 0 {
			device.buffers = append(device.buffers, mapped)
		}
		if mapErr != nil {
			return fmt.Errorf("mmap %d: %w", index, mapErr)
		}
		if len(mapped) != int(descriptor.Length) {
			return fmt.Errorf("mmap %d length: %w", index, ErrInvalidBuffer)
		}
		device.stopStream = true // A failed QBUF/STREAMON may still own queues.
		if err := device.queue(index); err != nil {
			return fmt.Errorf("QBUF %d: %w", index, err)
		}
	}
	typeID := uint32(bufTypeVideoCapture)
	if err := device.control(ctx, vidiocStreamon, unsafe.Pointer(&typeID)); err != nil {
		return fmt.Errorf("STREAMON: %w", err)
	}
	// V4L2 can report EPOLLERR before streaming. Only now register the fd
	// with Go's runtime poller, after queueing and STREAMON have completed.
	if activator, ok := device.file.(interface {
		activate() (syscall.RawConn, error)
	}); ok {
		raw, err := activator.activate()
		if err != nil {
			return fmt.Errorf("activate netpoll: %w", err)
		}
		device.raw = raw
	}
	// Reject non-pollable files rather than pretending Close/cancellation can
	// wake a blocked RawConn.Read.
	if err := device.file.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("read deadlines unavailable: %w", err)
	}
	return ctx.Err()
}

func cString(value []byte) string {
	if end := bytes.IndexByte(value, 0); end >= 0 {
		value = value[:end]
	}
	return string(value)
}

// Format returns immutable negotiated settings and does not issue an ioctl.
func (device *Device) Format() Format { return device.format }

// Formats returns a copy of the pixel formats advertised during Open, in driver
// order. Enumeration uses ENUM_FMT only; sizes and frame rates are not listed.
func (device *Device) Formats() []PixelFormat {
	return append([]PixelFormat(nil), device.formats...)
}

func (device *Device) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if device.lifetime.Err() != nil {
		return ErrClosed
	}
	return nil
}

func (device *Device) acquire(ctx context.Context) error {
	if err := device.check(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-device.lifetime.Done():
		return ErrClosed
	case <-device.gate:
		if err := device.check(ctx); err != nil {
			device.release()
			return err
		}
		return nil
	}
}

func (device *Device) release() { device.gate <- struct{}{} }

// ReadFrame waits for one descriptor-valid MJPG frame, preserving its original
// compressed bytes. It does not decode JPEGs, apply warm-up sleeps or filter image
// content. A cancelled read accounts for any dequeued buffer before returning and
// may be retried; descriptor/ioctl failures are terminal until Close and reopen.
func (device *Device) ReadFrame(ctx context.Context) (frame Frame, err error) {
	if err = device.acquire(ctx); err != nil {
		return frame, err
	}
	defer device.release()
	if device.readErr != nil {
		return frame, device.readErr
	}
	if err = device.file.SetReadDeadline(time.Time{}); err != nil {
		device.readErr = operationError("reset read deadline", device.path, err)
		return frame, device.readErr
	}
	woken := make(chan struct{})
	var wakeErr error
	stop := context.AfterFunc(ctx, func() {
		wakeErr = device.file.SetReadDeadline(time.Unix(1, 0))
		close(woken)
	})
	defer func() {
		if !stop() {
			<-woken // An old callback must not overwrite the next read's deadline.
		}
		err = errors.Join(err, wakeErr)
	}()
	// Close can set its wakeup deadline just before the reset above. Checking
	// permanent intent afterwards prevents that reset from stranding a read.
	if err = device.check(ctx); err == nil {
		frame, err = device.read(ctx)
	}
	if err != nil && ctx.Err() == nil && device.lifetime.Err() == nil {
		device.readErr = operationError("read", device.path, err)
		err = device.readErr
	}
	return frame, err
}

func (device *Device) read(ctx context.Context) (Frame, error) {
	for {
		descriptor, owned, err := device.dequeue(ctx)
		if !owned {
			return Frame{}, err
		}
		if descriptor.Index >= uint32(len(device.buffers)) || descriptor.Type != bufTypeVideoCapture || descriptor.Memory != memoryMmap {
			device.readErr = operationError("dequeue descriptor", device.path, ErrInvalidBuffer)
			return Frame{}, errors.Join(err, ErrInvalidBuffer)
		}
		var frame Frame
		if descriptor.BytesUsed > uint32(len(device.buffers[descriptor.Index])) {
			device.readErr = operationError("dequeue bytesused", device.path, ErrInvalidBuffer)
			err = errors.Join(err, ErrInvalidBuffer)
		} else if descriptor.Flags&bufFlagError == 0 && descriptor.BytesUsed != 0 && err == nil {
			timestamp := Timestamp{descriptor.Timestamp.Sec, descriptor.Timestamp.Usec}
			if _, timestampErr := timestamp.Nanoseconds(); timestampErr != nil {
				device.readErr = operationError("dequeue timestamp", device.path, timestampErr)
				err = timestampErr
			} else {
				frame = Frame{bytes.Clone(device.buffers[descriptor.Index][:descriptor.BytesUsed]), timestamp, descriptor.Flags, descriptor.Sequence}
			}
		}
		// Once DQBUF succeeded, cancellation must not skip returning a known
		// owned buffer. The requeue uses a clean descriptor and an uncancelled
		// context, while the action gate still protects the mapping and fd.
		queueErr := device.queue(descriptor.Index)
		if queueErr != nil {
			device.readErr = operationError("requeue", device.path, queueErr)
		}
		err = errors.Join(err, queueErr, device.check(ctx))
		if err != nil {
			return Frame{}, err
		}
		if frame.Data != nil {
			return frame, nil
		}
	}
}

func (device *Device) queue(index uint32) error {
	descriptor := buffer{Index: index, Type: bufTypeVideoCapture, Memory: memoryMmap}
	return device.control(context.Background(), vidiocQbuf, unsafe.Pointer(&descriptor))
}

// Close is permanent and idempotent. It attempts every cleanup step and joins
// errors. A concurrent reader must finish any executing ioctl/copy before memory
// is unmapped. The wakeup deadline interrupts netpoll, not a blocked kernel ioctl.
func (device *Device) Close() error {
	device.closeOnce.Do(func() {
		device.cancel()
		wakeErr := device.file.SetReadDeadline(time.Unix(1, 0))
		<-device.gate
		defer device.release()
		device.closeErr = wakeErr
		if device.stopStream {
			typeID := uint32(bufTypeVideoCapture)
			device.closeErr = errors.Join(device.closeErr, device.control(context.Background(), vidiocStreamoff, unsafe.Pointer(&typeID)))
		}
		for _, mapped := range device.buffers {
			device.closeErr = errors.Join(device.closeErr, device.ops.munmap(mapped))
		}
		device.buffers = nil
		device.closeErr = operationError("close", device.path, errors.Join(device.closeErr, device.file.Close()))
	})
	return device.closeErr
}

func operationError(operation, path string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENXIO) {
		err = errors.Join(ErrDisconnected, err)
	}
	return fmt.Errorf("v4l2 %s %s: %w", operation, path, err)
}
