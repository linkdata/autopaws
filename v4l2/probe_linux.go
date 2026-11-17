//go:build linux && (amd64 || arm64)

package v4l2

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"syscall"
	"unsafe"
)

// Capability flags needed by discovery. Capabilities contains CapDeviceCaps
// when the node-specific DeviceCaps field is available. Discovery can apply a
// stricter modern-driver policy than Open's aggregate-capability fallback.
const (
	CapDeviceCaps   uint32 = capDeviceCaps
	CapVideoCapture uint32 = capVideoCapture
)

// Info contains the raw QUERYCAP capability fields and advertised pixel formats.
// Nodes without device-specific capture capabilities return no Formats.
type Info struct {
	Capabilities uint32
	DeviceCaps   uint32
	Formats      []PixelFormat
}

// Probe uses a read-only, nonblocking file and QUERYCAP/ENUM_FMT only. It does
// not require MJPG, streaming, read deadlines or an idle capture device, and
// never changes format, allocates buffers or starts a stream. It retains fd
// ownership until synchronous kernel calls and close finish normally.
// Formats are enumerated only when CapDeviceCaps is present and DeviceCaps
// includes CapVideoCapture, matching discovery's modern-driver selection policy.
func Probe(ctx context.Context, path string) (Info, error) {
	return probe(ctx, path, systemOperations())
}

func probe(ctx context.Context, path string, ops operations) (info Info, err error) {
	if err = ctx.Err(); err != nil {
		return info, err
	}
	file, err := ops.open(path, os.O_RDONLY|syscall.O_NONBLOCK)
	if err != nil {
		return info, operationError("probe open", path, err)
	}
	defer func() {
		err = operationError("probe", path, errors.Join(err, file.Close(), ctx.Err()))
	}()
	raw, err := file.SyscallConn()
	if err != nil {
		return info, err
	}
	device := &Device{raw: raw, ops: ops}
	info, err = device.queryCapabilities(ctx)
	if err == nil && info.Capabilities&CapDeviceCaps != 0 && info.DeviceCaps&CapVideoCapture != 0 {
		info.Formats, err = device.enumFormats(ctx)
	}
	return info, err
}

func (device *Device) queryCapabilities(ctx context.Context) (Info, error) {
	var caps capability
	if err := device.control(ctx, vidiocQuerycap, unsafe.Pointer(&caps)); err != nil {
		return Info{}, fmt.Errorf("QUERYCAP: %w", err)
	}
	return Info{Capabilities: caps.Capabilities, DeviceCaps: caps.DeviceCaps}, nil
}

func (info Info) effectiveCapabilities() uint32 {
	if info.Capabilities&capDeviceCaps != 0 {
		return info.DeviceCaps
	}
	return info.Capabilities
}

func (device *Device) enumFormats(ctx context.Context) ([]PixelFormat, error) {
	var formats []PixelFormat
	for index := uint32(0); ; index++ {
		descriptor := fmtdesc{Index: index, Type: bufTypeVideoCapture}
		if err := device.control(ctx, vidiocEnumFmt, unsafe.Pointer(&descriptor)); err != nil {
			if errors.Is(err, syscall.EINVAL) && ctx.Err() == nil {
				return formats, nil
			}
			return nil, fmt.Errorf("ENUM_FMT: %w", err)
		}
		formats = append(formats, PixelFormat{descriptor.PixelFormat, cString(descriptor.Description[:]), descriptor.Flags})
		if index == math.MaxUint32 {
			return nil, fmt.Errorf("ENUM_FMT did not terminate: %w", ErrUnsupportedFormat)
		}
	}
}
