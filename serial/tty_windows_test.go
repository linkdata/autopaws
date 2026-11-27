//go:build windows && (amd64 || arm64)

package serial

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/sys/windows"
)

func fakeCommAPI() commAPI {
	state := windows.DCB{BaudRate: 1200, Flags: 0x7fff, ByteSize: 7, Parity: 2, StopBits: 2, XonChar: 17, XoffChar: 19}
	var nextEvent windows.Handle = 10
	return commAPI{
		open: func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
			return 1, nil
		},
		getState: func(_ windows.Handle, target *windows.DCB) error { *target = state; return nil },
		setState: func(_ windows.Handle, source *windows.DCB) error { state = *source; return nil },
		timeouts: func(windows.Handle, *windows.CommTimeouts) error { return nil },
		purge:    func(windows.Handle, uint32) error { return nil },
		event: func(*windows.SecurityAttributes, uint32, uint32, *uint16) (windows.Handle, error) {
			nextEvent++
			return nextEvent, nil
		},
		signal: func(windows.Handle) error { return nil },
		close:  func(windows.Handle) error { return nil },
		read: func(_ windows.Handle, data []byte, count *uint32, _ *windows.Overlapped) error {
			*count = uint32(copy(data, []byte{0x57, 0xab}))
			return nil
		},
		write: func(_ windows.Handle, data []byte, count *uint32, _ *windows.Overlapped) error {
			*count = uint32(len(data))
			return nil
		},
		wait:   func([]windows.Handle, bool, uint32) (uint32, error) { return windows.WAIT_OBJECT_0, nil },
		result: func(windows.Handle, *windows.Overlapped, *uint32, bool) error { return nil },
		cancel: func(windows.Handle, *windows.Overlapped) error { return nil },
		clearErrors: func(windows.Handle, *uint32, *windows.ComStat) error {
			return nil
		},
	}
}

func TestWindowsCOMNames(t *testing.T) {
	for input, want := range map[string]string{"COM1": "COM1", "com12": "COM12", `\\.\COM109`: "COM109", "COM0012": "COM12"} {
		if got, err := comName(input); err != nil || got != want {
			t.Errorf("comName(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"", "COM", "COM0", "COM-1", "COM1/file", "COM1 ", "LPT1", "COM4294967296", "COM1\x00"} {
		if got, err := comName(input); err == nil {
			t.Errorf("invalid COM path %q accepted as %q", input, got)
		}
	}
}

func TestWindowsOpenAppliesExclusiveRawSettings(t *testing.T) {
	api := fakeCommAPI()
	api.open = func(name *uint16, access, share uint32, _ *windows.SecurityAttributes, creation, flags uint32, _ windows.Handle) (windows.Handle, error) {
		if got := windows.UTF16PtrToString(name); got != `\\.\COM12` || access != windows.GENERIC_READ|windows.GENERIC_WRITE ||
			share != 0 || creation != windows.OPEN_EXISTING || flags != windows.FILE_FLAG_OVERLAPPED {
			t.Errorf("open %q access=%#x share=%#x creation=%#x flags=%#x", got, access, share, creation, flags)
		}
		return 1, nil
	}
	set := api.setState
	api.setState = func(handle windows.Handle, state *windows.DCB) error {
		if state.BaudRate != 9600 || state.Flags != 1 || state.ByteSize != 8 || state.Parity != windows.NOPARITY || state.StopBits != windows.ONESTOPBIT {
			t.Errorf("not raw 9600 8N1: %+v", state)
		}
		return set(handle, state)
	}
	api.timeouts = func(_ windows.Handle, timeouts *windows.CommTimeouts) error {
		if timeouts.WriteTotalTimeoutConstant != 1000 || timeouts.WriteTotalTimeoutMultiplier != 0 {
			t.Errorf("unbounded driver writes: %+v", timeouts)
		}
		if timeouts.ReadIntervalTimeout != ^uint32(0) || timeouts.ReadTotalTimeoutMultiplier != ^uint32(0) || timeouts.ReadTotalTimeoutConstant != 1000 {
			t.Errorf("not bounded first-byte reads: %+v", timeouts)
		}
		return nil
	}
	api.purge = func(_ windows.Handle, flags uint32) error {
		if flags != windows.PURGE_RXABORT|windows.PURGE_RXCLEAR {
			t.Errorf("unexpected purge flags %#x", flags)
		}
		return nil
	}
	d, err := openWindowsDevice(t.Context(), "com12", 9600, api)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsOpenFailuresAndCanceledAdmission(t *testing.T) {
	for _, failure := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION} {
		api := fakeCommAPI()
		api.open = func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
			return 0, failure
		}
		if _, err := openWindowsDevice(t.Context(), "COM3", 9600, api); !errors.Is(err, ErrBusy) || !errors.Is(err, failure) {
			t.Errorf("busy cause lost: %v", err)
		}
	}
	api := fakeCommAPI()
	var closed []windows.Handle
	api.close = func(handle windows.Handle) error { closed = append(closed, handle); return nil }
	api.setState = func(windows.Handle, *windows.DCB) error { return nil }
	if _, err := openWindowsDevice(t.Context(), "COM3", 9600, api); !errors.Is(err, ErrSettings) || len(closed) != 2 {
		t.Fatalf("unapplied settings = %v, closed=%v", err, closed)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	api.open = func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
		t.Fatal("opened after cancellation")
		return 0, nil
	}
	if _, err := openWindowsDevice(ctx, "COM3", 9600, api); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: %v", err)
	}
}

