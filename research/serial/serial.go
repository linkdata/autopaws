// Package serial is a stdlib-only Linux raw serial port prototype.
package serial

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// Not exported by package syscall on every linux arch (values are asm-generic,
// identical on amd64 and arm64).
const (
	tcflsh   = 0x540B
	tcsbrk   = 0x5409
	crtscts  = 0x80000000
	cbaud    = 0x100F
	tciflush = 0
)

// ErrBaud is returned for an unsupported baud rate.
var ErrBaud = errors.New("unsupported baud rate")

var bauds = map[int]uint32{9600: syscall.B9600, 19200: syscall.B19200, 38400: syscall.B38400, 57600: syscall.B57600, 115200: syscall.B115200}

func ioctl(fd uintptr, req uint, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(req), arg); e != 0 {
		return e
	}
	return nil
}

// MakeRaw puts t into raw 8N1 mode at the given speed constant (cfmakeraw + CLOCAL|CREAD, no flow control).
func MakeRaw(t *syscall.Termios, speed uint32) {
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON | syscall.IXOFF | syscall.IXANY | syscall.INPCK
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB | syscall.CSTOPB | crtscts | cbaud
	t.Cflag |= syscall.CS8 | syscall.CLOCAL | syscall.CREAD | speed
	t.Ispeed, t.Ospeed = speed, speed
	t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 1, 0 // irrelevant: fd is nonblocking, timeouts via deadlines
}

// SetRaw configures f as raw 8N1 at baud and flushes pending input.
func SetRaw(f *os.File, baud int) (err error) {
	speed, ok := bauds[baud]
	if !ok {
		return ErrBaud
	}
	var rc syscall.RawConn
	if rc, err = f.SyscallConn(); err == nil {
		cerr := rc.Control(func(fd uintptr) {
			var t syscall.Termios
			if err = ioctl(fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t))); err == nil {
				MakeRaw(&t, speed)
				if err = ioctl(fd, syscall.TCSETS, uintptr(unsafe.Pointer(&t))); err == nil {
					err = ioctl(fd, tcflsh, tciflush)
				}
			}
		})
		err = errors.Join(err, cerr)
	}
	return
}

// Open opens a tty as raw 8N1 at baud. The returned file is registered with
// the Go netpoller, so SetReadDeadline works and Close unblocks a pending Read.
func Open(path string, baud int) (f *os.File, err error) {
	if f, err = os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0); err == nil {
		if err = SetRaw(f, baud); err == nil {
			return
		}
		_ = f.Close()
		f = nil
	}
	return
}

// Drain blocks until all written output has been transmitted (tcdrain, like pyserial flush()).
func Drain(f *os.File) (err error) {
	var rc syscall.RawConn
	if rc, err = f.SyscallConn(); err == nil {
		cerr := rc.Control(func(fd uintptr) { err = ioctl(fd, tcsbrk, 1) })
		err = errors.Join(err, cerr)
	}
	return
}

// OpenPTY returns a master/slave pseudo-terminal pair.
func OpenPTY() (master, slave *os.File, err error) {
	if master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0); err == nil {
		var rc syscall.RawConn
		var n uint32
		if rc, err = master.SyscallConn(); err == nil {
			var unlock int32
			cerr := rc.Control(func(fd uintptr) {
				if err = ioctl(fd, syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err == nil {
					err = ioctl(fd, syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n)))
				}
			})
			if err = errors.Join(err, cerr); err == nil {
				if slave, err = os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|syscall.O_NOCTTY, 0); err == nil {
					return
				}
			}
		}
		_ = master.Close()
		master = nil
	}
	return
}

// CH340IDs are the VID:PID pairs (lowercase hex, as in sysfs) for CH340/CH341.
var CH340IDs = map[string]bool{"1a86:7523": true, "1a86:7522": true}

// FindCH340 returns /dev paths of ttys whose USB device matches CH340IDs.
// sysRoot is "/sys" in production.
//
// Walk: /sys/class/tty/<name>/device is a symlink into /sys/devices; for a
// usb-serial tty it points at the port device (…/1-1:1.0/ttyUSB0), whose
// parent is the USB interface (1-1:1.0), whose parent is the USB device (1-1)
// holding idVendor/idProduct. Walking up to the first dir with idVendor also
// handles ttyACM (device -> interface directly).
func FindCH340(sysRoot string) (ports []string, err error) {
	var ents []os.DirEntry
	if ents, err = os.ReadDir(filepath.Join(sysRoot, "class/tty")); err == nil {
		devices := filepath.Join(sysRoot, "devices")
		for _, e := range ents {
			dev, rerr := filepath.EvalSymlinks(filepath.Join(sysRoot, "class/tty", e.Name(), "device"))
			if rerr != nil {
				continue // virtual tty, no device link
			}
			for d := dev; strings.HasPrefix(d, devices+"/"); d = filepath.Dir(d) {
				vid, verr := os.ReadFile(filepath.Join(d, "idVendor"))
				if verr != nil {
					continue
				}
				pid, _ := os.ReadFile(filepath.Join(d, "idProduct"))
				if CH340IDs[strings.TrimSpace(string(vid))+":"+strings.TrimSpace(string(pid))] {
					ports = append(ports, "/dev/"+e.Name())
				}
				break
			}
		}
	}
	return
}
