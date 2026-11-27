package hid

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// UnmarshalJSON keeps an omitted desktop origin distinct from explicit zero.
func (geometry *MappingGeometry) UnmarshalJSON(data []byte) error {
	var request struct {
		Offset *struct {
			X *int `json:"x"`
			Y *int `json:"y"`
		} `json:"offset"`
		Width  int `json:"width"`
		Height int `json:"height"`
		Span   *struct {
			X      *int `json:"x"`
			Y      *int `json:"y"`
			Width  int  `json:"width"`
			Height int  `json:"height"`
		} `json:"span"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	if request.Offset == nil || request.Offset.X == nil || request.Offset.Y == nil || request.Span == nil || request.Span.X == nil || request.Span.Y == nil {
		return fmt.Errorf("%w: offset.x, offset.y, span.x and span.y are required, including zero", ErrMapping)
	}
	*geometry = MappingGeometry{
		Offset: Point{X: *request.Offset.X, Y: *request.Offset.Y}, Width: request.Width, Height: request.Height,
		Span: MappingSpan{X: *request.Span.X, Y: *request.Span.Y, Width: request.Span.Width, Height: request.Span.Height},
	}
	return nil
}
