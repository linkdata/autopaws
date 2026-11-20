//go:build linux && (amd64 || arm64)

package serial

import (
	"context"
	"syscall"
)

// Linux asm-generic constants, identical on the supported amd64/arm64 ABIs.
const (
	ttyGetAttr = syscall.TCGETS
	ttySetAttr = syscall.TCSETS
	tcflsh     = 0x540b
	crtscts    = 0x80000000
	cbaud      = 0x100f
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
	t.Cflag &^= syscall.CSIZE | syscall.PARENB | syscall.CSTOPB | crtscts | cbaud
	t.Cflag |= syscall.CS8 | syscall.CLOCAL | syscall.CREAD | speed
	t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 1, 0
}

func (d *ttyDevice) flushInput(ctx context.Context) error {
	return d.ioctl(ctx, tcflsh, nil) // TCIFLUSH is the immediate integer value zero.
}
