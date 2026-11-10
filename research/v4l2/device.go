//go:build linux && (amd64 || arm64)

package v4l2

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ErrNotCapture is returned by [Open] for nodes that are not streaming
// video-capture devices (e.g. UVC metadata nodes).
var ErrNotCapture = errors.New("v4l2: not a streaming video capture device")

// Device is an open V4L2 capture device streaming via MMAP buffers.
//
// ReadFrame must be called from one goroutine; Close may be called
// concurrently with it and unblocks it.
type Device struct {
	f      *os.File
	rc     syscall.RawConn
	mu     sync.Mutex // guards bufs against Close during ReadFrame
	bufs   [][]byte
	closed bool
	Width  uint32
	Height uint32
	PixFmt uint32
}

// ctl runs a non-blocking ioctl on the device fd.
func (d *Device) ctl(req uintptr, arg unsafe.Pointer) (err error) {
	if cerr := d.rc.Control(func(fd uintptr) { err = ioctl(fd, req, arg) }); cerr != nil {
		err = cerr
	}
	return
}

// Open opens path, negotiates pixFmt at width x height (driver may adjust;
// see the Width/Height/PixFmt fields), optionally sets fps, maps nbuf
// buffers and starts streaming.
func Open(path string, pixFmt, width, height, fps, nbuf uint32) (d *Device, err error) {
	// os.OpenFile puts the char device in non-blocking mode and registers it
	// with the runtime netpoller, so DQBUF waits via epoll, honours
	// deadlines and is unblocked by Close.
	var f *os.File
	if f, err = os.OpenFile(path, os.O_RDWR, 0); err == nil {
		d = &Device{f: f}
		if d.rc, err = f.SyscallConn(); err == nil {
			if err = d.setup(pixFmt, width, height, fps, nbuf); err == nil {
				return
			}
		}
		_ = d.Close()
		d = nil
	}
	return
}

func (d *Device) setup(pixFmt, width, height, fps, nbuf uint32) (err error) {
	var c capability
	if err = d.ctl(vidiocQuerycap, unsafe.Pointer(&c)); err == nil {
		caps := c.Capabilities
		if caps&capDeviceCaps != 0 {
			caps = c.DeviceCaps
		}
		if caps&(capVideoCapture|capStreaming) != capVideoCapture|capStreaming {
			return ErrNotCapture
		}
		f := format{Type: bufTypeVideoCapture}
		f.Pix.Width, f.Pix.Height, f.Pix.PixelFormat = width, height, pixFmt
		if err = d.ctl(vidiocSFmt, unsafe.Pointer(&f)); err == nil {
			d.Width, d.Height, d.PixFmt = f.Pix.Width, f.Pix.Height, f.Pix.PixelFormat
			if fps > 0 {
				p := streamparm{Type: bufTypeVideoCapture, TimePerFrame: [2]uint32{1, fps}}
				_ = d.ctl(vidiocSParm, unsafe.Pointer(&p)) // best effort; many UVC HDMI cards ignore it
			}
			rb := requestbuffers{Count: nbuf, Type: bufTypeVideoCapture, Memory: memoryMmap}
			if err = d.ctl(vidiocReqbufs, unsafe.Pointer(&rb)); err == nil {
				for i := uint32(0); i < rb.Count && err == nil; i++ {
					b := buffer{Index: i, Type: bufTypeVideoCapture, Memory: memoryMmap}
					if err = d.ctl(vidiocQuerybuf, unsafe.Pointer(&b)); err == nil {
						var m []byte
						if cerr := d.rc.Control(func(fd uintptr) {
							m, err = syscall.Mmap(int(fd), int64(b.Offset), int(b.Length), syscall.PROT_READ, syscall.MAP_SHARED)
						}); cerr != nil {
							err = cerr
						}
						if err == nil {
							d.bufs = append(d.bufs, m)
							err = d.ctl(vidiocQbuf, unsafe.Pointer(&b))
						}
					}
				}
				if err == nil {
					t := int32(bufTypeVideoCapture)
					err = d.ctl(vidiocStreamon, unsafe.Pointer(&t))
				}
			}
		}
	}
	return
}

// ReadFrame blocks until a frame is available and returns a copy of its
// bytes (bytesused, not the full buffer). Frames flagged as errored by the
// driver are skipped.
func (d *Device) ReadFrame() (frame []byte, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for frame == nil && err == nil {
		if d.closed {
			return nil, os.ErrClosed
		}
		b := buffer{Type: bufTypeVideoCapture, Memory: memoryMmap}
		var ierr error
		if err = d.rc.Read(func(fd uintptr) bool {
			ierr = ioctl(fd, vidiocDqbuf, unsafe.Pointer(&b))
			return ierr != syscall.EAGAIN
		}); err == nil {
			if err = ierr; err == nil {
				if b.Flags&bufFlagError == 0 && b.BytesUsed > 0 && int(b.Index) < len(d.bufs) {
					frame = append([]byte(nil), d.bufs[b.Index][:b.BytesUsed]...)
				}
				err = d.ctl(vidiocQbuf, unsafe.Pointer(&b))
			}
		}
	}
	return
}

// Close stops streaming, unmaps buffers and closes the device.
func (d *Device) Close() (err error) {
	_ = d.f.SetReadDeadline(time.Unix(1, 0)) // wake a blocked ReadFrame
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.closed = true
		if d.rc != nil {
			t := int32(bufTypeVideoCapture)
			_ = d.ctl(vidiocStreamoff, unsafe.Pointer(&t))
		}
		for _, m := range d.bufs {
			err = errors.Join(err, syscall.Munmap(m))
		}
		d.bufs = nil
		err = errors.Join(err, d.f.Close())
	}
	return
}

// FrameSize is a discrete frame size with its discrete frame rates (fps).
type FrameSize struct {
	Width, Height uint32
	FPS           []float64
}

// Formats lists pixel formats and, for each, its discrete frame sizes.
// Stepwise/continuous sizes are ignored.
func (d *Device) Formats() (out map[uint32][]FrameSize, err error) {
	out = map[uint32][]FrameSize{}
	for i := uint32(0); ; i++ {
		fd := fmtdesc{Index: i, Type: bufTypeVideoCapture}
		if d.ctl(vidiocEnumFmt, unsafe.Pointer(&fd)) != nil {
			break // EINVAL terminates enumeration
		}
		var sizes []FrameSize
		for j := uint32(0); ; j++ {
			fs := frmsizeenum{Index: j, PixelFormat: fd.PixelFormat}
			if d.ctl(vidiocEnumFramesizes, unsafe.Pointer(&fs)) != nil || fs.Type != frmsizeTypeDiscrete {
				break
			}
			s := FrameSize{Width: fs.U[0], Height: fs.U[1]}
			for k := uint32(0); ; k++ {
				fi := frmivalenum{Index: k, PixelFormat: fd.PixelFormat, Width: s.Width, Height: s.Height}
				if d.ctl(vidiocEnumFrameivals, unsafe.Pointer(&fi)) != nil || fi.Type != frmsizeTypeDiscrete || fi.U[0] == 0 {
					break
				}
				s.FPS = append(s.FPS, float64(fi.U[1])/float64(fi.U[0]))
			}
			sizes = append(sizes, s)
		}
		out[fd.PixelFormat] = sizes
	}
	return
}
