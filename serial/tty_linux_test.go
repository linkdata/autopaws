//go:build linux && (amd64 || arm64)

package serial

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/linkdata/autopaws/ch9329"
)

func newPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	rc, err := master.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var number uint32
	var unlock int32
	var ioctlErr error
	if err := rc.Control(func(fd uintptr) {
		ioctlErr = ioctl(fd, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock))
		if ioctlErr == nil {
			ioctlErr = ioctl(fd, syscall.TIOCGPTN, unsafe.Pointer(&number))
		}
	}); err != nil || ioctlErr != nil {
		t.Fatalf("PTY ioctls: %v, %v", err, ioctlErr)
	}
	return master, fmt.Sprintf("/dev/pts/%d", number)
}

func readPTY(t *testing.T, master *os.File, want []byte) {
	t.Helper()
	if err := master.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(master, got); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("PTY received % x, %v; want % x", got, err, want)
	}
}

func realPort(t *testing.T, path string, baud int) *Port {
	t.Helper()
	p, err := New(Options{Path: path, Baud: baud})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return p
}

func readTermios(t *testing.T, d *ttyDevice) syscall.Termios {
	t.Helper()
	var termios syscall.Termios
	if err := d.ioctl(context.Background(), syscall.TCGETS, unsafe.Pointer(&termios)); err != nil {
		t.Fatal(err)
	}
	return termios
}

func readReset(t *testing.T, master *os.File) {
	t.Helper()
	reset := ch9329.ReleaseAll()
	readPTY(t, master, append(reset[0], reset[1]...))
}

func TestPTYRawSpeedsAndBytes(t *testing.T) {
	for _, baud := range []int{1200, 1800, 2400, 4800, 9600, 19200, 38400, 57600, 115200} {
		t.Run(fmt.Sprint(baud), func(t *testing.T) {
			master, path := newPTY(t)
			p := realPort(t, path, baud)
			if generation, err := p.Prepare(context.Background()); err != nil || generation != 1 {
				t.Fatalf("Prepare = %d, %v", generation, err)
			}
			readReset(t, master)
			termios := readTermios(t, p.dev.(*ttyDevice))
			if termios.Cflag&cbaud != baudRates[baud] || termios.Cflag&syscall.CSIZE != syscall.CS8 ||
				termios.Cflag&(syscall.PARENB|syscall.CSTOPB|crtscts) != 0 ||
				termios.Cflag&(syscall.CLOCAL|syscall.CREAD) != syscall.CLOCAL|syscall.CREAD ||
				termios.Lflag&(syscall.ICANON|syscall.ECHO|syscall.ECHONL|syscall.ISIG|syscall.IEXTEN) != 0 ||
				termios.Oflag&syscall.OPOST != 0 ||
				termios.Iflag&(syscall.IGNBRK|syscall.BRKINT|syscall.PARMRK|syscall.ISTRIP|
					syscall.INLCR|syscall.IGNCR|syscall.ICRNL|syscall.IXON|syscall.IXOFF|syscall.IXANY|syscall.INPCK) != 0 ||
				termios.Cc[syscall.VMIN] != 1 || termios.Cc[syscall.VTIME] != 0 {
				t.Fatalf("raw modes not applied: %+v", termios)
			}
			frame := append(ch9329.Keyboard(0, 4), 0x0d, 0x0a, 0x11, 0x13, 0x03)
			if err := p.WriteFrame(context.Background(), frame); err != nil {
				t.Fatal(err)
			}
			readPTY(t, master, frame)
		})
	}
}

func TestPTYReadsDeadlineAndInputDrain(t *testing.T) {
	master, path := newPTY(t)
	testTTYReads(t, master, path)
}

func TestPTYBusyDoesNotChangeOrFlushFirstPort(t *testing.T) {
	master, path := newPTY(t)
	first := realPort(t, path, 9600)
	if _, err := first.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	readReset(t, master)
	d := first.dev.(*ttyDevice)
	before := readTermios(t, d)
	// These bytes must survive a competing opener's failure. In particular,
	// it must not flush input before discovering that it cannot own the tty.
	pending := []byte{0x57, 0xab, 0x00, 0x82, 0x01, 0x00, 0x85}
	if _, err := master.Write(pending); err != nil {
		t.Fatal(err)
	}
	second := realPort(t, path, 115200)
	if generation, err := second.Prepare(context.Background()); generation != 0 || !errors.Is(err, ErrBusy) {
		t.Fatalf("competing Prepare = %d, %v", generation, err)
	}
	if after := readTermios(t, d); after != before {
		t.Fatalf("busy opener changed modes: before %+v; after %+v", before, after)
	}
	if err := d.f.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(pending))
	if _, err := io.ReadFull(d.f, got); err != nil || !bytes.Equal(got, pending) {
		t.Fatalf("busy opener flushed pending input: % x, %v", got, err)
	}
	if err := first.WriteFrame(context.Background(), []byte{0x0d, 0x0a, 0x11, 0x13, 0x03}); err != nil {
		t.Fatal(err)
	}
	readPTY(t, master, []byte{0x0d, 0x0a, 0x11, 0x13, 0x03})
}

