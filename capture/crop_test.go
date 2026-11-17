package capture

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
)

func lumaImage(bounds, active image.Rectangle, brightness uint8) *image.YCbCr {
	img := image.NewYCbCr(bounds, image.YCbCrSubsampleRatio420)
	for y := active.Min.Y; y < active.Max.Y; y++ {
		for x := active.Min.X; x < active.Max.X; x++ {
			img.Y[img.YOffset(x, y)] = brightness
		}
	}
	for i := range img.Cb {
		img.Cb[i] = 128
		img.Cr[i] = 128
	}
	return img
}

func TestDetectCropReferenceThresholds(t *testing.T) {
	bounds := image.Rect(0, 0, 160, 120)
	for _, tc := range []struct {
		name         string
		active, want image.Rectangle
		value        uint8
		ok           bool
	}{
		{"letterbox", image.Rect(0, 16, 160, 104), image.Rect(0, 16, 160, 104), 200, true},
		{"pillarbox", image.Rect(16, 0, 144, 120), image.Rect(16, 0, 144, 120), 200, true},
		{"both", image.Rect(12, 8, 148, 112), image.Rect(12, 8, 148, 112), 200, true},
		{"no-border", bounds, image.Rectangle{}, 200, false},
		{"all-black", bounds, image.Rectangle{}, 0, false},
		{"threshold-equal", bounds, image.Rectangle{}, 20, false},
		{"threshold-above", image.Rect(0, 4, 160, 116), image.Rect(0, 4, 160, 116), 21, true},
		{"small-content", image.Rect(40, 30, 103, 93), image.Rectangle{}, 200, false},
		{"minimum-content", image.Rect(40, 28, 104, 92), image.Rect(40, 28, 104, 92), 200, true},
		{"insignificant-border", image.Rect(3, 3, 157, 117), image.Rectangle{}, 200, false},
		{"one-significant-border", image.Rect(4, 3, 157, 117), image.Rect(4, 3, 157, 117), 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DetectCrop(lumaImage(bounds, tc.active, tc.value))
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got %v,%v want %v,%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDetectCropCountsActiveRowsAndColumns(t *testing.T) {
	img := lumaImage(image.Rect(0, 0, 160, 120), image.Rectangle{}, 0)
	// The active bounding box is large, but only 63 rows/columns are active.
	for i := range 63 {
		img.Y[img.YOffset(8+i*2, 8+i)] = 200
	}
	if _, ok := DetectCrop(img); ok {
		t.Fatal("bounding-box span replaced active row/column count")
	}
}

func TestDetectCropCoordinatesAndOtherJPEGImages(t *testing.T) {
	bounds := image.Rect(-8, 12, 168, 140)
	active := image.Rect(8, 28, 152, 124)
	parent := lumaImage(bounds, active, 200)
	img := parent.SubImage(image.Rect(-4, 16, 164, 136)).(*image.YCbCr)
	if got, ok := DetectCrop(img); !ok || got != active {
		t.Fatalf("subimage: %v,%v", got, ok)
	}
	gray := image.NewGray(bounds)
	draw.Draw(gray, active, image.NewUniform(color.Gray{Y: 200}), image.Point{}, draw.Src)
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, active, image.White, image.Point{}, draw.Src)
	for _, img := range []image.Image{gray, rgba} {
		if got, ok := DetectCrop(img); !ok || got != active {
			t.Fatalf("%T: %v,%v", img, got, ok)
		}
	}
	if got, ok := DetectCrop(image.NewGray(image.Rectangle{})); ok || !got.Empty() {
		t.Fatal("empty image has crop")
	}
}

func BenchmarkDetectCrop(b *testing.B) {
	img := lumaImage(image.Rect(0, 0, 1920, 1080), image.Rect(0, 140, 1920, 940), 160)
	b.ReportAllocs()
	for b.Loop() {
		DetectCrop(img)
	}
}
