//go:build (linux || darwin || windows) && (amd64 || arm64)

package serial

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

type adapterDevice struct {
	fakeDevice
	parameters [ch9329.ParameterSize]byte
	resets     int
	reply      func([]byte, []byte) []byte
	chunk      int
	zeroReads  int
	readStart  chan struct{}
	readOnce   sync.Once
	onDeadline func(time.Time)

	rxMu     sync.Mutex
	rx       []byte
	deadline time.Time
	changed  chan struct{}
	closed   bool
}

func adapterReply(command byte, payload []byte) []byte {
	frame := append([]byte{0x57, 0xab, 0, command | 0x80, byte(len(payload))}, payload...)
	var sum byte
	for _, value := range frame {
		sum += value
	}
	return append(frame, sum)
}

func adapterPort(t *testing.T) (*Port, *adapterDevice) {
	t.Helper()
	d := &adapterDevice{changed: make(chan struct{}), readStart: make(chan struct{})}
	for index := range d.parameters {
		d.parameters[index] = byte(index + 13)
	}
	d.parameters[0], d.parameters[1], d.parameters[2] = 0x80, 0x80, 0
	binary.BigEndian.PutUint32(d.parameters[3:7], 9600)
	d.write = func(frame []byte) (int, error) {
		payload := []byte{0}
		switch frame[3] {
		case 1:
			payload = []byte{0x38, 1, 0, 0, 0, 0, 0, 0}
		case 8:
			payload = d.parameters[:]
		case 9:
			copy(d.parameters[:], frame[5:55])
		case 15:
			d.resets++
		}
		response := adapterReply(frame[3], payload)
		if d.reply != nil {
			response = d.reply(frame, response)
		}
		d.rxMu.Lock()
		d.rx = append(d.rx, response...)
		close(d.changed)
		d.changed = make(chan struct{})
		d.rxMu.Unlock()
		return len(frame), nil
	}
	d.close = func() error {
		d.rxMu.Lock()
		defer d.rxMu.Unlock()
		d.closed = true
		close(d.changed)
		d.changed = make(chan struct{})
		return nil
	}
	p, err := New(Options{Path: "/fake/serial", Baud: 9600})
	if err != nil {
		t.Fatal(err)
	}
	p.open = func(context.Context, string, uint32) (device, error) { return d, nil }
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := p.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p, d
}

func (d *adapterDevice) SetReadDeadline(deadline time.Time) error {
	d.rxMu.Lock()
	d.deadline = deadline
	close(d.changed)
	d.changed = make(chan struct{})
	d.rxMu.Unlock()
	if d.onDeadline != nil {
		d.onDeadline(deadline)
	}
	return nil
}

func (d *adapterDevice) flushInput(ctx context.Context) error {
	d.rxMu.Lock()
	defer d.rxMu.Unlock()
	d.rx = nil
	return ctx.Err()
}

func (d *adapterDevice) Read(buffer []byte) (int, error) {
	d.readOnce.Do(func() { close(d.readStart) })
	for {
		d.rxMu.Lock()
		if d.zeroReads > 0 {
			d.zeroReads--
			d.rxMu.Unlock()
			return 0, nil
		}
		if len(d.rx) > 0 {
			if d.chunk > 0 {
				buffer = buffer[:min(len(buffer), d.chunk)]
			}
			n := copy(buffer, d.rx)
			d.rx = d.rx[n:]
			d.rxMu.Unlock()
			return n, nil
		}
		deadline, changed, closed := d.deadline, d.changed, d.closed
		d.rxMu.Unlock()
		if closed {
			return 0, os.ErrClosed
		}
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
			return 0, os.ErrDeadlineExceeded
		}
	}
}

func adapterSetModes(d *adapterDevice) []byte {
	frames, _ := d.snapshot()
	var modes []byte
	for _, frame := range frames {
		if frame[3] == 9 {
			modes = append(modes, frame[5])
		}
	}
	return modes
}

func TestGetUSBModeValidatesFragmentedReplies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, d := adapterPort(t)
		d.chunk, d.zeroReads = 3, 2
		d.reply = func(request, response []byte) []byte {
			if request[3] != 1 {
				return response
			}
			bad := bytes.Clone(response)
			bad[len(bad)-1]++
			other := bytes.Clone(response)
			other[2]++
			other[len(other)-1]++
			prefix := append([]byte{0x12, 0x57}, bad...)
			prefix = append(prefix, other...)
			prefix = append(prefix, adapterReply(5, []byte{0})...)
			return append(prefix, response...)
		}
		mode, err := p.GetUSBMode(t.Context())
		if err != nil || mode != (ch9329.USBMode{RawMode: 0x80, Mode: 0, Source: "hardware_pins", FirmwareVersion: 0x38, USBEnumerated: true}) {
			t.Fatalf("mode=%+v, %v", mode, err)
		}
		if p.generation != 1 || len(adapterSetModes(d)) != 0 || d.resets != 0 {
			t.Fatal("query changed adapter state")
		}
	})
}

func TestSetUSBModePreservesParametersAndVerifiesRestoration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, d := adapterPort(t)
		original := d.parameters
		mode, err := p.SetUSBMode(t.Context(), 2)
		if err != nil || mode.RawMode != 2 || mode.Source != "software" || !mode.USBEnumerated {
			t.Fatalf("set mode=%+v, %v", mode, err)
		}
		want := original
		want[0] = 2
		if d.parameters != want || !slices.Equal(adapterSetModes(d), []byte{0x80, 2}) || p.generation != 3 {
			t.Fatal("SET did not preserve parameters or complete its safety/reset gates")
		}
		if _, err := p.SetUSBMode(t.Context(), 2); err != nil || len(adapterSetModes(d)) != 2 || p.generation != 3 {
			t.Fatal("same-mode request reset or rewrote configuration", err)
		}
		if _, err := p.SetUSBMode(t.Context(), 0x80); err != nil || d.parameters != original || p.generation != 5 {
			t.Fatal("hardware-pin configuration did not restore exactly", err)
		}
	})
}

