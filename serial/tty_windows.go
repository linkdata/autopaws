//go:build windows && (amd64 || arm64)

package serial

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var baudRates = map[int]uint32{
	1200: 1200, 1800: 1800, 2400: 2400, 4800: 4800, 9600: 9600,
	19200: 19200, 38400: 38400, 57600: 57600, 115200: 115200,
}

type commAPI struct {
	open        func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error)
	getState    func(windows.Handle, *windows.DCB) error
	setState    func(windows.Handle, *windows.DCB) error
	timeouts    func(windows.Handle, *windows.CommTimeouts) error
	purge       func(windows.Handle, uint32) error
	event       func(*windows.SecurityAttributes, uint32, uint32, *uint16) (windows.Handle, error)
	signal      func(windows.Handle) error
	close       func(windows.Handle) error
	read        func(windows.Handle, []byte, *uint32, *windows.Overlapped) error
	write       func(windows.Handle, []byte, *uint32, *windows.Overlapped) error
	wait        func([]windows.Handle, bool, uint32) (uint32, error)
	result      func(windows.Handle, *windows.Overlapped, *uint32, bool) error
	cancel      func(windows.Handle, *windows.Overlapped) error
	clearErrors func(windows.Handle, *uint32, *windows.ComStat) error
}

var nativeCommAPI = commAPI{
	open: windows.CreateFile, getState: windows.GetCommState, setState: windows.SetCommState,
	timeouts: windows.SetCommTimeouts, purge: windows.PurgeComm, event: windows.CreateEvent,
	signal: windows.SetEvent, close: windows.CloseHandle, read: windows.ReadFile, write: windows.WriteFile,
	wait: windows.WaitForMultipleObjects, result: windows.GetOverlappedResult,
	cancel: windows.CancelIoEx, clearErrors: windows.ClearCommError,
}

type windowsDevice struct {
	handle, changed windows.Handle
	api             commAPI
	io              sync.Mutex
	mu              sync.Mutex
	readDeadline    time.Time
	writeDeadline   time.Time
	closed          bool
	closeOnce       sync.Once
	closeErr        error
}

