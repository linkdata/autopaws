//go:build linux && (amd64 || arm64)

package serial

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Linux asm-generic constants, identical on the supported amd64/arm64 ABIs.
const (
	tcflsh  = 0x540b
	crtscts = 0x80000000
	cbaud   = 0x100f
)

var baudRates = map[int]uint32{
	1200: syscall.B1200, 1800: syscall.B1800, 2400: syscall.B2400,
	4800: syscall.B4800, 9600: syscall.B9600, 19200: syscall.B19200,
	38400: syscall.B38400, 57600: syscall.B57600, 115200: syscall.B115200,
}

type ttyDevice struct {
	f  *os.File
	rc syscall.RawConn
}

func openDevice(ctx context.Context, path string, speed uint32) (device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	d := &ttyDevice{f: f, rc: rc}
	if err := d.configure(ctx, speed); err != nil {
		return nil, errors.Join(err, d.Close())
	}
	return d, nil
}

func (d *ttyDevice) configure(ctx context.Context, speed uint32) error {
	if err := d.control(ctx, func(fd uintptr) error {
		return syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
	}); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return errors.Join(ErrBusy, err)
		}
		return err
	}
	var t syscall.Termios
	if err := d.ioctl(ctx, syscall.TCGETS, unsafe.Pointer(&t)); err != nil {
		return err
	}
	makeRaw(&t, speed)
	if err := d.ioctl(ctx, syscall.TCSETS, unsafe.Pointer(&t)); err != nil {
		return err
	}
	if err := d.ioctl(ctx, syscall.TCGETS, unsafe.Pointer(&t)); err != nil {
		return err
	}
	want := t
	makeRaw(&want, speed)
	if t != want {
		return ErrSettings
	}
	return d.ioctl(ctx, tcflsh, nil) // TCIFLUSH is the immediate integer value zero.
}

func makeRaw(t *syscall.Termios, speed uint32) {
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON | syscall.IXOFF |
		syscall.IXANY | syscall.INPCK
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB | syscall.CSTOPB | crtscts | cbaud
	t.Cflag |= syscall.CS8 | syscall.CLOCAL | syscall.CREAD | speed
	t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 1, 0
}

func (d *ttyDevice) Write(b []byte) (int, error)        { return d.f.Write(b) }
func (d *ttyDevice) SetWriteDeadline(t time.Time) error { return d.f.SetWriteDeadline(t) }
func (d *ttyDevice) Close() error                       { return d.f.Close() }

func (d *ttyDevice) queued(ctx context.Context) (int, error) {
	var n int32
	err := d.ioctl(ctx, syscall.TIOCOUTQ, unsafe.Pointer(&n))
	if err == nil && n < 0 {
		err = fmt.Errorf("serial: invalid output queue length %d", n)
	}
	return int(n), err
}

func (d *ttyDevice) ioctl(ctx context.Context, request uint, arg unsafe.Pointer) error {
	return d.control(ctx, func(fd uintptr) error { return ioctl(fd, request, arg) })
}

func (d *ttyDevice) control(ctx context.Context, call func(uintptr) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		cerr := d.rc.Control(func(fd uintptr) { err = call(fd) })
		err = errors.Join(ctx.Err(), cerr, err)
		if !errors.Is(err, syscall.EINTR) || ctx.Err() != nil {
			return err
		}
	}
}

func ioctl(fd uintptr, request uint, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(request), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}
