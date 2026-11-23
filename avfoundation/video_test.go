//go:build darwin && (amd64 || arm64)

package avfoundation

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/linkdata/autopaws/v4l2"
)

func multipartFrames(t *testing.T, frames ...[]byte) []byte {
	t.Helper()
	var wire bytes.Buffer
	writer := multipart.NewWriter(&wire)
	if err := writer.SetBoundary(videoBoundary); err != nil {
		t.Fatal(err)
	}
	for _, frame := range frames {
		part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"image/jpeg"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return wire.Bytes()
}

func TestVideoMultipartFraming(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 32, 24)), nil); err != nil {
		t.Fatal(err)
	}
	// Legal JPEG application metadata can contain apparent EOI markers. Three
	// large segments ensure the frame crosses many multipart reader buffers.
	data := []byte{0xff, 0xd8}
	for range 3 {
		payload := bytes.Repeat([]byte{0x73}, 60000)
		payload[100], payload[101] = 0xff, 0xd9
		data = append(data, 0xff, 0xe1, 0xea, 0x62) // Segment length = 60002.
		data = append(data, payload...)
	}
	data = append(data, encoded.Bytes()[2:]...)
	wire := multipartFrames(t, data, data)
	for _, size := range []int{16, 4096, 64 << 10} {
		reader := multipart.NewReader(bufio.NewReaderSize(bytes.NewReader(wire), size), videoBoundary)
		for index := range 2 {
			got, err := readVideoPart(reader)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("buffer %d frame %d: got %d bytes, want %d: %v", size, index, len(got), len(data), err)
			}
		}
		if _, err := readVideoPart(reader); !errors.Is(err, io.EOF) {
			t.Fatalf("buffer %d: trailing result %v", size, err)
		}
	}
	body := bytes.Index(wire, []byte("\r\n\r\n")) + 4
	truncated := multipart.NewReader(bytes.NewReader(wire[:body+len(data)/2]), videoBoundary)
	if _, err := readVideoPart(truncated); err == nil {
		t.Fatal("accepted a truncated multipart body")
	}
}

func TestVideoMultipartOversizedFrame(t *testing.T) {
	// Generate only the multipart header eagerly, then stream an oversized
	// payload without retaining an additional full multipart-body copy.
	var header bytes.Buffer
	writer := multipart.NewWriter(&header)
	if err := writer.SetBoundary(videoBoundary); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"image/jpeg"}}); err != nil {
		t.Fatal(err)
	}
	stream := io.MultiReader(bytes.NewReader(header.Bytes()),
		strings.NewReader(strings.Repeat("x", maxJPEG+1)),
		strings.NewReader("\r\n--"+videoBoundary+"--\r\n"))
	if _, err := readVideoPart(multipart.NewReader(stream, videoBoundary)); err == nil {
		t.Fatal("accepted an oversized frame")
	}
}

func ffmpegRequired(t *testing.T) {
	t.Helper()
	if os.Getenv("AUTOPAWS_FFMPEG_TEST") != "1" {
		t.Skip("set AUTOPAWS_FFMPEG_TEST=1 to run FFmpeg fixture tests")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg fixture test requires ffmpeg on PATH")
	}
}

func TestVideoFFmpegLifecycle(t *testing.T) {
	ffmpegRequired(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source, err := openVideoCommand(ctx, []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc=size=128x96:rate=30", "-c:v", "mjpeg", "-threads", "1", "-f", "mpjpeg", "-boundary_tag", videoBoundary, "pipe:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	format := source.Format()
	if format.Width != 128 || format.Height != 96 || format.FourCC != v4l2.PixFmtMJPEG {
		t.Fatalf("format = %+v", format)
	}
	frame, err := source.ReadFrame(ctx)
	if err != nil || frame.Sequence == 0 || len(frame.Data) == 0 || frame.Flags != v4l2.TimestampMonotonic|v4l2.TimestampEstimated {
		t.Fatalf("frame = %+v, %v", frame, err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(frame.Data)); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- source.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Close did not join FFmpeg")
	}
}

func TestVideoFFmpegStartupCancellation(t *testing.T) {
	ffmpegRequired(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	source, err := openVideoCommand(ctx, []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc=size=128x96:rate=30", "-vf", "select=0", "-c:v", "mjpeg", "-threads", "1", "-f", "mpjpeg", "-boundary_tag", videoBoundary, "pipe:1",
	})
	if source != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("open canceled = %v, %v", source, err)
	}
}
