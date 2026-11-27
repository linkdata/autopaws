//go:build (linux || darwin || windows) && (amd64 || arm64)

package serial

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/linkdata/autopaws/ch9329"
)

// ErrUSBConfigUnsupported indicates that this backend/configuration cannot be
// safely inspected and reset through the CH9329 serial protocol.
var ErrUSBConfigUnsupported = errors.New("serial: adapter USB configuration unsupported")

type adapterReader interface {
	Read([]byte) (int, error)
	SetReadDeadline(time.Time) error
	flushInput(context.Context) error
}

// GetUSBMode reads the prepared adapter without changing its configuration.
// The caller holds the HID action gate, including any initial Prepare.
func (p *Port) GetUSBMode(ctx context.Context) (ch9329.USBMode, error) {
	ctx, cancel := p.operationContext(ctx)
	defer cancel()
	ctx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	if err := p.acquire(ctx); err != nil {
		return ch9329.USBMode{}, err
	}
	defer p.release()
	r, err := p.adapterReader()
	if err != nil {
		return ch9329.USBMode{}, err
	}
	mode, _, err := p.readUSBMode(ctx, r)
	if err != nil {
		return ch9329.USBMode{}, fmt.Errorf("get adapter USB mode: %w", errors.Join(err, p.invalidate()))
	}
	return mode, nil
}

// SetUSBMode preserves all other 49 parameter bytes. A different-mode write is
// preceded by a verified unchanged SET/reset round trip. Failures after that
// write attempt try to restore the saved parameters before retiring the port.
// The caller holds the HID gate and releases all input before this operation.
func (p *Port) SetUSBMode(ctx context.Context, raw byte) (ch9329.USBMode, error) {
	if !ch9329.ValidUSBMode(raw) {
		return ch9329.USBMode{}, ch9329.ErrUSBMode
	}
	ctx, cancel := p.operationContext(ctx)
	defer cancel()
	ctx, stop := context.WithTimeout(ctx, 8*time.Second)
	defer stop()
	if err := p.acquire(ctx); err != nil {
		return ch9329.USBMode{}, err
	}
	defer p.release()
	r, err := p.adapterReader()
	if err != nil {
		return ch9329.USBMode{}, err
	}
	mode, original, err := p.readUSBMode(ctx, r)
	if err != nil {
		return ch9329.USBMode{}, fmt.Errorf("read adapter USB mode: %w", errors.Join(err, p.invalidate()))
	}
	if mode.RawMode == raw {
		return mode, nil
	}
	if _, err := p.applyUSBParameters(ctx, r, original); err != nil {
		readback, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer stop()
		payload, readErr := p.adapterExchange(readback, r, ch9329.GetParameters(), ch9329.ParameterSize)
		state := "original configuration readback unavailable"
		if readErr == nil {
			state = "original configuration readback differs"
			if bytes.Equal(payload, original[:]) {
				state = "original configuration readback matches"
			}
		}
		return ch9329.USBMode{}, fmt.Errorf("adapter no-op safety gate failed; requested mode not written; %s: %w",
			state, errors.Join(err, readErr, p.invalidate()))
	}
	updated := original
	updated[0] = raw
	mode, err = p.applyUSBParameters(ctx, r, updated)
	if err == nil {
		return mode, nil
	}
	rollback, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer stop()
	_, restoreErr := p.applyUSBParameters(rollback, r, original)
	state := "original configuration restored and verified"
	if restoreErr != nil {
		state = "original configuration restoration failed; adapter configuration unverified"
	}
	return ch9329.USBMode{}, fmt.Errorf("adapter USB mode change failed; %s: %w", state, errors.Join(err, restoreErr, p.invalidate()))
}

func (p *Port) adapterReader() (adapterReader, error) {
	if p.dev == nil {
		return nil, ErrNotPrepared
	}
	r, ok := p.dev.(adapterReader)
	if !ok {
		return nil, ErrUSBConfigUnsupported
	}
	return r, nil
}

