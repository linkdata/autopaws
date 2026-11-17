// Package capture normalizes and distributes MJPEG frames from a shared source.
package capture

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"sync"
)

// Metadata belongs to the same source generation as a Frame's JPEG. Crop is
// empty when no crop applies. TimestampNS remains in the driver's clock domain;
// TimestampFlags identifies that domain and the capture instant. Sequence is
// the raw per-open source counter and can reset on reopen; it is diagnostic,
// rather than a delivery-order counter for a persistent connection.
type Metadata struct {
	Width, Height            int
	Crop                     image.Rectangle
	Generation               uint64
	TimestampNS              int64
	TimestampFlags, Sequence uint32
}

// Snapshot contains the cropped JPEG when a crop is active, or the normalized
// source JPEG otherwise. Bytes are read-only and may be shared with other users.
type Snapshot struct {
	JPEG          []byte
	Width, Height int
	Generation    uint64
}

// Frame holds immutable bytes and metadata plus lazily cached decode and encode
// results. The shared producer transfers ownership of its source bytes here.
type Frame struct {
	metadata  Metadata
	jpeg      []byte
	decode    sync.Once
	image     image.Image
	decodeErr error
	encode    sync.Once
	snapshot  Snapshot
	encodeErr error
}

func (frame *Frame) Metadata() Metadata { return frame.metadata }

// JPEG returns read-only normalized source bytes, without applying Crop. Web
// viewers use Metadata to crop at presentation time without a server encode.
func (frame *Frame) JPEG() []byte { return frame.jpeg }

func (frame *Frame) decoded() (image.Image, error) {
	frame.decode.Do(func() {
		frame.image, frame.decodeErr = jpeg.Decode(bytes.NewReader(frame.jpeg))
	})
	return frame.image, frame.decodeErr
}

// Snapshot validates a full decode even on the passthrough path and caches the
// only server encode, fixed at quality 85. A crop is encoded once per Frame,
// regardless of how many snapshot requests receive that frame.
func (frame *Frame) Snapshot() (Snapshot, error) {
	frame.encode.Do(func() {
		img, err := frame.decoded()
		if err != nil {
			frame.encodeErr = err
			return
		}
		result := Snapshot{JPEG: frame.jpeg, Width: frame.metadata.Width, Height: frame.metadata.Height, Generation: frame.metadata.Generation}
		if !frame.metadata.Crop.Empty() {
			crop := frame.metadata.Crop
			if !crop.In(img.Bounds()) {
				frame.encodeErr = fmt.Errorf("crop %v outside frame %v", crop, img.Bounds())
				return
			}
			// JPEG decodes to one of these standard image types, all of which
			// implement SubImage without copying the decoded pixels.
			img = img.(interface {
				SubImage(image.Rectangle) image.Image
			}).SubImage(crop)
			var encoded bytes.Buffer
			if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 85}); err != nil {
				frame.encodeErr = err
				return
			}
			result.JPEG, result.Width, result.Height = encoded.Bytes(), crop.Dx(), crop.Dy()
		}
		frame.snapshot = result
	})
	return frame.snapshot, frame.encodeErr
}

type processor struct {
	autocrop bool
	started  bool
	frames   uint64
	crop     image.Rectangle
}

func newProcessor(autocrop bool) *processor { return &processor{autocrop: autocrop} }

// process takes ownership of raw. Full decoding happens during initial warmup,
// crop detection and snapshot acquisition, then is reused by Snapshot.
func (processor *processor) process(raw []byte, metadata Metadata, needSnapshot bool) (*Frame, error) {
	normalized := NormalizeMJPEG(raw)
	config, err := jpeg.DecodeConfig(bytes.NewReader(normalized))
	if err != nil {
		return nil, err
	}
	if metadata.Width <= 0 || metadata.Height <= 0 || config.Width != metadata.Width || config.Height != metadata.Height {
		return nil, fmt.Errorf("JPEG size %dx%d differs from capture size %dx%d", config.Width, config.Height, metadata.Width, metadata.Height)
	}
	processor.frames++
	frame := &Frame{metadata: metadata, jpeg: normalized}
	detect := processor.autocrop && (!processor.started || processor.frames%30 == 0 || needSnapshot && processor.crop.Empty())
	if !processor.started || detect || needSnapshot {
		img, err := frame.decoded()
		if err != nil {
			return nil, err
		}
		if detect {
			processor.crop, _ = DetectCrop(img)
		}
	}
	processor.started = true
	frame.metadata.Crop = processor.crop
	return frame, nil
}
