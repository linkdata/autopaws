package capture

import (
	"bytes"
	"image"
	"image/jpeg"
	"sync"
)

var standardDHT = sync.OnceValue(func() []byte {
	var encoded bytes.Buffer
	// The fixed, supported image and bytes.Buffer cannot produce an encode error.
	_ = jpeg.Encode(&encoded, image.NewYCbCr(image.Rect(0, 0, 8, 8), image.YCbCrSubsampleRatio420), nil)
	data := encoded.Bytes()
	var tables []byte
	for offset := 2; offset+4 <= len(data); {
		length := int(data[offset+2])<<8 | int(data[offset+3])
		if length < 2 || length > len(data)-offset-2 {
			break
		}
		if data[offset+1] == 0xc4 {
			tables = append(tables, data[offset:offset+2+length]...)
		}
		if data[offset+1] == 0xda {
			break
		}
		offset += 2 + length
	}
	if len(tables) == 0 {
		panic("image/jpeg encoder produced no standard Huffman tables")
	}
	return tables
})

// NormalizeMJPEG trims padding after the last JPEG EOI and inserts the standard
// Annex K.3 Huffman tables before SOS when no DHT exists. Non-JPEG data passes
// through unchanged. The result can share data's storage; treat it as read-only.
func NormalizeMJPEG(data []byte) []byte {
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xd8 {
		return data
	}
	if end := bytes.LastIndex(data, []byte{0xff, 0xd9}); end >= 0 {
		data = data[:end+2]
	}
	for offset := 2; offset < len(data); {
		if data[offset] != 0xff {
			return data
		}
		markerStart := offset
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset == len(data) {
			return data
		}
		marker := data[offset]
		offset++
		if marker == 0 || marker == 0xd9 {
			return data
		}
		if marker == 1 || marker >= 0xd0 && marker <= 0xd8 {
			continue // Standalone markers have no segment length.
		}
		if offset+2 > len(data) {
			return data
		}
		length := int(data[offset])<<8 | int(data[offset+1])
		if length < 2 || length > len(data)-offset {
			return data
		}
		switch marker {
		case 0xc4:
			return data // Existing tables are authoritative, including custom ones.
		case 0xda:
			tables := standardDHT()
			result := make([]byte, 0, len(data)+len(tables))
			result = append(result, data[:markerStart]...)
			result = append(result, tables...)
			return append(result, data[markerStart:]...)
		}
		offset += length
	}
	return data
}
