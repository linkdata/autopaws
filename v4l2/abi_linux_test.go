//go:build linux && (amd64 || arm64)

package v4l2

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestABIFromLinuxHeaders(t *testing.T) {
	got := map[string]uintptr{
		"v4l2_capability.size":              unsafe.Sizeof(capability{}),
		"v4l2_capability.align":             unsafe.Alignof(capability{}),
		"v4l2_capability.driver":            unsafe.Offsetof(capability{}.Driver),
		"v4l2_capability.card":              unsafe.Offsetof(capability{}.Card),
		"v4l2_capability.bus_info":          unsafe.Offsetof(capability{}.BusInfo),
		"v4l2_capability.version":           unsafe.Offsetof(capability{}.Version),
		"v4l2_capability.capabilities":      unsafe.Offsetof(capability{}.Capabilities),
		"v4l2_capability.device_caps":       unsafe.Offsetof(capability{}.DeviceCaps),
		"v4l2_fmtdesc.size":                 unsafe.Sizeof(fmtdesc{}),
		"v4l2_fmtdesc.align":                unsafe.Alignof(fmtdesc{}),
		"v4l2_fmtdesc.index":                unsafe.Offsetof(fmtdesc{}.Index),
		"v4l2_fmtdesc.type":                 unsafe.Offsetof(fmtdesc{}.Type),
		"v4l2_fmtdesc.flags":                unsafe.Offsetof(fmtdesc{}.Flags),
		"v4l2_fmtdesc.description":          unsafe.Offsetof(fmtdesc{}.Description),
		"v4l2_fmtdesc.pixelformat":          unsafe.Offsetof(fmtdesc{}.PixelFormat),
		"v4l2_fmtdesc.mbus_code":            unsafe.Offsetof(fmtdesc{}.MbusCode),
		"v4l2_pix_format.size":              unsafe.Sizeof(pixFormat{}),
		"v4l2_pix_format.align":             unsafe.Alignof(pixFormat{}),
		"v4l2_pix_format.width":             unsafe.Offsetof(pixFormat{}.Width),
		"v4l2_pix_format.height":            unsafe.Offsetof(pixFormat{}.Height),
		"v4l2_pix_format.pixelformat":       unsafe.Offsetof(pixFormat{}.PixelFormat),
		"v4l2_pix_format.field":             unsafe.Offsetof(pixFormat{}.Field),
		"v4l2_pix_format.bytesperline":      unsafe.Offsetof(pixFormat{}.BytesPerLine),
		"v4l2_pix_format.sizeimage":         unsafe.Offsetof(pixFormat{}.SizeImage),
		"v4l2_pix_format.colorspace":        unsafe.Offsetof(pixFormat{}.Colorspace),
		"v4l2_pix_format.priv":              unsafe.Offsetof(pixFormat{}.Priv),
		"v4l2_pix_format.flags":             unsafe.Offsetof(pixFormat{}.Flags),
		"v4l2_pix_format.ycbcr_enc":         unsafe.Offsetof(pixFormat{}.YcbcrEnc),
		"v4l2_pix_format.quantization":      unsafe.Offsetof(pixFormat{}.Quantization),
		"v4l2_pix_format.xfer_func":         unsafe.Offsetof(pixFormat{}.XferFunc),
		"v4l2_format.size":                  unsafe.Sizeof(format{}),
		"v4l2_format.align":                 unsafe.Alignof(format{}),
		"v4l2_format.type":                  unsafe.Offsetof(format{}.Type),
		"v4l2_format.fmt":                   unsafe.Offsetof(format{}.Pix),
		"v4l2_requestbuffers.size":          unsafe.Sizeof(requestbuffers{}),
		"v4l2_requestbuffers.align":         unsafe.Alignof(requestbuffers{}),
		"v4l2_requestbuffers.count":         unsafe.Offsetof(requestbuffers{}.Count),
		"v4l2_requestbuffers.type":          unsafe.Offsetof(requestbuffers{}.Type),
		"v4l2_requestbuffers.memory":        unsafe.Offsetof(requestbuffers{}.Memory),
		"v4l2_requestbuffers.capabilities":  unsafe.Offsetof(requestbuffers{}.Capabilities),
		"v4l2_requestbuffers.flags":         unsafe.Offsetof(requestbuffers{}.Flags),
		"v4l2_buffer.size":                  unsafe.Sizeof(buffer{}),
		"v4l2_buffer.align":                 unsafe.Alignof(buffer{}),
		"v4l2_buffer.index":                 unsafe.Offsetof(buffer{}.Index),
		"v4l2_buffer.type":                  unsafe.Offsetof(buffer{}.Type),
		"v4l2_buffer.bytesused":             unsafe.Offsetof(buffer{}.BytesUsed),
		"v4l2_buffer.flags":                 unsafe.Offsetof(buffer{}.Flags),
		"v4l2_buffer.field":                 unsafe.Offsetof(buffer{}.Field),
		"v4l2_buffer.timestamp":             unsafe.Offsetof(buffer{}.Timestamp),
		"v4l2_buffer.timecode":              unsafe.Offsetof(buffer{}.Timecode),
		"v4l2_buffer.sequence":              unsafe.Offsetof(buffer{}.Sequence),
		"v4l2_buffer.memory":                unsafe.Offsetof(buffer{}.Memory),
		"v4l2_buffer.m":                     unsafe.Offsetof(buffer{}.Offset),
		"v4l2_buffer.length":                unsafe.Offsetof(buffer{}.Length),
		"v4l2_buffer.request_fd":            unsafe.Offsetof(buffer{}.RequestFD),
		"timeval.size":                      unsafe.Sizeof(syscall.Timeval{}),
		"timeval.align":                     unsafe.Alignof(syscall.Timeval{}),
		"timeval.tv_sec":                    unsafe.Offsetof(syscall.Timeval{}.Sec),
		"timeval.tv_usec":                   unsafe.Offsetof(syscall.Timeval{}.Usec),
		"VIDIOC_QUERYCAP":                   vidiocQuerycap,
		"VIDIOC_ENUM_FMT":                   vidiocEnumFmt,
		"VIDIOC_S_FMT":                      vidiocSFmt,
		"VIDIOC_REQBUFS":                    vidiocReqbufs,
		"VIDIOC_QUERYBUF":                   vidiocQuerybuf,
		"VIDIOC_QBUF":                       vidiocQbuf,
		"VIDIOC_DQBUF":                      vidiocDqbuf,
		"VIDIOC_STREAMON":                   vidiocStreamon,
		"VIDIOC_STREAMOFF":                  vidiocStreamoff,
		"V4L2_BUF_TYPE_VIDEO_CAPTURE":       bufTypeVideoCapture,
		"V4L2_MEMORY_MMAP":                  memoryMmap,
		"VIDEO_MAX_FRAME":                   maxFrames,
		"V4L2_BUF_FLAG_ERROR":               bufFlagError,
		"V4L2_CAP_VIDEO_CAPTURE":            capVideoCapture,
		"V4L2_CAP_STREAMING":                capStreaming,
		"V4L2_CAP_DEVICE_CAPS":              capDeviceCaps,
		"V4L2_BUF_FLAG_TIMESTAMP_MASK":      uintptr(TimestampMask),
		"V4L2_BUF_FLAG_TIMESTAMP_UNKNOWN":   uintptr(TimestampUnknown),
		"V4L2_BUF_FLAG_TIMESTAMP_MONOTONIC": uintptr(TimestampMonotonic),
		"V4L2_BUF_FLAG_TIMESTAMP_COPY":      uintptr(TimestampCopy),
		"V4L2_BUF_FLAG_TSTAMP_SRC_MASK":     uintptr(TimestampSourceMask),
		"V4L2_BUF_FLAG_TSTAMP_SRC_EOF":      uintptr(TimestampEndOfFrame),
		"V4L2_BUF_FLAG_TSTAMP_SRC_SOE":      uintptr(TimestampStartOfExposure),
		"V4L2_PIX_FMT_MJPEG":                PixFmtMJPEG,
	}
	data, err := os.ReadFile("testdata/abi-linux-amd64.txt")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(got) {
		t.Fatalf("C fixture has %d measurements, Go has %d", len(lines), len(got))
	}
	for _, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		want, err := strconv.ParseUint(value, 0, 64)
		if !ok || err != nil {
			t.Fatalf("bad C fixture line %q: %v", line, err)
		}
		actual, exists := got[key]
		if !exists || uint64(actual) != want {
			t.Errorf("%s = %#x; C measured %#x (Go field present: %t)", key, actual, want, exists)
		}
		delete(got, key)
	}
	for key := range got {
		t.Errorf("Go measurement %q is missing from the C fixture", key)
	}
}