func TestUSBModeRejectsInvalidOrUnsupportedBeforeIO(t *testing.T) {
	p, err := New(Options{Path: "/must/not/open"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, raw := range []byte{1, 3, 0x81, 0x83, 0xff} {
		if _, err := p.SetUSBMode(t.Context(), raw); !errors.Is(err, ch9329.ErrUSBMode) {
			t.Fatal("invalid mode touched admission/hardware", err)
		}
	}
	if _, err := p.GetUSBMode(t.Context()); !errors.Is(err, ErrNotPrepared) {
		t.Fatal(err)
	}
	writeOnly := preparedPort(t, &fakeDevice{})
	if _, err := writeOnly.GetUSBMode(t.Context()); !errors.Is(err, ErrUSBConfigUnsupported) {
		t.Fatal(err)
	}
}

func TestUSBModeNoopResetNeedsAcknowledgement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, d := adapterPort(t)
		original := d.parameters
		d.reply = func(request, response []byte) []byte {
			if request[3] == 15 {
				return nil
			}
			return response
		}
		_, err := p.SetUSBMode(t.Context(), 2)
		if err == nil || !strings.Contains(err.Error(), "requested mode not written") || !strings.Contains(err.Error(), "readback matches") {
			t.Fatal("missing RESET acknowledgement reported success or lost readback state", err)
		}
		if d.parameters != original || !slices.Equal(adapterSetModes(d), []byte{0x80}) || p.dev != nil {
			t.Fatal("safety gate changed the requested mode or left port prepared")
		}
	})
}

func TestUSBModeRejectsUnsafeStoredSerialSettings(t *testing.T) {
	for _, index := range []int{1, 2, 6} {
		synctest.Test(t, func(t *testing.T) {
			p, d := adapterPort(t)
			d.parameters[index]++
			if _, err := p.SetUSBMode(t.Context(), 2); !errors.Is(err, ErrUSBConfigUnsupported) {
				t.Fatalf("unsafe serial parameter %d accepted: %v", index, err)
			}
			if len(adapterSetModes(d)) != 0 || d.resets != 0 {
				t.Fatal("unsupported serial settings were written/reset")
			}
		})
	}
}

func TestUSBModeNoopReadbackMismatchNeverChangesMode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, d := adapterPort(t)
		corrupted := false
		d.reply = func(request, response []byte) []byte {
			if request[3] == 8 && d.resets == 1 && !corrupted {
				corrupted = true
				response[54]++
				response[len(response)-1]++
			}
			return response
		}
		_, err := p.SetUSBMode(t.Context(), 2)
		if err == nil || !strings.Contains(err.Error(), "50-byte") || !strings.Contains(err.Error(), "requested mode not written") {
			t.Fatal("no-op readback mismatch passed the change gate", err)
		}
		if !slices.Equal(adapterSetModes(d), []byte{0x80}) || p.dev != nil {
			t.Fatal("readback mismatch entered the requested mode")
		}
	})
}

func TestUSBModeFailureAfterDifferentSetRestoresOrReportsUnverified(t *testing.T) {
	for _, failure := range []string{"lost SET acknowledgement", "readback mismatch", "cancellation", "rollback failure"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, d := adapterPort(t)
				original := d.parameters
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				changed := false
				d.reply = func(request, response []byte) []byte {
					if request[3] == 9 && request[5] == 2 {
						changed = true
						if failure == "cancellation" {
							cancel()
						}
						if failure == "lost SET acknowledgement" || failure == "rollback failure" {
							return nil
						}
					}
					if failure == "readback mismatch" && request[3] == 8 && d.parameters[0] == 2 {
						response[54]++
						response[len(response)-1]++
					}
					if failure == "rollback failure" && changed && request[3] == 9 && request[5] == 0x80 {
						return nil
					}
					return response
				}
				_, err := p.SetUSBMode(ctx, 2)
				if err == nil || !slices.Equal(adapterSetModes(d), []byte{0x80, 2, 0x80}) || p.dev != nil {
					t.Fatalf("failed change was accepted/retried or not restored: %v, SETs%x", err, adapterSetModes(d))
				}
				if failure == "rollback failure" {
					if !strings.Contains(err.Error(), "configuration unverified") {
						t.Fatal("unacknowledged rollback claimed verified restoration", err)
					}
				} else if !strings.Contains(err.Error(), "restored and verified") || d.parameters != original {
					t.Fatal("failure did not verify full original restoration", err)
				}
				if failure == "cancellation" && !errors.Is(err, context.Canceled) {
					t.Fatal("rollback replaced cancellation error", err)
				}
			})
		})
	}
}

func TestUSBModeCancellationJoinsReadDeadlineBeforeRetirement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, d := adapterPort(t)
		d.reply = func([]byte, []byte) []byte { return nil }
		entered, release := make(chan struct{}), make(chan struct{})
		d.onDeadline = func(deadline time.Time) {
			if deadline.Equal(time.Unix(1, 0)) {
				close(entered)
				<-release
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { _, err := p.GetUSBMode(ctx); done <- err }()
		<-d.readStart
		cancel()
		<-entered
		synctest.Wait()
		if _, closes := d.snapshot(); closes != 0 {
			t.Fatal("device retired before its deadline callback joined")
		}
		select {
		case err := <-done:
			t.Fatal("operation released ownership while callback was active", err)
		default:
		}
		close(release)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, closes := d.snapshot(); closes != 1 || p.dev != nil {
			t.Fatal("canceled query did not retire its device exactly once")
		}
	})
}
