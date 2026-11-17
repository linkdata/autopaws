package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const ocrTimeout = 15 * time.Second

func extractOCR(ctx context.Context, executable string, data []byte) (string, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("decode OCR JPEG: %w", err)
	}
	pgm, err := preparePGM(img)
	if err != nil {
		return "", err
	}
	text, err := runTesseract(ctx, executable, pgm, ocrTimeout)
	if err != nil {
		return "", err
	}
	return postprocessOCR(text), nil
}

// JPEG luminance is already grayscale. Preserve it without contrast/threshold
// changes, invert dark backgrounds, then replicate each pixel into a 2x2 block.
func preparePGM(img image.Image) ([]byte, error) {
	bounds := img.Bounds()
	if bounds.Empty() {
		return nil, fmt.Errorf("OCR image is empty")
	}
	var luma func(int, int) byte
	switch img := img.(type) {
	case *image.YCbCr:
		luma = func(x, y int) byte { return img.Y[img.YOffset(x, y)] }
	case *image.Gray:
		luma = func(x, y int) byte { return img.Pix[img.PixOffset(x, y)] }
	default:
		return nil, fmt.Errorf("unsupported OCR JPEG color model %T", img)
	}
	var sum uint64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			sum += uint64(luma(x, y))
		}
	}
	invert := sum < uint64(bounds.Dx())*uint64(bounds.Dy())*128
	width, height := bounds.Dx()*2, bounds.Dy()*2
	header := fmt.Appendf(nil, "P5\n%d %d\n255\n", width, height)
	result := make([]byte, len(header)+width*height)
	copy(result, header)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			value := luma(x, y)
			if invert {
				value = 255 - value
			}
			index := len(header) + (y-bounds.Min.Y)*2*width + (x-bounds.Min.X)*2
			result[index], result[index+1] = value, value
			result[index+width], result[index+width+1] = value, value
		}
	}
	return result, nil
}

func runTesseract(ctx context.Context, executable string, pgm []byte, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "stdin", "stdout", "--oem", "3", "--psm", "6")
	command.Stdin = bytes.NewReader(pgm)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	// Reap the process and bound inherited-pipe waits after cancellation/exit.
	command.WaitDelay = time.Second
	err := command.Run()
	if contextErr := ctx.Err(); contextErr != nil {
		return "", contextErr
	}
	if err != nil {
		return "", fmt.Errorf("tesseract: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

var excessOCRNewlines = regexp.MustCompile(`\n{4,}`)

func pythonWhitespace(r rune) bool {
	return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f
}

// Keep Python's operation order and its Unicode whitespace semantics.
func postprocessOCR(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, pythonWhitespace)
	}
	text = excessOCRNewlines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n\n")
	for _, correction := range [][2]string{{" |s ", " ls "}, {" |s\n", " ls\n"}, {"\n|s ", "\nls "}} {
		text = strings.ReplaceAll(text, correction[0], correction[1])
	}
	return strings.TrimFunc(text, pythonWhitespace)
}
