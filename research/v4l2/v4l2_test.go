//go:build linux && (amd64 || arm64)

package v4l2

import (
	"bytes"
	"image"
	"image/jpeg"
	"math/rand/v2"
	"testing"
)

// Values printed by gcc from /usr/include/linux/videodev2.h (arm64).
func TestIoctlNumbers(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"QUERYCAP", vidiocQuerycap, 0x80685600},
		{"ENUM_FMT", vidiocEnumFmt, 0xc0405602},
		{"G_FMT", vidiocGFmt, 0xc0d05604},
		{"S_FMT", vidiocSFmt, 0xc0d05605},
		{"G_PARM", vidiocGParm, 0xc0cc5615},
		{"S_PARM", vidiocSParm, 0xc0cc5616},
		{"REQBUFS", vidiocReqbufs, 0xc0145608},
		{"QUERYBUF", vidiocQuerybuf, 0xc0585609},
		{"QBUF", vidiocQbuf, 0xc058560f},
		{"DQBUF", vidiocDqbuf, 0xc0585611},
		{"STREAMON", vidiocStreamon, 0x40045612},
		{"STREAMOFF", vidiocStreamoff, 0x40045613},
		{"ENUM_FRAMESIZES", vidiocEnumFramesizes, 0xc02c564a},
		{"ENUM_FRAMEINTERVALS", vidiocEnumFrameivals, 0xc034564b},
		{"PIX_FMT_MJPEG", PixFmtMJPEG, 0x47504a4d},
		{"PIX_FMT_YUYV", PixFmtYUYV, 0x56595559},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// synth makes a 1080p frame: flat "desktop" with a black letterbox and
// some noisy "text" blocks.
func synth(noise bool) *image.YCbCr {
	m := image.NewYCbCr(image.Rect(0, 0, 1920, 1080), image.YCbCrSubsampleRatio420)
	r := rand.New(rand.NewPCG(1, 2))
	for y := range 1080 {
		for x := range 1920 {
			v := uint8(0)
			if x >= 240 && x < 1680 {
				v = uint8(60 + x/16)
				if noise || (y/40)%3 == 0 && (x/8)%2 == 0 {
					v = uint8(r.IntN(256))
				}
			}
			m.Y[y*m.YStride+x] = v
		}
	}
	for i := range m.Cb {
		m.Cb[i], m.Cr[i] = 128, 128
	}
	return m
}

func encode(t testing.TB, m image.Image) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, m, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func stripDHT(b []byte) []byte {
	out := append([]byte(nil), b[:2]...)
	for i := 2; ; {
		n := int(b[i+2])<<8 | int(b[i+3])
		if b[i+1] == 0xDA {
			return append(out, b[i:]...)
		}
		if b[i+1] != 0xC4 {
			out = append(out, b[i:i+2+n]...)
		}
		i += 2 + n
	}
}

func TestFixMJPEG(t *testing.T) {
	orig := encode(t, synth(false))
	bare := append(stripDHT(orig), 0, 0, 0, 0) // no DHT + trailing padding
	if _, err := jpeg.Decode(bytes.NewReader(bare)); err == nil {
		t.Fatal("image/jpeg decoded a DHT-less frame; expected uninitialized Huffman table error")
	} else {
		t.Log("DHT-less decode error:", err)
	}
	fixed := FixMJPEG(bare)
	if !bytes.Equal(fixed, orig) {
		t.Fatalf("fixed frame differs from original (len %d vs %d)", len(fixed), len(orig))
	}
	if got := FixMJPEG(orig); !bytes.Equal(got, orig) {
		t.Fatal("frame with DHT was modified")
	}
}

func TestDetectCrop(t *testing.T) {
	r, ok := DetectCrop(synth(false))
	if want := image.Rect(240, 0, 1680, 1080); !ok || r != want {
		t.Fatalf("got %v %v, want %v", r, ok, want)
	}
	full := image.NewYCbCr(image.Rect(0, 0, 640, 480), image.YCbCrSubsampleRatio420)
	for i := range full.Y {
		full.Y[i] = 200
	}
	if _, ok := DetectCrop(full); ok {
		t.Fatal("crop on borderless frame")
	}
}

func bench(b *testing.B, noise bool) {
	m := synth(noise)
	enc := encode(b, m)
	b.Logf("jpeg size %d bytes", len(enc))
	b.Run("EncodeQ85", func(b *testing.B) {
		for b.Loop() {
			encode(b, m)
		}
	})
	b.Run("Decode", func(b *testing.B) {
		for b.Loop() {
			if _, err := jpeg.Decode(bytes.NewReader(enc)); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("DetectCrop", func(b *testing.B) {
		for b.Loop() {
			DetectCrop(m)
		}
	})
	b.Run("CropReencode", func(b *testing.B) {
		for b.Loop() {
			d, _ := jpeg.Decode(bytes.NewReader(enc))
			encode(b, d.(*image.YCbCr).SubImage(image.Rect(240, 0, 1680, 1080)))
		}
	})
}

func BenchmarkSmooth(b *testing.B) { bench(b, false) }
func BenchmarkNoise(b *testing.B)  { bench(b, true) }

// BenchmarkYUYVEncode measures the YUYV fallback: pack 4:2:2 into
// image.YCbCr and JPEG-encode.
func BenchmarkYUYVEncode(b *testing.B) {
	src := synth(false)
	yuyv := make([]byte, 1920*1080*2)
	for i := 0; i < len(yuyv); i += 4 {
		p := i / 2
		yuyv[i], yuyv[i+1], yuyv[i+2], yuyv[i+3] = src.Y[p], 128, src.Y[p+1], 128
	}
	m := image.NewYCbCr(image.Rect(0, 0, 1920, 1080), image.YCbCrSubsampleRatio422)
	for b.Loop() {
		for i, j := 0, 0; i < len(yuyv); i, j = i+4, j+1 {
			m.Y[2*j], m.Cb[j], m.Y[2*j+1], m.Cr[j] = yuyv[i], yuyv[i+1], yuyv[i+2], yuyv[i+3]
		}
		encode(b, m)
	}
}

func TestOpenNotV4L2(t *testing.T) {
	if d, err := Open("/dev/null", PixFmtMJPEG, 1920, 1080, 30, 4); err == nil {
		d.Close()
		t.Fatal("opened /dev/null")
	} else {
		t.Log(err)
	}
	if l := List(); len(l) != 0 {
		t.Log(l)
	}
}
