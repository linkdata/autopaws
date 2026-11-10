// Command alsaproto captures ~1s of S16_LE audio from a raw ALSA PCM device
// using only syscall ioctls (linux/amd64, linux/arm64 layout).
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"syscall"
	"unsafe"
)

// ioctl numbers for 64-bit Linux (verified against <sound/asound.h> on arm64).
const (
	ioctlHwRefine  = 0xc2604110
	ioctlHwParams  = 0xc2604111
	ioctlSwParams  = 0xc0884113
	ioctlPrepare   = 0x00004140
	ioctlStart     = 0x00004142
	ioctlDrop      = 0x00004143
	ioctlReadiFrms = 0x80184151

	paramAccess    = 0
	paramFormat    = 1
	paramSubformat = 2
	paramChannels  = 10
	paramRate      = 11
	paramPeriodSz  = 13
	paramPeriods   = 15
	paramBufferSz  = 17
	firstInterval  = 8

	accessRWInterleaved = 3
	formatS16LE         = 2
	subformatStd        = 0
)

type mask struct{ bits [8]uint32 }

type interval struct {
	min, max uint32
	flags    uint32 // bit0 openmin, bit1 openmax, bit2 integer, bit3 empty
}

// hwParams mirrors struct snd_pcm_hw_params (608 bytes).
type hwParams struct {
	flags     uint32
	masks     [3]mask
	mres      [5]mask
	intervals [12]interval
	ires      [9]interval
	rmask     uint32
	cmask     uint32
	info      uint32
	msbits    uint32
	rateNum   uint32
	rateDen   uint32
	fifoSize  uint64
	reserved  [64]byte
}

// swParams mirrors struct snd_pcm_sw_params (136 bytes).
type swParams struct {
	tstampMode       int32
	periodStep       uint32
	sleepMin         uint32
	_                uint32
	availMin         uint64
	xferAlign        uint64
	startThreshold   uint64
	stopThreshold    uint64
	silenceThreshold uint64
	silenceSize      uint64
	boundary         uint64
	proto            uint32
	tstampType       uint32
	reserved         [56]byte
}