func (p *Port) readUSBMode(ctx context.Context, r adapterReader) (ch9329.USBMode, [ch9329.ParameterSize]byte, error) {
	var parameters [ch9329.ParameterSize]byte
	info, err := p.adapterExchange(ctx, r, ch9329.GetInfo(), 8)
	if err != nil {
		return ch9329.USBMode{}, parameters, err
	}
	payload, err := p.adapterExchange(ctx, r, ch9329.GetParameters(), ch9329.ParameterSize)
	if err != nil {
		return ch9329.USBMode{}, parameters, err
	}
	copy(parameters[:], payload)
	if parameters[1]&^byte(0x80) != 0 || parameters[2] != 0 || binary.BigEndian.Uint32(parameters[3:7]) != uint32(p.opts.Baud) {
		return ch9329.USBMode{}, parameters, ErrUSBConfigUnsupported
	}
	mode, err := ch9329.ParseUSBMode(info, parameters)
	return mode, parameters, err
}

func (p *Port) applyUSBParameters(ctx context.Context, r adapterReader, parameters [ch9329.ParameterSize]byte) (ch9329.USBMode, error) {
	if _, err := p.adapterExchange(ctx, r, ch9329.SetParameters(parameters), 1); err != nil {
		return ch9329.USBMode{}, err
	}
	if _, err := p.adapterExchange(ctx, r, ch9329.Reset(), 1); err != nil {
		return ch9329.USBMode{}, err
	}
	p.generation++
	if err := wait(ctx, time.Second); err != nil {
		return ch9329.USBMode{}, err
	}
	for {
		mode, actual, err := p.readUSBMode(ctx, r)
		if err != nil {
			return ch9329.USBMode{}, err
		}
		if actual != parameters {
			return ch9329.USBMode{}, errors.New("adapter full 50-byte configuration readback mismatch")
		}
		if mode.USBEnumerated {
			return mode, nil
		}
		if err := wait(ctx, 50*time.Millisecond); err != nil {
			return ch9329.USBMode{}, err
		}
	}
}

// adapterExchange owns the port gate. Join the wake callback before another
// operation can change the deadline or retire/reuse this device.
func (p *Port) adapterExchange(parent context.Context, r adapterReader, request []byte, length int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, ioBudget)
	defer cancel()
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	if err := r.flushInput(ctx); err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	if err := r.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	woke := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = r.SetReadDeadline(time.Unix(1, 0))
		close(woke)
	})
	defer func() {
		if !stop() {
			<-woke
		}
	}()
	if err := p.send(ctx, request); err != nil {
		return nil, err
	}
	var pending []byte
	var buffer [128]byte
	total := 0
	for {
		if err := p.check(ctx); err != nil {
			return nil, err
		}
		n, err := r.Read(buffer[:])
		if err = errors.Join(p.check(ctx), err); err != nil {
			return nil, err
		}
		if n == 0 {
			if err := wait(ctx, queuePoll); err != nil {
				return nil, err
			}
			continue
		}
		total += n
		if total > 4096 {
			return nil, errors.New("adapter reply exceeded input limit")
		}
		pending = append(pending, buffer[:n]...)
		for len(pending) >= 6 {
			start := bytes.Index(pending, []byte{0x57, 0xab})
			if start < 0 {
				pending = pending[len(pending)-1:]
				break
			}
			pending = pending[start:]
			if len(pending) < 6 {
				break
			}
			size := int(pending[4]) + 6
			if size > 70 {
				pending = pending[1:]
				continue
			}
			if len(pending) < size {
				break
			}
			var sum byte
			for _, value := range pending[:size-1] {
				sum += value
			}
			if sum != pending[size-1] {
				pending = pending[1:]
				continue
			}
			frame := pending[:size]
			pending = pending[size:]
			if frame[2] != 0 || frame[3] != request[3]|0x80 && frame[3] != request[3]|0xc0 {
				continue
			}
			if frame[3] != request[3]|0x80 || int(frame[4]) != length || length == 1 && frame[5] != 0 {
				return nil, fmt.Errorf("adapter command %02x rejected or malformed reply", request[3])
			}
			return bytes.Clone(frame[5 : size-1]), nil
		}
	}
}
