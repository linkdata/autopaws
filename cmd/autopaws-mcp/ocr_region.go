package main

import (
	"errors"
	"fmt"
	"image"
)

type screenRegion struct {
	X      int `json:"x" jsonschema:"Left edge in original screenshot pixels, measured from the top-left corner."`
	Y      int `json:"y" jsonschema:"Top edge in original screenshot pixels, measured from the top-left corner."`
	Width  int `json:"width" jsonschema:"Positive region width in screenshot pixels."`
	Height int `json:"height" jsonschema:"Positive region height in screenshot pixels."`
}

type ocrInput struct {
	Region *screenRegion `json:"region,omitempty" jsonschema:"Optional OCR region in original screenshot pixels. Must fit entirely inside the captured image; omission analyzes the full screenshot."`
}

func (region *screenRegion) validate() error {
	if region != nil && (region.X < 0 || region.Y < 0 || region.Width <= 0 || region.Height <= 0) {
		return errors.New("region requires nonnegative x and y and positive width and height")
	}
	return nil
}

func cropOCRImage(img image.Image, region *screenRegion) (image.Image, error) {
	if err := region.validate(); err != nil {
		return nil, err
	}
	if region == nil {
		return img, nil
	}
	bounds := img.Bounds()
	if region.Width > bounds.Dx()-region.X || region.Height > bounds.Dy()-region.Y {
		return nil, fmt.Errorf("region must fit inside the %dx%d screenshot", bounds.Dx(), bounds.Dy())
	}
	croppable, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return nil, fmt.Errorf("unsupported OCR image type %T", img)
	}
	left, top := bounds.Min.X+region.X, bounds.Min.Y+region.Y
	return croppable.SubImage(image.Rect(left, top, left+region.Width, top+region.Height)), nil
}
