//go:build linux && (amd64 || arm64)

// Package v4l2 is a feasibility prototype for V4L2 MMAP streaming capture
// using only the standard library (syscall + os).
package v4l2

import (
	"syscall"
	"unsafe"
)

// Kernel ABI mirrors of <linux/videodev2.h> for LP64 (amd64, arm64).
// Sizes are asserted at compile time below and match gcc sizeof on arm64.

type capability struct {
	Driver       [16]byte
	Card         [32]byte
	BusInfo      [32]byte
	Version      uint32
	Capabilities uint32
	DeviceCaps   uint32
	_            [3]uint32
}

type fmtdesc struct {
	Index       uint32
	Type        uint32
	Flags       uint32
	Description [32]byte
	PixelFormat uint32
	MbusCode    uint32
	_           [3]uint32
}

// frmsizeenum: union {discrete{w,h}; stepwise{6 x u32}} is 24 bytes.
type frmsizeenum struct {
	Index       uint32
	PixelFormat uint32
	Type        uint32
	U           [6]uint32 // discrete: U[0]=width U[1]=height
	_           [2]uint32
}

// frmivalenum: union {discrete fract; stepwise{3 x fract}} is 24 bytes.
type frmivalenum struct {
	Index       uint32
	PixelFormat uint32
	Width       uint32
	Height      uint32
	Type        uint32
	U           [6]uint32 // discrete: U[0]=numerator U[1]=denominator
	_           [2]uint32
}

type pixFormat struct {
	Width        uint32
	Height       uint32
	PixelFormat  uint32
	Field        uint32
	BytesPerLine uint32
	SizeImage    uint32
	Colorspace   uint32
	Priv         uint32
	Flags        uint32
	YcbcrEnc     uint32
	Quantization uint32
	XferFunc     uint32
}

// format: the fmt union is 200 bytes and 8-aligned on LP64 (v4l2_window
// holds pointers), so it starts at offset 8, not 4. Total 208.
type format struct {
	Type uint32
	_    uint32
	Pix  pixFormat
	_    [200 - 48]byte
}

// streamparm: parm union is 200 bytes, 4-aligned (no pointers). Total 204.
type streamparm struct {
	Type         uint32
	Capability   uint32
	CaptureMode  uint32
	TimePerFrame [2]uint32 // numerator, denominator
	ExtendedMode uint32
	ReadBuffers  uint32
	_            [4]uint32
	_            [200 - 40]byte
}

type requestbuffers struct {
	Count        uint32
	Type         uint32
	Memory       uint32
	Capabilities uint32
	Flags        uint8
	_            [3]uint8
}

// buffer: struct timeval forces 8-alignment => 4 pad bytes after Field and
// 4 tail pad bytes. The m union is 8 bytes; Offset is its low word (LE).
type buffer struct {
	Index     uint32
	Type      uint32
	BytesUsed uint32
	Flags     uint32
	Field     uint32
	_         uint32
	Timestamp syscall.Timeval
	Timecode  [16]byte
	Sequence  uint32
	Memory    uint32
	Offset    uint32 // m.offset
	_         uint32 // rest of m (userptr/planes are 8 bytes)
	Length    uint32
	_         uint32 // reserved2
	RequestFD int32
	_         uint32
}

// Compile-time layout checks: out-of-range constant index if a size is off.
var (
	_ = [1]int{}[unsafe.Sizeof(capability{})-104]
	_ = [1]int{}[unsafe.Sizeof(fmtdesc{})-64]
	_ = [1]int{}[unsafe.Sizeof(frmsizeenum{})-44]
	_ = [1]int{}[unsafe.Sizeof(frmivalenum{})-52]
	_ = [1]int{}[unsafe.Sizeof(format{})-208]
	_ = [1]int{}[unsafe.Offsetof(format{}.Pix)-8]
	_ = [1]int{}[unsafe.Sizeof(streamparm{})-204]
	_ = [1]int{}[unsafe.Sizeof(requestbuffers{})-20]
	_ = [1]int{}[unsafe.Sizeof(buffer{})-88]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Timestamp)-24]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Sequence)-56]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Offset)-64]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Length)-72]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.RequestFD)-80]
)

// asm-generic/ioctl.h encoding (used by both x86 and arm64).
const (
	iocWrite = 1
	iocRead  = 2
)

func ioc(dir, nr, size uintptr) uintptr { return dir<<30 | size<<16 | 'V'<<8 | nr }

var (
	vidiocQuerycap       = ioc(iocRead, 0, unsafe.Sizeof(capability{}))
	vidiocEnumFmt        = ioc(iocRead|iocWrite, 2, unsafe.Sizeof(fmtdesc{}))
	vidiocGFmt           = ioc(iocRead|iocWrite, 4, unsafe.Sizeof(format{}))
	vidiocSFmt           = ioc(iocRead|iocWrite, 5, unsafe.Sizeof(format{}))
	vidiocReqbufs        = ioc(iocRead|iocWrite, 8, unsafe.Sizeof(requestbuffers{}))
	vidiocQuerybuf       = ioc(iocRead|iocWrite, 9, unsafe.Sizeof(buffer{}))
	vidiocQbuf           = ioc(iocRead|iocWrite, 15, unsafe.Sizeof(buffer{}))
	vidiocDqbuf          = ioc(iocRead|iocWrite, 17, unsafe.Sizeof(buffer{}))
	vidiocStreamon       = ioc(iocWrite, 18, 4)
	vidiocStreamoff      = ioc(iocWrite, 19, 4)
	vidiocGParm          = ioc(iocRead|iocWrite, 21, unsafe.Sizeof(streamparm{}))
	vidiocSParm          = ioc(iocRead|iocWrite, 22, unsafe.Sizeof(streamparm{}))
	vidiocEnumFramesizes = ioc(iocRead|iocWrite, 74, unsafe.Sizeof(frmsizeenum{}))
	vidiocEnumFrameivals = ioc(iocRead|iocWrite, 75, unsafe.Sizeof(frmivalenum{}))
)

const (
	bufTypeVideoCapture = 1
	memoryMmap          = 1
	bufFlagError        = 0x40
	frmsizeTypeDiscrete = 1
	capVideoCapture     = 0x00000001
	capStreaming        = 0x04000000
	capDeviceCaps       = 0x80000000

	// PixFmtMJPEG and PixFmtYUYV are V4L2 fourcc pixel formats.
	PixFmtMJPEG = 'M' | 'J'<<8 | 'P'<<16 | 'G'<<24
	PixFmtYUYV  = 'Y' | 'U'<<8 | 'Y'<<16 | 'V'<<24
)

// ioctl issues one ioctl, retrying on EINTR.
func ioctl(fd, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
		if e != syscall.EINTR {
			if e != 0 {
				return e
			}
			return nil
		}
	}
}
