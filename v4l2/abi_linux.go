//go:build linux && (amd64 || arm64)

package v4l2

import (
	"syscall"
	"unsafe"
)

// Linux UAPI mirrors for the supported LP64 little-endian architectures.
// testdata/sz.c and its recorded output pin every named field below.
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

type format struct {
	Type uint32
	_    [0]uint64 // The 200-byte union is 8-aligned, starting at offset 8.
	Pix  pixFormat
	_    [200 - 48]byte
}

type requestbuffers struct {
	Count        uint32
	Type         uint32
	Memory       uint32
	Capabilities uint32
	Flags        uint8
	_            [3]uint8
}

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
	Offset    uint32 // Low word of the 8-byte m union; both targets are LE.
	_         uint32
	Length    uint32
	_         uint32 // reserved2
	RequestFD int32
	_         uint32
}

// A mismatch fails compilation, including during arm64 cross-builds.
var (
	_ = [1]int{}[unsafe.Sizeof(capability{})-104]
	_ = [1]int{}[unsafe.Alignof(capability{})-4]
	_ = [1]int{}[unsafe.Offsetof(capability{}.Driver)-0]
	_ = [1]int{}[unsafe.Offsetof(capability{}.Card)-16]
	_ = [1]int{}[unsafe.Offsetof(capability{}.BusInfo)-48]
	_ = [1]int{}[unsafe.Offsetof(capability{}.Version)-80]
	_ = [1]int{}[unsafe.Offsetof(capability{}.Capabilities)-84]
	_ = [1]int{}[unsafe.Offsetof(capability{}.DeviceCaps)-88]
	_ = [1]int{}[unsafe.Sizeof(fmtdesc{})-64]
	_ = [1]int{}[unsafe.Alignof(fmtdesc{})-4]
	_ = [1]int{}[unsafe.Offsetof(fmtdesc{}.Index)-0]
	_ = [1]int{}[unsafe.Offsetof(fmtdesc{}.Type)-4]
	_ = [1]int{}[unsafe.Offsetof(fmtdesc{}.Flags)-8]
	_ = [1]int{}[unsafe.Offsetof(fmtdesc{}.Description)-12]
	_ = [1]int{}[unsafe.Offsetof(fmtdesc{}.PixelFormat)-44]
	_ = [1]int{}[unsafe.Offsetof(fmtdesc{}.MbusCode)-48]
	_ = [1]int{}[unsafe.Sizeof(pixFormat{})-48]
	_ = [1]int{}[unsafe.Alignof(pixFormat{})-4]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.Width)-0]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.Height)-4]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.PixelFormat)-8]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.Field)-12]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.BytesPerLine)-16]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.SizeImage)-20]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.Colorspace)-24]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.Priv)-28]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.Flags)-32]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.YcbcrEnc)-36]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.Quantization)-40]
	_ = [1]int{}[unsafe.Offsetof(pixFormat{}.XferFunc)-44]
	_ = [1]int{}[unsafe.Sizeof(format{})-208]
	_ = [1]int{}[unsafe.Alignof(format{})-8]
	_ = [1]int{}[unsafe.Offsetof(format{}.Type)-0]
	_ = [1]int{}[unsafe.Offsetof(format{}.Pix)-8]
	_ = [1]int{}[unsafe.Sizeof(requestbuffers{})-20]
	_ = [1]int{}[unsafe.Alignof(requestbuffers{})-4]
	_ = [1]int{}[unsafe.Offsetof(requestbuffers{}.Count)-0]
	_ = [1]int{}[unsafe.Offsetof(requestbuffers{}.Type)-4]
	_ = [1]int{}[unsafe.Offsetof(requestbuffers{}.Memory)-8]
	_ = [1]int{}[unsafe.Offsetof(requestbuffers{}.Capabilities)-12]
	_ = [1]int{}[unsafe.Offsetof(requestbuffers{}.Flags)-16]
	_ = [1]int{}[unsafe.Sizeof(buffer{})-88]
	_ = [1]int{}[unsafe.Alignof(buffer{})-8]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Index)-0]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Type)-4]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.BytesUsed)-8]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Flags)-12]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Field)-16]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Timestamp)-24]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Timecode)-40]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Sequence)-56]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Memory)-60]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Offset)-64]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.Length)-72]
	_ = [1]int{}[unsafe.Offsetof(buffer{}.RequestFD)-80]
	_ = [1]int{}[unsafe.Sizeof(syscall.Timeval{})-16]
	_ = [1]int{}[unsafe.Alignof(syscall.Timeval{})-8]
	_ = [1]int{}[unsafe.Offsetof(syscall.Timeval{}.Sec)-0]
	_ = [1]int{}[unsafe.Offsetof(syscall.Timeval{}.Usec)-8]
)

const (
	iocWrite = 1
	iocRead  = 2
)

func ioc(direction, number, size uintptr) uintptr {
	return direction<<30 | size<<16 | 'V'<<8 | number
}

var (
	vidiocQuerycap  = ioc(iocRead, 0, unsafe.Sizeof(capability{}))
	vidiocEnumFmt   = ioc(iocRead|iocWrite, 2, unsafe.Sizeof(fmtdesc{}))
	vidiocSFmt      = ioc(iocRead|iocWrite, 5, unsafe.Sizeof(format{}))
	vidiocReqbufs   = ioc(iocRead|iocWrite, 8, unsafe.Sizeof(requestbuffers{}))
	vidiocQuerybuf  = ioc(iocRead|iocWrite, 9, unsafe.Sizeof(buffer{}))
	vidiocQbuf      = ioc(iocRead|iocWrite, 15, unsafe.Sizeof(buffer{}))
	vidiocDqbuf     = ioc(iocRead|iocWrite, 17, unsafe.Sizeof(buffer{}))
	vidiocStreamon  = ioc(iocWrite, 18, 4)
	vidiocStreamoff = ioc(iocWrite, 19, 4)
)

const (
	bufTypeVideoCapture = 1
	memoryMmap          = 1
	maxFrames           = 32 // Linux VIDEO_MAX_FRAME bounds REQBUFS results.
	bufFlagError        = 0x00000040
	capVideoCapture     = 0x00000001
	capStreaming        = 0x04000000
	capDeviceCaps       = 0x80000000
)
