package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The Go test binary is a portable fake executable, so process/pipe cancellation
// is exercised on Windows as well as Unix without a shell script dependency.
func TestMain(m *testing.M) {
	if mode := os.Getenv("AUTOPAWS_OCR_TEST_HELPER"); mode != "" {
		if !reflect.DeepEqual(os.Args[1:], []string{"stdin", "stdout", "--oem", "3", "--psm", "6"}) {
			fmt.Fprint(os.Stderr, "unexpected OCR arguments")
			os.Exit(10)
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil || !bytes.HasPrefix(data, []byte("P5\n")) {
			fmt.Fprint(os.Stderr, "missing PGM stdin")
			os.Exit(11)
		}
		switch mode {
		case "success":
			fmt.Fprint(os.Stdout, "  result line  \n\n\n\nsecond line \n")
			os.Exit(0)
		case "fail":
			fmt.Fprint(os.Stderr, "fake Tesseract failure")
			os.Exit(17)
		case "hang":
			if marker := os.Getenv("AUTOPAWS_OCR_READY"); marker != "" {
				if err := os.WriteFile(marker, []byte("ready"), 0o600); err != nil {
					os.Exit(12)
				}
			}
			time.Sleep(time.Hour)
			os.Exit(13)
		}
		os.Exit(14)
	}
	os.Exit(m.Run())
}

func fakeTesseract(t *testing.T, mode string) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTOPAWS_OCR_TEST_HELPER", mode)
	// Avoid the race runtime's one-second exit pause in each helper subprocess.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	return path
}

func pgmPixels(t *testing.T, data []byte) (int, int, []byte) {
	t.Helper()
	reader := bufio.NewReader(bytes.NewReader(data))
	var magic string
	var width, height, maximum int
	if _, err := fmt.Fscanf(reader, "%s\n%d %d\n%d\n", &magic, &width, &height, &maximum); err != nil {
		t.Fatal(err)
	}
	pixels, err := io.ReadAll(reader)
	if err != nil || magic != "P5" || maximum != 255 || len(pixels) != width*height {
		t.Fatalf("invalid PGM %q %dx%d max%d pixels%d error%v", magic, width, height, maximum, len(pixels), err)
	}
	return width, height, pixels
}

func TestOCRPreprocessingLumaInversionAndNearestNeighbor(t *testing.T) {
	for _, value := range []byte{20, 128, 230} {
		for _, gray := range []bool{false, true} {
			bounds := image.Rect(-2, 3, 0, 5)
			var img image.Image
			if gray {
				frame := image.NewGray(image.Rect(-5, 0, 4, 9))
				for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
					for x := bounds.Min.X; x < bounds.Max.X; x++ {
						frame.SetGray(x, y, color.Gray{Y: value})
					}
				}
				img = frame.SubImage(bounds)
			} else {
				frame := image.NewYCbCr(image.Rect(-5, 0, 4, 9), image.YCbCrSubsampleRatio420)
				for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
					for x := bounds.Min.X; x < bounds.Max.X; x++ {
						frame.Y[frame.YOffset(x, y)] = value
					}
				}
				img = frame.SubImage(bounds)
			}
			data, err := preparePGM(img)
			if err != nil {
				t.Fatal(err)
			}
			width, height, pixels := pgmPixels(t, data)
			want := value
			if value < 128 {
				want = 255 - value
			}
			if width != 4 || height != 4 || !bytes.Equal(pixels, bytes.Repeat([]byte{want}, 16)) {
				t.Fatalf("preprocessing gray%t value%d = %dx%d %v", gray, value, width, height, pixels)
			}
		}
	}
	img := image.NewGray(image.Rect(0, 0, 2, 2))
	copy(img.Pix, []byte{0, 100, 200, 255}) // Mean138.75: no inversion.
	data, err := preparePGM(img)
	if err != nil {
		t.Fatal(err)
	}
	_, _, pixels := pgmPixels(t, data)
	want := []byte{0, 0, 100, 100, 0, 0, 100, 100, 200, 200, 255, 255, 200, 200, 255, 255}
	if !bytes.Equal(pixels, want) || !bytes.Equal(img.Pix, []byte{0, 100, 200, 255}) {
		t.Fatalf("nearest-neighbor replication = %v; source %v", pixels, img.Pix)
	}
	for _, invalid := range []image.Image{image.NewGray(image.Rectangle{}), image.NewCMYK(image.Rect(0, 0, 2, 2))} {
		if _, err := preparePGM(invalid); err == nil {
			t.Fatal("unsupported/empty image accepted")
		}
	}
}