func TestWindowsIODeadlineCancelsAndDrains(t *testing.T) {
	for _, reading := range []bool{false, true} {
		t.Run(fmt.Sprint("read=", reading), func(t *testing.T) {
			api := fakeCommAPI()
			d := &windowsDevice{handle: 1, changed: 2, api: api}
			call, setDeadline := d.Write, d.SetWriteDeadline
			if reading {
				call, setDeadline = d.Read, d.SetReadDeadline
			}
			var canceled, drained bool
			d.api.write = func(windows.Handle, []byte, *uint32, *windows.Overlapped) error { return windows.ERROR_IO_PENDING }
			d.api.read = d.api.write
			d.api.wait = func(handles []windows.Handle, all bool, timeout uint32) (uint32, error) {
				if all || timeout != windows.INFINITE || len(handles) != 2 || handles[1] != d.changed {
					t.Errorf("wait handles=%v all=%t timeout=%d", handles, all, timeout)
				}
				if err := setDeadline(time.Unix(1, 0)); err != nil {
					t.Error(err)
				}
				return windows.WAIT_OBJECT_0 + 1, nil
			}
			d.api.cancel = func(_ windows.Handle, operation *windows.Overlapped) error {
				canceled = operation != nil && operation.HEvent != 0
				return nil
			}
			d.api.result = func(_ windows.Handle, _ *windows.Overlapped, count *uint32, wait bool) error {
				drained = wait && canceled
				*count = 2
				return windows.ERROR_OPERATION_ABORTED
			}
			if n, err := call([]byte{1, 2, 3}); n != 2 || !errors.Is(err, os.ErrDeadlineExceeded) || !canceled || !drained {
				t.Fatalf("canceled I/O = %d, %v, canceled=%t drained=%t", n, err, canceled, drained)
			}
		})
	}
}

func TestWindowsOverlappedIOCompletionAndPartialResult(t *testing.T) {
	for _, reading := range []bool{false, true} {
		t.Run(fmt.Sprint("read=", reading), func(t *testing.T) {
			api := fakeCommAPI()
			d := &windowsDevice{handle: 1, changed: 2, api: api}
			call, setDeadline := d.Write, d.SetWriteDeadline
			if reading {
				call, setDeadline = d.Read, d.SetReadDeadline
			}
			d.api.write = func(windows.Handle, []byte, *uint32, *windows.Overlapped) error { return windows.ERROR_IO_PENDING }
			d.api.read = d.api.write
			d.api.result = func(_ windows.Handle, _ *windows.Overlapped, count *uint32, wait bool) error {
				if wait {
					t.Error("completion waited a second time")
				}
				*count = 2
				return nil
			}
			if n, err := call([]byte{1, 2, 3}); n != 2 || err != nil {
				t.Fatalf("partial completion = %d, %v", n, err)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := call([]byte{1}); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("I/O after close = %v", err)
			}
			if err := setDeadline(time.Now()); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("deadline after close = %v", err)
			}
		})
	}
}

