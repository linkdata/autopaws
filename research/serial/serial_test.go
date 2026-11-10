package serial

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// CH9329 keyboard packet: 57 AB 00 02 08 <mod> 00 <k1..k6> <sum>; 'a' (0x04).
func ch9329Packet() []byte {
	p := []byte{0x57, 0xAB, 0x00, 0x02, 0x08, 0, 0, 0x04, 0, 0, 0, 0, 0}
	var s byte
	for _, b := range p {
		s += b
	}
	return append(p, s)
}

func TestPTY(t *testing.T) {
	for _, baud := range []int{9600, 115200} {
		m, s, err := OpenPTY()
		if err != nil {
			t.Fatal(err)
		}
		// Slave plays the "serial port"; master plays the CH9329. Both raw so
		// no echo/CR translation on 0x0D etc.
		if err = SetRaw(s, baud); err != nil {
			t.Fatal(err)
		}
		if err = SetRaw(m, baud); err != nil {
			t.Fatal(err)
		}
		var tio syscall.Termios
		rc, _ := s.SyscallConn()
		_ = rc.Control(func(fd uintptr) { err = ioctl(fd, syscall.TCGETS, uintptr(unsafe.Pointer(&tio))) })
		if err != nil || tio.Lflag&syscall.ICANON != 0 || tio.Cflag&syscall.CSIZE != syscall.CS8 || tio.Cflag&cbaud != bauds[baud] {
			t.Fatalf("termios not applied: %+v %v", tio, err)
		}
		t.Logf("baud %d: cflag=%#o ispeed=%#o", baud, tio.Cflag, tio.Ispeed)

		pkt := ch9329Packet()
		pkt = append(pkt, 0x0D, 0x0A, 0x11, 0x13, 0x03) // CR LF XON XOFF ^C must pass untouched
		if _, err = s.Write(pkt); err != nil {
			t.Fatal(err)
		}
		if err = Drain(s); err != nil {
			t.Fatal("drain:", err)
		}
		got := make([]byte, len(pkt))
		_ = m.SetReadDeadline(time.Now().Add(time.Second))
		if _, err = io.ReadFull(m, got); err != nil || !bytes.Equal(got, pkt) {
			t.Fatalf("got % x err %v want % x", got, err, pkt)
		}

		// Read timeout via netpoller deadline.
		start := time.Now()
		_ = s.SetReadDeadline(start.Add(200 * time.Millisecond))
		_, err = s.Read(got)
		if el := time.Since(start); !errors.Is(err, os.ErrDeadlineExceeded) || el < 190*time.Millisecond || el > time.Second {
			t.Fatalf("timeout: err %v after %v", err, el)
		}

		// Reverse direction (CH9329 ACK) and Close unblocking a pending Read.
		_ = s.SetReadDeadline(time.Time{})
		done := make(chan error, 1)
		go func() {
			b := make([]byte, 7)
			_, e := io.ReadFull(s, b)
			if e == nil && b[3] != 0x82 {
				e = errors.New("bad ack")
			}
			if e == nil {
				_, e = s.Read(b) // blocks until Close
			}
			done <- e
		}()
		_, _ = m.Write([]byte{0x57, 0xAB, 0x00, 0x82, 0x01, 0x00, 0x85})
		time.Sleep(50 * time.Millisecond)
		_ = s.Close()
		if e := <-done; !errors.Is(e, os.ErrClosed) {
			t.Fatalf("close did not unblock read: %v", e)
		}
		_ = m.Close()
	}
}

func TestFindCH340(t *testing.T) {
	root := t.TempDir()
	mk := func(p, c string) {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(name, target string) {
		_ = os.MkdirAll(filepath.Join(root, "class/tty", name), 0o755)
		if target != "" {
			_ = os.MkdirAll(target, 0o755)
			if err := os.Symlink(target, filepath.Join(root, "class/tty", name, "device")); err != nil {
				t.Fatal(err)
			}
		}
	}
	usb := filepath.Join(root, "devices/pci0000:00/0000:00:14.0/usb1")
	mk(usb+"/1-1/idVendor", "1a86\n")
	mk(usb+"/1-1/idProduct", "7523\n")
	link("ttyUSB0", usb+"/1-1/1-1:1.0/ttyUSB0")
	mk(usb+"/1-2/idVendor", "0403\n")
	mk(usb+"/1-2/idProduct", "6001\n")
	link("ttyUSB1", usb+"/1-2/1-2:1.0/ttyUSB1")
	link("tty0", "")
	link("ttyS0", filepath.Join(root, "devices/platform/serial8250/ttyS0"))
	got, err := FindCH340(root)
	if err != nil || !slices.Equal(got, []string{"/dev/ttyUSB0"}) {
		t.Fatalf("got %v %v", got, err)
	}
	if real, err := FindCH340("/sys"); err == nil {
		t.Logf("real /sys: %v", real)
	}
}
