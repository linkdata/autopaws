package capture

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
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

func TestDetectCropPreservesDarkScreenContent(t *testing.T) {
	bounds := image.Rect(0, 0, 160, 120)
	for _, tc := range []struct {
		name   string
		active image.Rectangle
	}{
		{"centered-window", image.Rect(40, 30, 120, 90)},
		{"single-dark-edge", image.Rect(16, 0, 160, 120)},
		{"letterboxed-video", image.Rect(0, 16, 160, 104)},
		{"black-desktop", image.Rectangle{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := DetectCrop(lumaImage(bounds, tc.active, 200), 160, 120); ok || !got.Empty() {
				t.Fatalf("matching-aspect screen content cropped to %v", got)
			}
		})
	}
}

func TestDetectCropUsesTargetGeometryWithJPEG(t *testing.T) {
	bounds := image.Rect(0, 0, 1920, 1080)
	want := image.Rect(285, 0, 1635, 1080)
	img := lumaImage(bounds, want, 200)
	var data bytes.Buffer
	if err := jpeg.Encode(&data, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range []image.Image{img, decoded} {
		if got, ok := DetectCrop(img, 1280, 1024); !ok || got != want {
			t.Fatalf("%T: got %v,%v want %v,true", img, got, ok, want)
		}
	}
}

func TestDetectCropChecksOutsideTolerance(t *testing.T) {
	bounds := image.Rect(0, 0, 160, 120)
	for _, tc := range []struct {
		name                      string
		screenWidth, screenHeight int
		want                      image.Rectangle
		point                     image.Point
		value                     uint8
		ok                        bool
	}{
		{"left-tolerance", 1, 1, image.Rect(20, 0, 140, 120), image.Pt(12, 60), 255, true},
		{"left-outside", 1, 1, image.Rect(20, 0, 140, 120), image.Pt(11, 60), 21, false},
		{"right-tolerance", 1, 1, image.Rect(20, 0, 140, 120), image.Pt(147, 60), 255, true},
		{"right-outside", 1, 1, image.Rect(20, 0, 140, 120), image.Pt(148, 60), 21, false},
		{"top-tolerance", 2, 1, image.Rect(0, 20, 160, 100), image.Pt(80, 12), 255, true},
		{"top-outside", 2, 1, image.Rect(0, 20, 160, 100), image.Pt(80, 11), 21, false},
		{"bottom-tolerance", 2, 1, image.Rect(0, 20, 160, 100), image.Pt(80, 107), 255, true},
		{"bottom-outside", 2, 1, image.Rect(0, 20, 160, 100), image.Pt(80, 108), 21, false},
		{"black-threshold", 1, 1, image.Rect(20, 0, 140, 120), image.Pt(0, 60), 20, true},
		{"unverifiable-narrow-bars", 19, 15, image.Rect(4, 0, 156, 120), image.Pt(0, 60), 255, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := lumaImage(bounds, tc.want, 200)
			img.Y[img.YOffset(tc.point.X, tc.point.Y)] = tc.value
			got, ok := DetectCrop(img, tc.screenWidth, tc.screenHeight)
			if ok != tc.ok || ok && got != tc.want || !ok && !got.Empty() {
				t.Fatalf("got %v,%v want crop=%v (%v)", got, ok, tc.ok, tc.want)
			}
		})
	}
}

func TestDetectCropGeometryLimits(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name                      string
		screenWidth, screenHeight int
		want                      image.Rectangle
	}{
		{"missing-width", 0, 120, image.Rectangle{}},
		{"missing-height", 160, 0, image.Rectangle{}},
		{"missing-geometry", 0, 0, image.Rectangle{}},
		{"three-pixel-bars", 160, 114, image.Rectangle{}},
		{"odd-seven-pixel-letterbox", 160, 113, image.Rectangle{}},
		{"odd-seven-pixel-pillarbox", 153, 120, image.Rectangle{}},
		{"four-pixel-bars", 160, 112, image.Rectangle{}},
		{"eight-pixel-bars", 160, 104, image.Rectangle{}},
		{"nine-pixel-bars", 160, 102, image.Rect(0, 9, 160, 111)},
		{"large-square", maxInt, maxInt, image.Rect(20, 0, 140, 120)},
		{"large-wide", maxInt - 1, maxInt / 2, image.Rect(0, 20, 160, 100)},
		{"extremely-wide", maxInt, 1, image.Rectangle{}},
		{"extremely-tall", 1, maxInt, image.Rectangle{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := lumaImage(image.Rect(0, 0, 160, 120), image.Rectangle{}, 0)
			got, ok := DetectCrop(img, tc.screenWidth, tc.screenHeight)
			if got != tc.want || ok != !tc.want.Empty() {
				t.Fatalf("got %v,%v want %v,%v", got, ok, tc.want, !tc.want.Empty())
			}
		})
	}
}

func TestDetectCropCoordinatesAndOtherJPEGImages(t *testing.T) {
	bounds := image.Rect(-8, 12, 168, 140)
	frame := image.Rect(-4, 16, 156, 136)
	active := image.Rect(-4, 36, 156, 116)
	parent := lumaImage(bounds, active, 200)
	gray := image.NewGray(bounds)
	draw.Draw(gray, active, image.NewUniform(color.Gray{Y: 200}), image.Point{}, draw.Src)
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, active, image.White, image.Point{}, draw.Src)
	for _, img := range []image.Image{parent.SubImage(frame), gray.SubImage(frame), rgba.SubImage(frame)} {
		if got, ok := DetectCrop(img, 2, 1); !ok || got != active {
			t.Fatalf("%T: %v,%v", img, got, ok)
		}
	}
	if got, ok := DetectCrop(image.NewGray(image.Rectangle{}), 2, 1); ok || !got.Empty() {
		t.Fatal("empty image has crop")
	}
}

func BenchmarkDetectCrop(b *testing.B) {
	img := lumaImage(image.Rect(0, 0, 1920, 1080), image.Rect(285, 0, 1635, 1080), 160)
	b.ReportAllocs()
	for b.Loop() {
		DetectCrop(img, 1280, 1024)
	}
}
