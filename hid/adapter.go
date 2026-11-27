package hid

import (
	"context"
	"errors"

	"github.com/linkdata/autopaws/ch9329"
)

// ErrAdapterUSBMode means this transport cannot configure its USB mouse mode.
var ErrAdapterUSBMode = errors.New("adapter USB mode unavailable")

type usbModeTransport interface {
	GetUSBMode(context.Context) (ch9329.USBMode, error)
	SetUSBMode(context.Context, byte) (ch9329.USBMode, error)
}

// GetUSBMode reads adapter configuration without interleaving an input action.
func (controller *Controller) GetUSBMode(ctx context.Context) (mode ch9329.USBMode, err error) {
	adapter, ok := controller.transport.(usbModeTransport)
	if !ok {
		return mode, ErrAdapterUSBMode
	}
	err = controller.run(ctx, releaseBoth, func(current *action) error {
		current.dirty = true // A failed query can invalidate the port holding input.
		mode, err = adapter.GetUSBMode(current.ctx)
		return err
	})
	return mode, err
}

// SetUSBMode releases input, then changes and verifies the persistent USB mode.
func (controller *Controller) SetUSBMode(ctx context.Context, raw byte) (mode ch9329.USBMode, err error) {
	if !ch9329.ValidUSBMode(raw) {
		return mode, ch9329.ErrUSBMode
	}
	adapter, ok := controller.transport.(usbModeTransport)
	if !ok {
		return mode, ErrAdapterUSBMode
	}
	err = controller.run(ctx, releaseBoth, func(current *action) error {
		attempt := controller.beginCleanup(ctx)
		releaseErr := controller.cleanup(attempt, releaseBoth)
		controller.endCleanup(attempt)
		if releaseErr != nil {
			return releaseErr
		}
		if err := current.ctx.Err(); err != nil {
			return err
		}
		current.dirty = true // Configuration writes may have reached the adapter.
		mode, err = adapter.SetUSBMode(current.ctx, raw)
		controller.prepared = false // A reset may have occurred even on failure.
		return err
	})
	return mode, err
}
