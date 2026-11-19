//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"syscall"
	"time"
	"unsafe"

	"github.com/linkdata/autopaws/internal/monotime"
)

type kernel struct {
	open        func(string, int) (int, error)
	setNonblock func(int, bool) error
	ioctl       func(int, uintptr, unsafe.Pointer) error
	close       func(int) error
	now         func() (int64, error)
}

func systemKernel() kernel {
	return kernel{
		open:        func(path string, flags int) (int, error) { return syscall.Open(path, flags, 0) },
		setNonblock: syscall.SetNonblock, ioctl: ioctl, close: syscall.Close, now: monotime.Now,
	}
}

func ioctl(fd int, request uintptr, argument unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(argument))
	if errno != 0 {
		return errno
	}
	return nil
}

func timestampNS(timestamp syscall.Timespec) (int64, error) {
	if timestamp.Sec < 0 || timestamp.Nsec < 0 || timestamp.Nsec >= int64(time.Second) || timestamp.Sec > (math.MaxInt64-timestamp.Nsec)/int64(time.Second) {
		return 0, ErrTimestamp
	}
	return timestamp.Sec*int64(time.Second) + timestamp.Nsec, nil
}

func framesNS(frames uint64, rate uint32) (int64, error) {
	if rate == 0 {
		return 0, ErrTimestamp
	}
	hi, lo := bits.Mul64(frames, uint64(time.Second))
	if hi >= uint64(rate) {
		return 0, ErrTimestamp
	}
	nanoseconds, _ := bits.Div64(hi, lo, uint64(rate))
	if nanoseconds > math.MaxInt64 {
		return 0, ErrTimestamp
	}
	return int64(nanoseconds), nil
}

func firstSampleNS(snapshot status, frames uint32, config Config) (int64, error) {
	if snapshot.Delay < 0 || snapshot.Available > uint64(config.BufferFrames) {
		return 0, ErrTimestamp
	}
	timestamp, err := timestampNS(snapshot.Timestamp)
	if err != nil {
		return 0, err
	}
	queued, err := framesNS(uint64(snapshot.Delay)+uint64(frames), config.Rate)
	if err != nil || queued > timestamp {
		return 0, ErrTimestamp
	}
	return timestamp - queued, nil
}

func kernelError(operation, path string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EBUSY) {
		err = errors.Join(ErrBusy, err)
	}
	if errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENXIO) {
		err = errors.Join(ErrDisconnected, err)
	}
	return fmt.Errorf("alsa %s %s: %w", operation, path, err)
}

func call(ctx context.Context, ops kernel, fd int, request uintptr, argument unsafe.Pointer) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := ops.ioctl(fd, request, argument)
		if errors.Is(err, syscall.EINTR) && ctx.Err() == nil {
			continue
		}
		return errors.Join(ctx.Err(), err)
	}
}
