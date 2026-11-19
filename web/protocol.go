//go:build (linux || darwin) && (amd64 || arm64)

package web

import (
	"encoding/binary"
	"image"
	"time"

	"github.com/linkdata/autopaws/alsa"
	"github.com/linkdata/autopaws/capture"
	"github.com/linkdata/autopaws/v4l2"
)

const (
	mediaHeaderSize = 64
	mediaVersion    = 1
	videoMessage    = 1
	audioMessage    = 2
)

const (
	flagMonotonic = 1 << iota
	flagStartOfExposure
	flagEstimated
)

func videoHeader(metadata capture.Metadata, sequence uint64) [mediaHeaderSize]byte {
	var header [mediaHeaderSize]byte
	header[0], header[1] = videoMessage, mediaVersion
	binary.LittleEndian.PutUint16(header[2:], mediaHeaderSize)
	var flags uint32
	if metadata.TimestampFlags&v4l2.TimestampMask == v4l2.TimestampMonotonic {
		flags |= flagMonotonic
		binary.LittleEndian.PutUint64(header[24:], uint64(metadata.TimestampNS))
	}
	if flags&flagMonotonic != 0 && metadata.TimestampFlags&v4l2.TimestampEstimated != 0 {
		flags |= flagEstimated
	}
	if metadata.TimestampFlags&v4l2.TimestampSourceMask == v4l2.TimestampStartOfExposure {
		flags |= flagStartOfExposure
	}
	binary.LittleEndian.PutUint32(header[4:], flags)
	binary.LittleEndian.PutUint64(header[8:], metadata.Generation)
	binary.LittleEndian.PutUint64(header[16:], sequence)
	binary.LittleEndian.PutUint32(header[32:], uint32(metadata.Width))
	binary.LittleEndian.PutUint32(header[36:], uint32(metadata.Height))
	crop := metadata.Crop
	if crop.Empty() {
		crop = image.Rect(0, 0, metadata.Width, metadata.Height)
	}
	binary.LittleEndian.PutUint32(header[40:], uint32(crop.Min.X))
	binary.LittleEndian.PutUint32(header[44:], uint32(crop.Min.Y))
	binary.LittleEndian.PutUint32(header[48:], uint32(crop.Dx()))
	binary.LittleEndian.PutUint32(header[52:], uint32(crop.Dy()))
	return header
}

func audioHeader(block alsa.Block) [mediaHeaderSize]byte {
	var header [mediaHeaderSize]byte
	header[0], header[1] = audioMessage, mediaVersion
	binary.LittleEndian.PutUint16(header[2:], mediaHeaderSize)
	binary.LittleEndian.PutUint32(header[4:], flagMonotonic|flagEstimated)
	binary.LittleEndian.PutUint64(header[8:], block.Config.Generation)
	binary.LittleEndian.PutUint64(header[16:], block.Sequence)
	binary.LittleEndian.PutUint64(header[24:], uint64(block.TimestampNS))
	binary.LittleEndian.PutUint32(header[56:], block.Frames)
	binary.LittleEndian.PutUint32(header[60:], uint32(uint64(block.Frames)*uint64(time.Second)/uint64(block.Config.Rate)))
	return header
}
