package capture

import (
	"image"
	"image/color"
)

const (
	cropThreshold  = 20
	cropMinBorder  = 4
	cropMinContent = 64
)

// DetectCrop applies the reference detector's thresholds to JPEG luminance.
// The returned rectangle uses the image's coordinates. Empty/insignificant
// borders and fewer than 64 active rows or columns do not produce a crop.
func DetectCrop(img image.Image) (image.Rectangle, bool) {
	bounds := img.Bounds()
	rowMax := make([]uint8, bounds.Dy())
	colMax := make([]uint8, bounds.Dx())
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			var value uint8
			switch img := img.(type) {
			case *image.YCbCr:
				value = img.Y[img.YOffset(x, y)]
			case *image.Gray:
				value = img.Pix[img.PixOffset(x, y)]
			default:
				value = color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y
			}
			rowMax[y-bounds.Min.Y] = max(rowMax[y-bounds.Min.Y], value)
			colMax[x-bounds.Min.X] = max(colMax[x-bounds.Min.X], value)
		}
	}
	span := func(values []uint8) (low, high, count int) {
		low = -1
		for index, value := range values {
			if value > cropThreshold {
				if low < 0 {
					low = index
				}
				high = index + 1
				count++
			}
		}
		return
	}
	y1, y2, rows := span(rowMax)
	x1, x2, columns := span(colMax)
	if rows < cropMinContent || columns < cropMinContent {
		return image.Rectangle{}, false
	}
	if y1 < cropMinBorder && bounds.Dy()-y2 < cropMinBorder &&
		x1 < cropMinBorder && bounds.Dx()-x2 < cropMinBorder {
		return image.Rectangle{}, false
	}
	return image.Rect(x1, y1, x2, y2).Add(bounds.Min), true
}
