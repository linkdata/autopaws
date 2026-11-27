//go:build darwin && (amd64 || arm64)

package serial

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/linkdata/autopaws/ch9329"
)

func darwinPTY(t *testing.T) (*os.File, string) {
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
	var name [128]byte
	var callErr error
	if err := rc.Control(func(fd uintptr) {
		for _, request := range []uint{syscall.TIOCPTYGRANT, syscall.TIOCPTYUNLK, syscall.TIOCPTYGNAME} {
			if callErr = ioctl(fd, request, unsafe.Pointer(&name[0])); callErr != nil {
				return
			}
		}
	}); err != nil || callErr != nil {
		t.Fatalf("PTY setup: %v, %v", err, callErr)
	}
	return master, strings.TrimRight(string(name[:]), "\x00")
}

func expectDarwinPTY(t *testing.T, master *os.File, want []byte) func() {
	t.Helper()
	if err := master.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(master, got); done <- err }()
	return func() {
		t.Helper()
		if err := <-done; err != nil || !bytes.Equal(got, want) {
			t.Fatalf("PTY bytes = % x, %v; want % x", got, err, want)
		}
	}
}

func TestDarwinPTYRawSpeedsResetAndLock(t *testing.T) {
	for _, baud := range []int{1200, 1800, 2400, 4800, 9600, 19200, 38400, 57600, 115200} {
		t.Run(fmt.Sprint(baud), func(t *testing.T) {
			master, path := darwinPTY(t)
			port, err := New(Options{Path: path, Baud: baud})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = port.Close() })
			reset := ch9329.ReleaseAll()
			checkReset := expectDarwinPTY(t, master, append(reset[0], reset[1]...))
			if generation, err := port.Prepare(context.Background()); err != nil || generation != 1 {
				t.Fatalf("Prepare = %d, %v", generation, err)
			}
			checkReset()
			var termios syscall.Termios
			if err := port.dev.(*ttyDevice).ioctl(context.Background(), syscall.TIOCGETA, unsafe.Pointer(&termios)); err != nil {
				t.Fatal(err)
			}
			if termios.Ispeed != uint64(baud) || termios.Ospeed != uint64(baud) ||
				termios.Cflag&syscall.CSIZE != syscall.CS8 ||
				termios.Cflag&(syscall.PARENB|syscall.CSTOPB|darwinFlowControl) != 0 ||
				termios.Lflag&(syscall.ECHO|syscall.ICANON|syscall.ISIG) != 0 ||
				termios.Oflag&syscall.OPOST != 0 || termios.Iflag&(syscall.IXON|syscall.IXOFF|syscall.ICRNL) != 0 {
				t.Fatalf("raw settings = %+v", termios)
			}
			competitor, err := New(Options{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = competitor.Close() })
			if _, err := competitor.Prepare(context.Background()); !errors.Is(err, ErrBusy) {
				t.Fatalf("competing prepare = %v", err)
			}
			frame := []byte{0x0d, 0x0a, 0x11, 0x13, 0x03, 0xff}
			checkFrame := expectDarwinPTY(t, master, frame)
			if err := port.WriteFrame(context.Background(), frame); err != nil {
				t.Fatal(err)
			}
			checkFrame()
		})
	}
}

func TestDarwinPTYBlockedWriteDeadline(t *testing.T) {
	_, path := darwinPTY(t)
	opened, err := openDevice(context.Background(), path, syscall.B115200)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if err := opened.SetWriteDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.Write(make([]byte, 1<<20)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("blocked write = %v", err)
	}
}

func TestDarwinPTYReadsDeadlineAndInputDrain(t *testing.T) {
	master, path := darwinPTY(t)
	testTTYReads(t, master, path)
}
