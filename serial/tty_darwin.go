//go:build darwin && (amd64 || arm64)

package serial

import (
	"context"
	"syscall"
	"unsafe"
)

// Darwin termios stores baud rates in separate speed fields. The flow-control
// mask includes CTS/RTS, DTR/DSR and carrier flow control from sys/termios.h.
const (
	ttyGetAttr        = syscall.TIOCGETA
	ttySetAttr        = syscall.TIOCSETA
	darwinFlowControl = 0x001f0000
)

var baudRates = map[int]uint32{
	1200: syscall.B1200, 1800: syscall.B1800, 2400: syscall.B2400,
	4800: syscall.B4800, 9600: syscall.B9600, 19200: syscall.B19200,
	38400: syscall.B38400, 57600: syscall.B57600, 115200: syscall.B115200,
}

func makeRaw(t *syscall.Termios, speed uint32) {
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON | syscall.IXOFF |
		syscall.IXANY | syscall.INPCK
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB | syscall.CSTOPB | darwinFlowControl
	t.Cflag |= syscall.CS8 | syscall.CLOCAL | syscall.CREAD
	t.Ispeed, t.Ospeed = uint64(speed), uint64(speed)
	t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 1, 0
}

func (d *ttyDevice) flushInput(ctx context.Context) error {
	// TIOCFLUSH takes a pointer to FREAD, unlike Linux's immediate TCIFLUSH.
	input := int32(1)
	return d.ioctl(ctx, syscall.TIOCFLUSH, unsafe.Pointer(&input))
}
