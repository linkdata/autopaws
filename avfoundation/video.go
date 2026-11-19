package avfoundation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"mime/multipart"
	"os/exec"

	"github.com/linkdata/autopaws/internal/monotime"
	"github.com/linkdata/autopaws/v4l2"
)

const (
	videoBoundary = "autopaws-frame"
	maxJPEG       = 32 << 20
)

type videoResult struct {
	frame v4l2.Frame
	err   error
}

// Video owns one FFmpeg process and a continuously drained, latest-frame slot.
// Close kills and joins the process, including a blocked pipe read. Timestamps
// are estimated pipe-read times; FFmpeg/OS buffering precedes these timestamps.
// shortcut: pipes omit capture PTS; use native timestamps if freshness is required.
type Video struct {
	format  v4l2.Format
	frames  chan videoResult
	process *process
}

// OpenVideo resolves an exact device name and waits for the first encoded
// frame. ctx governs startup; the returned stream lives until Close.
func OpenVideo(ctx context.Context, path string, options v4l2.Options) (*Video, error) {
	if options.Width == 0 || options.Height == 0 {
		return nil, v4l2.ErrOptions
	}
	devices, err := Discover(ctx)
	if err != nil {
		return nil, err
	}
	device, err := selectDevice(devices, path, false)
	if err != nil {
		return nil, err
	}
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-f", "avfoundation",
		"-framerate", "30", "-video_size", fmt.Sprintf("%dx%d", options.Width, options.Height),
	}
	args = append(args, inputArguments(device)...)
	args = append(args, "-an", "-c:v", "mjpeg", "-q:v", "3",
		"-threads", "1", "-fps_mode", "passthrough", "-f", "mpjpeg", "-boundary_tag", videoBoundary, "-flush_packets", "1", "pipe:1")
	return openVideoCommand(ctx, args)
}

func openVideoCommand(ctx context.Context, args []string) (*Video, error) {
	source := &Video{frames: make(chan videoResult, 1)}
	owner, err := startProcess(ctx, "video", func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "ffmpeg", args...)
	}, source.run, func(err error) {
		source.publish(videoResult{err: err})
		close(source.frames)
	})
	if err != nil {
		return nil, err
	}
	source.process = owner
	return source, nil
}

func (source *Video) run(pipe io.Reader, ready func()) error {
	reader := multipart.NewReader(pipe, videoBoundary)
	var sequence uint32
	var err error
	for {
		// Read-start rather than read-end avoids counting a frame whose pipe
		// read was already underway when a snapshot request arrived as new.
		timestamp, clockErr := monotime.Now()
		if clockErr != nil {
			err = clockErr
			break
		}
		var data []byte
		data, err = readVideoPart(reader)
		if err != nil {
			break
		}
		config, decodeErr := jpeg.DecodeConfig(bytes.NewReader(data))
		if decodeErr != nil {
			err = decodeErr
			break
		}
		if sequence == 0 {
			source.format = v4l2.Format{Width: uint32(config.Width), Height: uint32(config.Height), FourCC: v4l2.PixFmtMJPEG}
			ready()
		} else if uint32(config.Width) != source.format.Width || uint32(config.Height) != source.format.Height {
			err = errors.New("avfoundation: frame dimensions changed")
			break
		}
		sequence++
		frame := v4l2.Frame{
			Data: data, Timestamp: v4l2.Timestamp{Seconds: timestamp / 1e9, Microseconds: timestamp % 1e9 / 1e3},
			Flags: v4l2.TimestampMonotonic | v4l2.TimestampEstimated, Sequence: sequence,
		}
		source.publish(videoResult{frame: frame})
	}
	return err
}

func (source *Video) publish(result videoResult) {
	select {
	case <-source.frames:
	default:
	}
	source.frames <- result
}

func (source *Video) Format() v4l2.Format { return source.format }

func (source *Video) ReadFrame(ctx context.Context) (v4l2.Frame, error) {
	if err := ctx.Err(); err != nil {
		return v4l2.Frame{}, err
	}
	select {
	case <-ctx.Done():
		return v4l2.Frame{}, ctx.Err()
	case result, ok := <-source.frames:
		if !ok {
			return v4l2.Frame{}, ErrClosed
		}
		return result.frame, result.err
	}
}

func (source *Video) Close() error {
	source.process.close()
	return nil
}

// readVideoPart bounds one MIME payload; the standard multipart reader handles
// delimiters independently of JPEG markers and streams across pipe refills.
func readVideoPart(reader *multipart.Reader) ([]byte, error) {
	part, err := reader.NextPart()
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(part, maxJPEG+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxJPEG {
		return nil, errors.New("avfoundation: oversized JPEG")
	}
	return data, nil
}