func TestWindowsCloseJoinsCanceledIOBeforeClosingHandles(t *testing.T) {
	for _, reading := range []bool{false, true} {
		t.Run(fmt.Sprint("read=", reading), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				api := fakeCommAPI()
				d := &windowsDevice{handle: 1, changed: 2, api: api}
				call := d.Write
				if reading {
					call = d.Read
				}
				entered, wake, draining, finish := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				var signal sync.Once
				var closed atomic.Int32
				d.api.write = func(windows.Handle, []byte, *uint32, *windows.Overlapped) error { return windows.ERROR_IO_PENDING }
				d.api.read = d.api.write
				d.api.wait = func([]windows.Handle, bool, uint32) (uint32, error) {
					close(entered)
					<-wake
					return windows.WAIT_OBJECT_0 + 1, nil
				}
				d.api.signal = func(windows.Handle) error { signal.Do(func() { close(wake) }); return nil }
				d.api.result = func(windows.Handle, *windows.Overlapped, *uint32, bool) error {
					close(draining)
					<-finish
					return windows.ERROR_OPERATION_ABORTED
				}
				d.api.close = func(windows.Handle) error { closed.Add(1); return nil }
				ioDone := make(chan error, 1)
				go func() { _, err := call([]byte{1}); ioDone <- err }()
				<-entered
				closeDone := make(chan error, 1)
				go func() { closeDone <- d.Close() }()
				<-draining
				if count := closed.Load(); count != 0 {
					t.Errorf("closed %d handles before I/O completion", count)
				}
				close(finish)
				if err := <-ioDone; !errors.Is(err, os.ErrClosed) {
					t.Errorf("closed I/O = %v", err)
				}
				if err := <-closeDone; err != nil {
					t.Fatal(err)
				}
				if err := d.Close(); err != nil || closed.Load() != 3 {
					t.Fatalf("close wasn't idempotent: handles=%d, error=%v", closed.Load(), err)
				}
			})
		})
	}
}

func TestWindowsQueueAndTimeoutBounds(t *testing.T) {
	api := fakeCommAPI()
	d := &windowsDevice{handle: 1, changed: 2, api: api}
	d.api.clearErrors = func(_ windows.Handle, faults *uint32, state *windows.ComStat) error {
		*faults, state.CBOutQue = 1, 7 // Unread receive replies may overflow.
		return nil
	}
	if n, err := d.queued(t.Context()); n != 7 || err != nil {
		t.Fatalf("queue = %d, %v", n, err)
	}
	d.api.clearErrors = func(_ windows.Handle, faults *uint32, _ *windows.ComStat) error { *faults = 0x100; return nil }
	if _, err := d.queued(t.Context()); err == nil {
		t.Fatal("transmit overflow ignored")
	}
	if got := []uint32{windowsWaitTimeout(time.Time{}), windowsWaitTimeout(time.Unix(1, 0)), windowsWaitTimeout(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))}; !reflect.DeepEqual(got, []uint32{windows.INFINITE, 0, windows.INFINITE - 1}) {
		t.Fatalf("wait bounds = %v", got)
	}
}

func TestWindowsReadZeroAndIndependentDeadlines(t *testing.T) {
	d := &windowsDevice{handle: 1, changed: 2, api: fakeCommAPI()}
	if err := d.SetReadDeadline(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Write([]byte{1, 2, 3}); n != 3 || err != nil {
		t.Fatalf("read deadline affected write = %d, %v", n, err)
	}
	if n, err := d.Read(make([]byte, 3)); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expired read deadline = %d, %v", n, err)
	}
	if err := d.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetWriteDeadline(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if n, err := d.Read(buf); n != 2 || err != nil || buf[0] != 0x57 || buf[1] != 0xab {
		t.Fatalf("immediate read = % x, %v", buf[:n], err)
	}
	d.api.read = func(windows.Handle, []byte, *uint32, *windows.Overlapped) error {
		return nil // A driver read timeout may return zero bytes without EOF.
	}
	if n, err := d.Read(buf); n != 0 || err != nil {
		t.Fatalf("zero-byte driver read = %d, %v", n, err)
	}
	d.api.read = func(windows.Handle, []byte, *uint32, *windows.Overlapped) error {
		t.Fatal("zero-length read entered the driver")
		return nil
	}
	if n, err := d.Read(nil); n != 0 || err != nil {
		t.Fatalf("zero-length read = %d, %v", n, err)
	}
}

func TestWindowsInputDrain(t *testing.T) {
	d := &windowsDevice{handle: 1, changed: 2, api: fakeCommAPI()}
	ctx, cancel := context.WithCancel(t.Context())
	d.api.purge = func(handle windows.Handle, flags uint32) error {
		if handle != d.handle || flags != windows.PURGE_RXABORT|windows.PURGE_RXCLEAR {
			t.Errorf("input purge handle=%d flags=%#x", handle, flags)
		}
		cancel()
		return nil
	}
	if err := d.flushInput(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during input drain = %v", err)
	}
	d.api.purge = func(windows.Handle, uint32) error {
		t.Fatal("purged after cancellation or close")
		return nil
	}
	if err := d.flushInput(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled input drain = %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.flushInput(t.Context()); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("input drain after close = %v", err)
	}
}
