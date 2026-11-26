package capture

import (
	"image"
	"image/color"
	"math/bits"
)

const (
	cropThreshold  = 20
	cropEdgeMargin = 8
)

// DetectCrop validates the centered aspect-fit area of the configured target.
// Missing geometry, insignificant bars or bright pixels outside the expected
// area (allowing JPEG edge bleed) leave the full capture intact.
func DetectCrop(img image.Image, screenWidth, screenHeight int) (image.Rectangle, bool) {
	bounds := img.Bounds()
	if bounds.Empty() || screenWidth <= 0 || screenHeight <= 0 {
		return image.Rectangle{}, false
	}
	width, height := bounds.Dx(), bounds.Dy()
	// Target dimensions can span int, so compare and divide full-width products.
	xhi, xlo := bits.Mul64(uint64(width), uint64(screenHeight))
	yhi, ylo := bits.Mul64(uint64(height), uint64(screenWidth))
	if xhi > yhi || xhi == yhi && xlo > ylo {
		fitted, _ := bits.Div64(yhi, ylo, uint64(screenHeight))
		width = int(fitted)
	} else {
		fitted, _ := bits.Div64(xhi, xlo, uint64(screenWidth))
		height = int(fitted)
	}
	x, y := (bounds.Dx()-width)/2, (bounds.Dy()-height)/2
	if width == 0 || height == 0 || x <= cropEdgeMargin && y <= cropEdgeMargin {
		return image.Rectangle{}, false
	}
	crop := image.Rect(x, y, x+width, y+height).Add(bounds.Min)
	tolerated := crop.Inset(-cropEdgeMargin)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if image.Pt(x, y).In(tolerated) {
				continue
			}
			var value uint8
			switch img := img.(type) {
			case *image.YCbCr:
				value = img.Y[img.YOffset(x, y)]
			case *image.Gray:
				value = img.Pix[img.PixOffset(x, y)]
			default:
				value = color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y
			}
			if value > cropThreshold {
				return image.Rectangle{}, false
			}
		}
	}
	return crop, true
}