type notifyWrite struct {
	device
	started chan struct{}
}

func (d *notifyWrite) Write(b []byte) (int, error) {
	close(d.started)
	return d.device.Write(b)
}

func TestPTYCancelAndClosePendingWrite(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(fmt.Sprint("close=", closing), func(t *testing.T) {
			master, path := newPTY(t)
			p := realPort(t, path, 115200)
			if _, err := p.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			readReset(t, master)
			started := make(chan struct{})
			p.dev = &notifyWrite{device: p.dev, started: started}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- p.WriteFrame(ctx, make([]byte, 1<<20)) }()
			<-started
			want := error(context.Canceled)
			if closing {
				want = os.ErrClosed
				if err := p.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("write = %v; want %v", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("PTY writer did not leave its netpoll wait")
			}
		})
	}
}

func TestOpenDeviceErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := openDevice(ctx, "/no/open/attempt", syscall.B9600); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open: %v", err)
	}
	if _, err := openDevice(context.Background(), filepath.Join(t.TempDir(), "missing"), syscall.B9600); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing device: %v", err)
	}
	path := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openDevice(context.Background(), path, syscall.B9600); !errors.Is(err, syscall.ENOTTY) {
		t.Fatalf("non-tty device: %v", err)
	}
}

type rawControl func(func(uintptr)) error

func (f rawControl) Control(call func(uintptr)) error { return f(call) }
func (rawControl) Read(func(uintptr) bool) error      { panic("unused Read") }
func (rawControl) Write(func(uintptr) bool) error     { panic("unused Write") }

func TestControlRetriesAndCancellation(t *testing.T) {
	calls := 0
	d := &ttyDevice{rc: rawControl(func(call func(uintptr)) error { call(0); return nil })}
	if err := d.control(context.Background(), func(uintptr) error {
		calls++
		if calls < 3 {
			return syscall.EINTR
		}
		return nil
	}); err != nil || calls != 3 {
		t.Fatalf("retry = %v, calls %d", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	err := d.control(ctx, func(uintptr) error { calls++; cancel(); return syscall.EINTR })
	if !errors.Is(err, context.Canceled) || !errors.Is(err, syscall.EINTR) || calls != 1 {
		t.Fatalf("cancelled retry = %v, calls %d", err, calls)
	}
	if err := d.control(ctx, func(uintptr) error { t.Fatal("called after cancellation"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled control = %v", err)
	}
	d.rc = rawControl(func(func(uintptr)) error { return os.ErrClosed })
	if err := d.control(context.Background(), func(uintptr) error { t.Fatal("closed callback"); return nil }); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed control = %v", err)
	}
}

func TestPTYDetectsUnappliedSettings(t *testing.T) {
	master, path := newPTY(t)
	_ = master
	opened, err := openDevice(context.Background(), path, syscall.B9600)
	if err != nil {
		t.Fatal(err)
	}
	d := opened.(*ttyDevice)
	t.Cleanup(func() { _ = d.Close() })
	original := d.rc
	calls := 0
	d.rc = rawControl(func(call func(uintptr)) error {
		calls++
		return original.Control(func(fd uintptr) {
			if calls == 4 { // emulate a driver changing the requested modes
				var termios syscall.Termios
				if err := ioctl(fd, syscall.TCGETS, unsafe.Pointer(&termios)); err != nil {
					t.Fatal(err)
				}
				termios.Lflag |= syscall.ECHO
				if err := ioctl(fd, syscall.TCSETS, unsafe.Pointer(&termios)); err != nil {
					t.Fatal(err)
				}
			}
			call(fd)
		})
	})
	if err := d.configure(context.Background(), syscall.B9600); !errors.Is(err, ErrSettings) {
		t.Fatalf("unapplied settings: %v", err)
	}
}