func comName(path string) (string, error) {
	name := strings.TrimPrefix(strings.ToUpper(path), `\\.\`)
	digits, ok := strings.CutPrefix(name, "COM")
	if !ok || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return "", fmt.Errorf("serial: expected a COM port, got %q", path)
	}
	number, err := strconv.ParseUint(digits, 10, 32)
	if err != nil || number == 0 {
		return "", fmt.Errorf("serial: invalid COM port %q", path)
	}
	return "COM" + strconv.FormatUint(number, 10), nil
}

func openDevice(ctx context.Context, path string, speed uint32) (device, error) {
	return openWindowsDevice(ctx, path, speed, nativeCommAPI)
}

func openWindowsDevice(ctx context.Context, path string, speed uint32, api commAPI) (device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := comName(path)
	if err != nil {
		return nil, err
	}
	wide, err := windows.UTF16PtrFromString(`\\.\` + name)
	if err != nil {
		return nil, err
	}
	handle, err := api.open(wide, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			err = errors.Join(ErrBusy, err)
		}
		return nil, err
	}
	changed, err := api.event(nil, 0, 0, nil)
	if err != nil {
		return nil, errors.Join(err, api.close(handle))
	}
	d := &windowsDevice{handle: handle, changed: changed, api: api}
	if err := d.configure(ctx, speed); err != nil {
		return nil, errors.Join(err, d.Close())
	}
	return d, nil
}

func rawWindowsState(state *windows.DCB, speed uint32) {
	state.BaudRate = speed
	state.Flags = 1 // fBinary, with parity processing and all flow control disabled.
	state.ByteSize, state.Parity, state.StopBits = 8, windows.NOPARITY, windows.ONESTOPBIT
}

func (d *windowsDevice) configure(ctx context.Context, speed uint32) error {
	state := windows.DCB{DCBlength: uint32(unsafe.Sizeof(windows.DCB{}))}
	if err := d.api.getState(d.handle, &state); err != nil {
		return err
	}
	rawWindowsState(&state, speed)
	if err := d.api.setState(d.handle, &state); err != nil {
		return err
	}
	if err := d.api.getState(d.handle, &state); err != nil {
		return err
	}
	if state.BaudRate != speed || state.ByteSize != 8 || state.Parity != windows.NOPARITY ||
		state.StopBits != windows.ONESTOPBIT || state.Flags&0x7fff != 1 {
		return ErrSettings
	}
	// Return buffered bytes or wait for the first byte; driver timeouts also
	// bound I/O if cancellation is delayed by the driver.
	if err := d.api.timeouts(d.handle, &windows.CommTimeouts{
		ReadIntervalTimeout: ^uint32(0), ReadTotalTimeoutMultiplier: ^uint32(0),
		ReadTotalTimeoutConstant:  uint32(ioBudget / time.Millisecond),
		WriteTotalTimeoutConstant: uint32(ioBudget / time.Millisecond),
	}); err != nil {
		return err
	}
	return d.flushInput(ctx)
}

func (d *windowsDevice) SetWriteDeadline(deadline time.Time) error {
	return d.setDeadline(deadline, false)
}

func (d *windowsDevice) SetReadDeadline(deadline time.Time) error {
	return d.setDeadline(deadline, true)
}

func (d *windowsDevice) setDeadline(deadline time.Time, read bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return os.ErrClosed
	}
	if read {
		d.readDeadline = deadline
	} else {
		d.writeDeadline = deadline
	}
	return d.api.signal(d.changed)
}

func (d *windowsDevice) ioState(read bool) (time.Time, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return time.Time{}, os.ErrClosed
	}
	deadline := d.writeDeadline
	if read {
		deadline = d.readDeadline
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return deadline, os.ErrDeadlineExceeded
	}
	return deadline, nil
}

func windowsWaitTimeout(deadline time.Time) uint32 {
	if deadline.IsZero() {
		return windows.INFINITE
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0
	}
	return uint32(min((remaining-1)/time.Millisecond+1, time.Duration(windows.INFINITE-1)))
}

func (d *windowsDevice) Write(data []byte) (int, error) { return d.transfer(data, false) }
func (d *windowsDevice) Read(data []byte) (int, error)  { return d.transfer(data, true) }

func (d *windowsDevice) transfer(data []byte, read bool) (transferred int, err error) {
	d.io.Lock()
	defer d.io.Unlock()
	defer runtime.KeepAlive(data)
	if _, err := d.ioState(read); err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, nil
	}
	event, err := d.api.event(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, d.api.close(event)) }()
	operation := windows.Overlapped{HEvent: event}
	var count uint32
	operationName, call := "write", d.api.write
	if read {
		operationName, call = "read", d.api.read
	}
	err = call(d.handle, data, &count, &operation)
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return int(count), err
	}
	for {
		deadline, reason := d.ioState(read)
		if reason != nil {
			return d.cancelIO(&operation, reason)
		}
		state, waitErr := d.api.wait([]windows.Handle{event, d.changed}, false, windowsWaitTimeout(deadline))
		if waitErr != nil {
			return d.cancelIO(&operation, waitErr)
		}
		switch state {
		case windows.WAIT_OBJECT_0:
			err := d.api.result(d.handle, &operation, &count, false)
			return int(count), err
		case windows.WAIT_OBJECT_0 + 1, uint32(windows.WAIT_TIMEOUT):
			// A changed deadline or an elapsed wait requires fresh state.
		default:
			return d.cancelIO(&operation, fmt.Errorf("serial: unexpected %s wait status %d", operationName, state))
		}
	}
}

func (d *windowsDevice) cancelIO(operation *windows.Overlapped, reason error) (int, error) {
	cancelErr := d.api.cancel(d.handle, operation)
	if errors.Is(cancelErr, windows.ERROR_NOT_FOUND) {
		cancelErr = nil // Completion won the cancellation race.
	}
	// OVERLAPPED and data must stay alive until the driver has completed the I/O.
	var count uint32
	resultErr := d.api.result(d.handle, operation, &count, true)
	if errors.Is(resultErr, windows.ERROR_OPERATION_ABORTED) {
		resultErr = nil
	}
	return int(count), errors.Join(reason, cancelErr, resultErr)
}

func (d *windowsDevice) flushInput(ctx context.Context) error {
	d.io.Lock()
	defer d.io.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return os.ErrClosed
	}
	err := d.api.purge(d.handle, windows.PURGE_RXABORT|windows.PURGE_RXCLEAR)
	return errors.Join(ctx.Err(), err)
}

func (d *windowsDevice) queued(ctx context.Context) (int, error) {
	d.io.Lock()
	defer d.io.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return 0, os.ErrClosed
	}
	var faults uint32
	var state windows.ComStat
	if err := d.api.clearErrors(d.handle, &faults, &state); err != nil {
		return 0, errors.Join(ctx.Err(), err)
	}
	// Ordinary HID writes leave replies unread; receive overflow is not a write failure.
	if faults&0x100 != 0 { // CE_TXFULL
		return 0, fmt.Errorf("serial: communication errors %#x", faults)
	}
	return int(state.CBOutQue), ctx.Err()
}

func (d *windowsDevice) Close() error {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		wakeErr := d.api.signal(d.changed)
		d.mu.Unlock()
		d.io.Lock()
		defer d.io.Unlock()
		d.closeErr = errors.Join(wakeErr, d.api.close(d.handle), d.api.close(d.changed))
	})
	return d.closeErr
}
