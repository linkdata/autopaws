package capture

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"testing"
)

func encodedImage(t testing.TB, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func colorJPEG(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 160, 120))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{R: 180, G: 80, B: 20, A: 255}), image.Point{}, draw.Src)
	return encodedImage(t, img)
}

func withoutDHT(t testing.TB, data []byte) []byte {
	t.Helper()
	result := append([]byte(nil), data[:2]...)
	for pos := 2; pos+4 <= len(data); {
		length := int(data[pos+2])<<8 | int(data[pos+3])
		if data[pos+1] == 0xda {
			return append(result, data[pos:]...)
		}
		end := pos + 2 + length
		if length < 2 || end > len(data) {
			t.Fatal("bad test JPEG segment")
		}
		if data[pos+1] != 0xc4 {
			result = append(result, data[pos:end]...)
		}
		pos = end
	}
	t.Fatal("test JPEG has no SOS")
	return nil
}

func TestNormalizeMJPEGMissingTablesAndPadding(t *testing.T) {
	original := colorJPEG(t)
	stripped := withoutDHT(t, original)
	if _, err := jpeg.Decode(bytes.NewReader(stripped)); err == nil {
		t.Fatal("stripped fixture unexpectedly decodes")
	}
	before := bytes.Clone(stripped)
	fixed := NormalizeMJPEG(stripped)
	if !bytes.Equal(fixed, original) || !bytes.Equal(stripped, before) {
		t.Fatal("Huffman repair differs from encoder original or mutates source")
	}
	if _, err := jpeg.Decode(bytes.NewReader(fixed)); err != nil {
		t.Fatal(err)
	}
	padded := append(bytes.Clone(original), 3, 4, 5, 0)
	if actual := NormalizeMJPEG(padded); !bytes.Equal(actual, original) {
		t.Fatal("JPEG padding survived normalization")
	}
	if actual := NormalizeMJPEG(original); !bytes.Equal(actual, original) || &actual[0] != &original[0] {
		t.Fatal("ordinary JPEG changed or allocated")
	}
}

func TestNormalizeMJPEGMalformedAndNonJPEG(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0xff},
		{1, 2, 0xff, 0xd9, 3},
		{0xff, 0xd8},
		{0xff, 0xd8, 1, 2},
		{0xff, 0xd8, 0xff},
		{0xff, 0xd8, 0xff, 0xff},
		{0xff, 0xd8, 0xff, 0},
		{0xff, 0xd8, 0xff, 0xe0, 0},
		{0xff, 0xd8, 0xff, 0xe0, 0, 1},
		{0xff, 0xd8, 0xff, 0xe0, 0xff, 0xff},
		{0xff, 0xd8, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 1, 0xff, 0xd0},
		{0xff, 0xd8, 0xff, 0xc4, 0, 2, 0xff, 0xda, 0, 2},
	}
	for _, data := range cases {
		if result := NormalizeMJPEG(data); !bytes.Equal(result, data) {
			t.Errorf("malformed/non-JPEG %x changed to %x", data, result)
		}
	}
	withFill := []byte{0xff, 0xd8, 0xff, 0xff, 0xda, 0, 2}
	want := append([]byte{0xff, 0xd8}, standardDHT()...)
	want = append(want, withFill[2:]...)
	if got := NormalizeMJPEG(withFill); !bytes.Equal(got, want) {
		t.Fatal("marker fill bytes not retained around inserted DHT")
	}
}

func FuzzNormalizeMJPEG(f *testing.F) {
	f.Add(colorJPEG(f))
	f.Add([]byte{0xff, 0xd8, 0xff, 0xda, 0, 2})
	f.Add([]byte{1, 0xff, 0xd9, 2})
	f.Fuzz(func(t *testing.T, data []byte) {
		before := bytes.Clone(data)
		result := NormalizeMJPEG(data)
		if !bytes.Equal(data, before) {
			t.Fatal("modified caller bytes")
		}
		if len(data) < 2 || data[0] != 0xff || data[1] != 0xd8 {
			if !bytes.Equal(result, data) {
				t.Fatal("non-JPEG was changed")
			}
		}
		if len(result) > len(data)+len(standardDHT()) {
			t.Fatal("more than one standard DHT inserted")
		}
	})
}

func BenchmarkNormalizeMJPEG(b *testing.B) {
	original := colorJPEG(b)
	for _, fixture := range []struct {
		name string
		data []byte
	}{
		{"passthrough", original}, {"missing-DHT", withoutDHT(b, original)},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				NormalizeMJPEG(fixture.data)
			}
		})
	}
}