// xferi mirrors struct snd_xferi (24 bytes).
type xferi struct {
	result int64
	buf    unsafe.Pointer
	frames uint64
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

func (p *hwParams) any() {
	for i := range p.masks {
		for j := range p.masks[i].bits {
			p.masks[i].bits[j] = ^uint32(0)
		}
	}
	for i := range p.intervals {
		p.intervals[i] = interval{0, ^uint32(0), 0}
	}
	p.rmask = ^uint32(0)
}

func (p *hwParams) setMask(param, val int) {
	m := &p.masks[param]
	*m = mask{}
	m.bits[val/32] = 1 << (val % 32)
}

func (p *hwParams) iv(param int) *interval { return &p.intervals[param-firstInterval] }

func (p *hwParams) setRange(param int, lo, hi uint32) {
	*p.iv(param) = interval{min: lo, max: hi}
}

func main() {
	dev := flag.String("d", "/dev/snd/pcmC0D0c", "capture PCM device node")
	rate := flag.Uint("r", 48000, "preferred rate (falls back to device min if unsupported)")
	flag.Parse()

	fd, err := syscall.Open(*dev, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v (errno %d)\n", *dev, err, err.(syscall.Errno))
		os.Exit(1)
	}
	defer syscall.Close(fd)
	// O_NONBLOCK on open makes a busy device fail with EBUSY instead of
	// sleeping until the holder closes it; reads are then made blocking.
	if err = syscall.SetNonblock(fd, false); err != nil {
		fmt.Fprintln(os.Stderr, "clear O_NONBLOCK:", err)
		os.Exit(1)
	}

	// Stage 1: refine with format/access fixed to learn channel/rate ranges.
	var hw hwParams
	hw.any()
	hw.setMask(paramAccess, accessRWInterleaved)
	hw.setMask(paramFormat, formatS16LE)
	hw.setMask(paramSubformat, subformatStd)
	if err = ioctl(fd, ioctlHwRefine, unsafe.Pointer(&hw)); err != nil {
		fmt.Fprintln(os.Stderr, "HW_REFINE (S16_LE interleaved):", err)
		os.Exit(1)
	}
	ch, rt := hw.iv(paramChannels), hw.iv(paramRate)
	fmt.Printf("device ranges: channels %d..%d rate %d..%d\n", ch.min, ch.max, rt.min, rt.max)

	channels := min(max(ch.min, 1), 2)
	r := uint32(*rate)
	if r < rt.min || r > rt.max {
		r = rt.min
	}
	hw.setRange(paramChannels, channels, channels)
	hw.setRange(paramRate, r, r)
	// ~20ms periods, >=4 periods; kernel picks the smallest fit in range.
	hw.setRange(paramPeriodSz, r/50, r/10)
	hw.setRange(paramPeriods, 4, 32)
	hw.rmask = ^uint32(0)
	if err = ioctl(fd, ioctlHwParams, unsafe.Pointer(&hw)); err != nil {
		fmt.Fprintln(os.Stderr, "HW_PARAMS:", err)
		os.Exit(1)
	}
	period := hw.iv(paramPeriodSz).min
	buffer := hw.iv(paramBufferSz).min
	fmt.Printf("configured: S16_LE %dch %dHz period %d buffer %d frames\n", channels, r, period, buffer)

	sw := swParams{
		availMin:       uint64(period),
		startThreshold: 1,
		stopThreshold:  uint64(buffer),
		proto:          0x20012, // SNDRV_PCM_VERSION
	}
	if err = ioctl(fd, ioctlSwParams, unsafe.Pointer(&sw)); err != nil {
		fmt.Fprintln(os.Stderr, "SW_PARAMS:", err)
		os.Exit(1)
	}
	if err = ioctl(fd, ioctlPrepare, nil); err == nil {
		err = ioctl(fd, ioctlStart, nil)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "PREPARE/START:", err)
		os.Exit(1)
	}

	frameBytes := int(channels) * 2
	buf := make([]byte, int(period)*frameBytes)
	var total uint64
	var peak int
	xruns := 0
	for total < uint64(r) {
		x := xferi{buf: unsafe.Pointer(&buf[0]), frames: uint64(period)}
		err = ioctl(fd, ioctlReadiFrms, unsafe.Pointer(&x))
		switch err {
		case nil:
		case syscall.EPIPE, syscall.ESTRPIPE: // overrun / suspend: re-prepare and restart
			xruns++
			if err = ioctl(fd, ioctlPrepare, nil); err == nil {
				err = ioctl(fd, ioctlStart, nil)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "xrun recovery:", err)
				os.Exit(1)
			}
			continue
		case syscall.EINTR, syscall.EAGAIN:
			continue
		default:
			fmt.Fprintln(os.Stderr, "READI_FRAMES:", err)
			os.Exit(1)
		}
		total += uint64(x.result)
		for i := 0; i+1 < int(x.result)*frameBytes; i += 2 {
			v := int(int16(binary.LittleEndian.Uint16(buf[i:])))
			peak = max(peak, v, -v)
		}
	}
	_ = ioctl(fd, ioctlDrop, nil)
	fmt.Printf("read %d frames (%d bytes), peak %d (%.1f dBFS), xruns %d\n",
		total, total*uint64(frameBytes), peak, dbfs(peak), xruns)
}

func dbfs(peak int) float64 {
	if peak == 0 {
		return -999
	}
	// 20*log10(peak/32768) via math.Log10.
	return 20 * math.Log10(float64(peak)/32768)
}

func init() {
	if s := unsafe.Sizeof(hwParams{}); s != 608 {
		panic(fmt.Sprint("hwParams size ", s))
	}
	if s := unsafe.Sizeof(swParams{}); s != 136 {
		panic(fmt.Sprint("swParams size ", s))
	}
	if s := unsafe.Sizeof(xferi{}); s != 24 {
		panic(fmt.Sprint("xferi size ", s))
	}
}
