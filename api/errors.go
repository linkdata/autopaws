//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"context"
	"errors"
	"io/fs"

	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/hid"
	"github.com/linkdata/autopaws/serial"
)

func classifyError(err error) (apiwire.Code, string) {
	var code apiwire.Code
	switch {
	case errors.As(err, &code):
	case errors.Is(err, context.Canceled):
		code = apiwire.ErrCanceled
	case errors.Is(err, context.DeadlineExceeded):
		code = apiwire.ErrTimeout
	case errors.Is(err, capture.ErrCaptureChanged):
		code = apiwire.ErrCaptureChanged
	case errors.Is(err, hid.ErrMappingChanged):
		code = apiwire.ErrMappingChanged
	case errors.Is(err, hid.ErrMappingUnavailable):
		code = apiwire.ErrMappingUnavailable
	case errors.Is(err, hid.ErrUnsupported):
		code = apiwire.ErrUnsupported
	case errors.Is(err, hid.ErrLayout):
		var pathError *fs.PathError
		if errors.As(err, &pathError) && !invalidLayoutPath(err) {
			code = apiwire.ErrUnavailable
		} else {
			code = apiwire.ErrInvalidParams
		}
	case errors.Is(err, hid.ErrModifier), errors.Is(err, hid.ErrKeycode),
		errors.Is(err, hid.ErrDelay), errors.Is(err, hid.ErrSequence),
		errors.Is(err, hid.ErrCoordinates), errors.Is(err, hid.ErrMovement),
		errors.Is(err, hid.ErrButton), errors.Is(err, hid.ErrButtonMode), errors.Is(err, capture.ErrOptions):
		code = apiwire.ErrInvalidParams
	case errors.Is(err, hid.ErrMapping):
		code = apiwire.ErrInvalidParams
	case errors.Is(err, serial.ErrBusy):
		code = apiwire.ErrBusy
	default:
		code = apiwire.ErrUnavailable
	}
	detail := err.Error()
	if detail == code.Error() {
		detail = ""
	}
	return code, detail
}
