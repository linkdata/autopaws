package v4l2

import (
	"bytes"
	"image"
	"image/jpeg"
	"sync"
)

// stdDHT returns the DHT segment holding the JPEG Annex K.3 tables, lifted
// from image/jpeg's own encoder output (it always writes the standard
// tables), so no table bytes are hand-typed.
var stdDHT = sync.OnceValue(func() []byte {
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, image.NewYCbCr(image.Rect(0, 0, 8, 8), image.YCbCrSubsampleRatio420), nil)
	b := buf.Bytes()
	for i := 2; i+4 <= len(b); {
		n := int(b[i+2])<<8 | int(b[i+3])
		if b[i+1] == 0xC4 {
			return append([]byte(nil), b[i:i+2+n]...)
		}
		i += 2 + n
	}
	panic("no DHT in image/jpeg output")
})

// FixMJPEG trims bytes after the last EOI and inserts standard Huffman
// tables before SOS when the frame has no DHT (common for UVC MJPEG).
// Frames that do not parse as JPEG are returned unchanged.
func FixMJPEG(b []byte) []byte {
	if i := bytes.LastIndex(b, []byte{0xFF, 0xD9}); i >= 0 {
		b = b[:i+2]
	}
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return b
	}
	for i := 2; i+4 <= len(b) && b[i] == 0xFF; {
		switch b[i+1] {
		case 0xC4: // DHT present
			return b
		case 0xDA: // SOS without prior DHT
			out := make([]byte, 0, len(b)+len(stdDHT()))
			return append(append(append(out, b[:i]...), stdDHT()...), b[i:]...)
		}
		i += 2 + (int(b[i+2])<<8 | int(b[i+3]))
	}
	return b
}

// Crop thresholds, as in capture.py.
const (
	cropThreshold  = 20
	cropMinBorder  = 4
	cropMinContent = 64
)

// DetectCrop finds black borders using the luma plane (capture.py
// _detect_crop_rect, minus the BGR->gray step since JPEG decodes to Y'CbCr).
// ok is false when no significant border exists.
func DetectCrop(m *image.YCbCr) (r image.Rectangle, ok bool) {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	rowMax := make([]uint8, h)
	colMax := make([]uint8, w)
	for y := range h {
		row := m.Y[y*m.YStride : y*m.YStride+w]
		for x, v := range row {
			rowMax[y] = max(rowMax[y], v)
			colMax[x] = max(colMax[x], v)
		}
	}
	span := func(a []uint8) (lo, hi, n int) {
		lo = -1
		for i, v := range a {
			if v > cropThreshold {
				if lo < 0 {
					lo = i
				}
				hi = i + 1
				n++
			}
		}
		return
	}
	y1, y2, ny := span(rowMax)
	x1, x2, nx := span(colMax)
	if ny < cropMinContent || nx < cropMinContent {
		return
	}
	if y1 < cropMinBorder && h-y2 < cropMinBorder && x1 < cropMinBorder && w-x2 < cropMinBorder {
		return
	}
	return image.Rect(x1, y1, x2, y2).Add(m.Rect.Min), true
}
