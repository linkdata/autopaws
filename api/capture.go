//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/linkdata/autopaws/apiwire"
)

type captureFormat struct {
	Width        uint32 `json:"width"`
	Height       uint32 `json:"height"`
	FourCC       uint32 `json:"fourcc"`
	BytesPerLine uint32 `json:"bytes_per_line"`
	SizeImage    uint32 `json:"size_image"`
}

type capturePixelFormat struct {
	FourCC      uint32 `json:"fourcc"`
	Description string `json:"description"`
	Flags       uint32 `json:"flags"`
}

type captureUSB struct {
	VID     string `json:"vid"`
	PID     string `json:"pid"`
	BusPath string `json:"bus_path"`
}

type captureDevice struct {
	Device  string               `json:"device"`
	Name    string               `json:"name"`
	Formats []capturePixelFormat `json:"formats"`
	USB     captureUSB           `json:"usb"`
}

type captureStatus struct {
	Device           string          `json:"device"`
	RequestedWidth   uint32          `json:"requested_width"`
	RequestedHeight  uint32          `json:"requested_height"`
	Autocrop         bool            `json:"autocrop"`
	Running          bool            `json:"running"`
	Generation       uint64          `json:"generation"`
	Format           *captureFormat  `json:"format,omitempty"`
	AvailableDevices []captureDevice `json:"available_devices"`
	Error            apiwire.Code    `json:"error,omitempty"`
	Detail           string          `json:"detail,omitempty"`
}

func (server *Server) captureDevices(ctx context.Context) ([]captureDevice, error) {
	if server.options.DiscoverCapture == nil {
		return nil, apiwire.ErrUnavailable
	}
	found, err := server.options.DiscoverCapture(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]captureDevice, 0, len(found))
	for _, device := range found {
		formats := make([]capturePixelFormat, 0, len(device.Formats))
		for _, format := range device.Formats {
			formats = append(formats, capturePixelFormat{format.FourCC, format.Description, format.Flags})
		}
		result = append(result, captureDevice{
			Device: device.Path, Name: device.Name, Formats: formats,
			USB: captureUSB{device.USB.VID, device.USB.PID, device.USB.BusPath},
		})
	}
	return result, nil
}

// captureInfo takes one manager metadata snapshot. Discovery enriches it
// without opening a capture stream or changing selected/repairable settings.
func (server *Server) captureInfo(ctx context.Context) any {
	manager := server.options.Capture
	if manager == nil {
		return map[string]apiwire.Code{"error": apiwire.ErrUnavailable}
	}
	info := manager.Info()
	result := captureStatus{
		Device: info.Device, RequestedWidth: info.Width, RequestedHeight: info.Height,
		Autocrop: info.Autocrop, Running: info.Running, Generation: info.Generation,
		AvailableDevices: []captureDevice{},
	}
	if info.Format.Width != 0 || info.Format.Height != 0 {
		result.Format = &captureFormat{info.Format.Width, info.Format.Height, info.Format.FourCC, info.Format.BytesPerLine, info.Format.SizeImage}
	}
	found, discoveryErr := server.captureDevices(ctx)
	if discoveryErr == nil {
		result.AvailableDevices = found
	}
	statusErr := errors.Join(info.Err, discoveryErr)
	if info.Device == "" {
		statusErr = errors.Join(apiwire.ErrUnavailable, statusErr)
	}
	if statusErr != nil {
		result.Error, result.Detail = classifyError(statusErr)
	}
	return result
}

func (server *Server) captureFrame(ctx context.Context, data json.RawMessage) (any, error) {
	if err := params(data, &struct{}{}); err != nil {
		return nil, err
	}
	if server.options.Capture == nil {
		return nil, apiwire.ErrUnavailable
	}
	frame, err := server.options.Capture.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return struct {
		JPEG         []byte `json:"jpeg_b64"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		TargetWidth  int    `json:"target_width"`
		TargetHeight int    `json:"target_height"`
	}{frame.JPEG, frame.Width, frame.Height, server.options.ScreenWidth, server.options.ScreenHeight}, nil
}

func (server *Server) listCaptureDevices(ctx context.Context, data json.RawMessage) (any, error) {
	if err := params(data, &struct{}{}); err != nil {
		return nil, err
	}
	found, err := server.captureDevices(ctx)
	if err != nil {
		return nil, err
	}
	return struct {
		Devices []captureDevice `json:"devices"`
	}{found}, nil
}

func (server *Server) setCaptureDevice(ctx context.Context, data json.RawMessage) (any, error) {
	var request struct {
		Device *string `json:"device"`
	}
	if err := params(data, &request); err != nil {
		return nil, err
	}
	if request.Device == nil {
		return nil, required("device")
	}
	numeric := *request.Device
	if len(numeric) > 0 && (numeric[0] == '+' || numeric[0] == '-') {
		numeric = numeric[1:]
	}
	if *request.Device == "" || numeric != "" && strings.Trim(numeric, "0123456789") == "" {
		return nil, fmt.Errorf("%w: device must be a nonempty path, not a numeric index", apiwire.ErrInvalidParams)
	}
	if server.options.Capture == nil {
		return nil, apiwire.ErrUnavailable
	}
	if err := server.options.Capture.SetDevice(ctx, *request.Device); err != nil {
		return nil, err
	}
	return server.captureInfo(ctx), nil
}

func (server *Server) setCaptureResolution(ctx context.Context, data json.RawMessage) (any, error) {
	var request struct {
		Width, Height *uint32
	}
	if err := params(data, &request); err != nil {
		return nil, err
	}
	if request.Width == nil || request.Height == nil || *request.Width == 0 || *request.Height == 0 {
		return nil, fmt.Errorf("%w: width and height must be positive uint32 values", apiwire.ErrInvalidParams)
	}
	if server.options.Capture == nil {
		return nil, apiwire.ErrUnavailable
	}
	if err := server.options.Capture.SetResolution(ctx, *request.Width, *request.Height); err != nil {
		return nil, err
	}
	return server.captureInfo(ctx), nil
}