func TestOCRPostprocessingMatchesPython(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""},
		{"  prompt$ ls  \n next\t\r\n", "prompt$ ls\n next"},
		{"a\n\n\n\nb\n\n\n\n\nc", "a\n\n\nb\n\n\nc"},
		{"$ |s -la\n$ |s\n|s /tmp\n", "$ ls -la\n$ ls\nls /tmp"},
		{"|s\nnot|s here\n|s", "|s\nnot|s here\n|s"},
		{"\n|s here\n", "ls here"},
		{"\x1c\u2003 hello\u00a0\nworld\x1f\n\u0085", "hello\nworld"},
		{"a\n \t\n \n \nb", "a\n\n\nb"},
	} {
		if got := postprocessOCR(tc.input); got != tc.want {
			t.Errorf("postprocess(%q)=%q want%q", tc.input, got, tc.want)
		}
	}
}

func TestOCRExecAndJPEGFailures(t *testing.T) {
	path := fakeTesseract(t, "success")
	text, err := extractOCR(t.Context(), path, unitJPEG(t))
	if err != nil || text != "result line\n\n\nsecond line" {
		t.Fatalf("OCR output=%q error%v", text, err)
	}
	var grayscale bytes.Buffer
	if err := jpeg.Encode(&grayscale, image.NewGray(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	if text, err := extractOCR(t.Context(), path, grayscale.Bytes()); err != nil || text != "result line\n\n\nsecond line" {
		t.Fatalf("grayscale JPEG OCR=%q,%v", text, err)
	}
	for _, data := range [][]byte{nil, []byte("not a JPEG")} {
		if _, err := extractOCR(t.Context(), path, data); err == nil {
			t.Fatal("invalid JPEG accepted")
		}
	}
	cmyk, err := os.ReadFile("testdata/ocr/unsupported-cmyk.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractOCR(t.Context(), path, cmyk); err == nil || !strings.Contains(err.Error(), "color model") {
		t.Fatalf("CMYK JPEG=%v", err)
	}
	fakeTesseract(t, "fail")
	_, err = runTesseract(t.Context(), path, []byte("P5\n1 1\n255\nX"), time.Second)
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 17 || !strings.Contains(err.Error(), "fake Tesseract failure") {
		t.Fatalf("execution failure lost=%v", err)
	}
	_, err = extractOCR(t.Context(), path, unitJPEG(t))
	if !errors.As(err, &exited) {
		t.Fatalf("extract suppressed subprocess failure=%v", err)
	}
	_, err = runTesseract(t.Context(), filepath.Join(t.TempDir(), "missing"), []byte("P5\n"), time.Second)
	var missing *os.PathError
	if !errors.As(err, &missing) {
		t.Fatalf("start failure lost=%v", err)
	}
}

func TestOCRSubprocessTimeoutAndCancellationReapTheHelper(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			path := fakeTesseract(t, "hang")
			marker := filepath.Join(t.TempDir(), "ready")
			t.Setenv("AUTOPAWS_OCR_READY", marker)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			timeout := 5 * time.Second
			if deadline {
				timeout = 500 * time.Millisecond
			}
			go func() { _, err := runTesseract(ctx, path, []byte("P5\n1 1\n255\nX"), timeout); done <- err }()
			limit := time.NewTimer(5 * time.Second)
			defer limit.Stop()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
		ready:
			for {
				select {
				case <-ticker.C:
					if _, err := os.Stat(marker); err == nil {
						break ready
					}
				case <-limit.C:
					t.Fatal("OCR helper did not start")
				}
			}
			want := context.DeadlineExceeded
			if !deadline {
				cancel()
				want = context.Canceled
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("OCR cancellation=%v want%v", err, want)
				}
			case <-limit.C:
				t.Fatal("OCR process/pipe wait survived cancellation")
			}
		})
	}
}

func TestOCRWaitBounds(t *testing.T) {
	if wait, err := executionWait(nil); err != nil || wait != time.Second {
		t.Fatalf("default wait=%s,%v", wait, err)
	}
	for _, seconds := range []float64{0, 0.125, 60} {
		if wait, err := executionWait(&seconds); err != nil || wait != time.Duration(seconds*float64(time.Second)) {
			t.Fatalf("wait(%f)=%s,%v", seconds, wait, err)
		}
	}
	for _, seconds := range []float64{-1, 60.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := executionWait(&seconds); err == nil {
			t.Fatalf("invalid wait%f accepted", seconds)
		}
	}
}
