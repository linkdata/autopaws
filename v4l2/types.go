// Package v4l2 defines video frame types and Linux V4L2 capture.
// The frame types are also used by FFmpeg capture on macOS.
package v4l2

import (
	"errors"
	"math"
)

var (
	ErrClosed            = errors.New("v4l2: device closed")
	ErrOptions           = errors.New("v4l2: invalid capture options")
	ErrNotCapture        = errors.New("v4l2: not a streaming video capture device")
	ErrUnsupportedFormat = errors.New("v4l2: MJPG capture not supported")
	ErrInvalidBuffer     = errors.New("v4l2: invalid buffer descriptor")
	ErrTimestamp         = errors.New("v4l2: invalid capture timestamp")
	ErrDisconnected      = errors.New("v4l2: device disconnected")
)

// Options requests an MJPG size. Both dimensions must be positive. The driver
// may adjust them; Format reports what it actually accepted. Frame rate remains
// driver-selected; this package does not issue S_PARM or enumerate intervals.
type Options struct{ Width, Height uint32 }

// Format is the single-planar format returned by S_FMT.
type Format struct {
	Width, Height           uint32
	FourCC                  uint32
	BytesPerLine, SizeImage uint32
}

// PixelFormat is one ENUM_FMT result, in driver preference order.
type PixelFormat struct {
	FourCC      uint32
	Description string
	Flags       uint32
}

// Timestamp is the raw V4L2 timeval. Its clock domain and capture source are
// given by Frame.Flags. It is not a calendar time or a Go process-relative time.
type Timestamp struct{ Seconds, Microseconds int64 }

// Nanoseconds converts a valid nonnegative timeval without overflow. Check the
// frame's TimestampMask first before comparing it to another clock's reading.
func (timestamp Timestamp) Nanoseconds() (int64, error) {
	if timestamp.Seconds < 0 || timestamp.Microseconds < 0 || timestamp.Microseconds >= 1_000_000 ||
		timestamp.Seconds > (math.MaxInt64-timestamp.Microseconds*1_000)/1_000_000_000 {
		return 0, ErrTimestamp
	}
	return timestamp.Seconds*1_000_000_000 + timestamp.Microseconds*1_000, nil
}

// Timestamp flag masks preserve the Linux clock domain and capture instant.
// UNKNOWN must not be interpreted as CLOCK_MONOTONIC. COPY is a copied timestamp,
// rather than a promise that this source generated a monotonic capture time.
const (
	TimestampMask            uint32 = 0x0000e000
	TimestampUnknown         uint32 = 0x00000000
	TimestampMonotonic       uint32 = 0x00002000
	TimestampCopy            uint32 = 0x00004000
	TimestampSourceMask      uint32 = 0x00070000
	TimestampEndOfFrame      uint32 = 0x00000000
	TimestampStartOfExposure uint32 = 0x00010000
)

// Frame owns Data independently of the MMAP buffers, subsequent reads and Close.
// Linux Flags and Sequence retain the driver's full values. FFmpeg-backed
// sources synthesize sequence/timing and set TimestampEstimated.
// ERROR-flagged and zero-byte buffers are requeued and skipped.
type Frame struct {
	Data      []byte
	Timestamp Timestamp
	Flags     uint32
	Sequence  uint32
}

// TimestampEstimated marks host receipt estimates from FFmpeg sources.
const TimestampEstimated uint32 = 0x80000000

// PixFmtMJPEG is the MJPG four-character encoding identifier.
const PixFmtMJPEG = 'M' | 'J'<<8 | 'P'<<16 | 'G'<<24
