package main

import (
	"context"
	"image"
	"image/color"
	"math"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/apiwire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCropOCRImageKeepsPixelsAndColorModel(t *testing.T) {
	bounds := image.Rect(-2, 3, 3, 6)
	gray := image.NewGray(bounds)
	for i := range gray.Pix {
		gray.Pix[i] = byte(i)
	}
	ycbcr := image.NewYCbCr(bounds, image.YCbCrSubsampleRatio420)
	copy(ycbcr.Y, gray.Pix)
	cmyk := image.NewCMYK(bounds)
	cmyk.SetCMYK(2, 4, color.CMYK{C: 17, M: 33, Y: 91, K: 4})
	for _, img := range []image.Image{gray, ycbcr, cmyk} {
		unchanged, err := cropOCRImage(img, nil)
		if err != nil || unchanged != img {
			t.Fatalf("omitted region changed image: %v", err)
		}
		cropped, err := cropOCRImage(img, &screenRegion{X: 4, Y: 1, Width: 1, Height: 2})
		if err != nil {
			t.Fatal(err)
		}
		if cropped.Bounds() != image.Rect(2, 4, 3, 6) || reflect.TypeOf(cropped) != reflect.TypeOf(img) {
			t.Fatalf("crop bounds/model=%v/%T for %T", cropped.Bounds(), cropped, img)
		}
		for y := 4; y < 6; y++ {
			if !reflect.DeepEqual(cropped.At(2, y), img.At(2, y)) {
				t.Fatalf("crop changed pixel at (2,%d)", y)
			}
		}
	}
}

func TestCropOCRImageRejectsInvalidRegions(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 5, 3))
	for _, region := range []screenRegion{
		{X: -1, Width: 1, Height: 1},
		{Y: -1, Width: 1, Height: 1},
		{Width: 0, Height: 1},
		{Width: 1, Height: -1},
		{X: 5, Width: 1, Height: 1},
		{Y: 3, Width: 1, Height: 1},
		{X: 4, Width: 2, Height: 1},
		{Y: 2, Width: 1, Height: 2},
		{X: math.MaxInt, Width: math.MaxInt, Height: 1},
		{X: 1, Width: math.MaxInt, Height: 1},
	} {
		if _, err := cropOCRImage(img, &region); err == nil || !strings.Contains(err.Error(), "region") {
			t.Errorf("invalid region %+v accepted: %v", region, err)
		}
	}
	if _, err := cropOCRImage(image.NewUniform(color.White), &screenRegion{Width: 1, Height: 1}); err == nil {
		t.Fatal("uncroppable image type accepted")
	}
}

func TestOCRToolsHonorRegionAndKeepCoordinatesUnset(t *testing.T) {
	path := fakeTesseract(t, "dimensions")
	frame := sdkCapture{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
		return sdkDefaultReply(request, frame)
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{TesseractPath: path}, apiclient.New(fake.listener.Addr().String())))
	for _, tool := range []string{"get_screen_text", "execute_and_read"} {
		args := map[string]any{"region": map[string]any{"x": 4, "y": 1, "width": 1, "height": 2}}
		if tool == "execute_and_read" {
			args["command"], args["wait_seconds"] = "literal command", 0
		}
		result := sdkTool(t, session, tool, args, false)
		if strings.TrimSpace(sdkText(result)) != "2x4" {
			t.Fatalf("%s did not send only the doubled region to OCR: %s", tool, sdkText(result))
		}
		for _, part := range result.Content {
			if _, ok := part.(*mcp.ImageContent); ok {
				t.Fatalf("%s returned an image", tool)
			}
		}
		sdkTool(t, session, "mouse_move", map[string]any{"x": 0, "y": 0}, true)
	}
	var methods []string
	for _, request := range fake.snapshot() {
		methods = append(methods, request.Method)
	}
	if !slices.Equal(methods, []string{"capture_frame", "type_text", "capture_frame"}) {
		t.Fatalf("region OCR API sequence=%v", methods)
	}
	for _, tool := range []string{"get_screen_text", "execute_and_read"} {
		args := map[string]any{"region": map[string]any{"x": 4, "y": 1, "width": 2, "height": 2}}
		if tool == "execute_and_read" {
			args["command"], args["wait_seconds"] = "literal command", 0
		}
		if result := sdkTool(t, session, tool, args, true); !strings.Contains(sdkText(result), "region must fit inside") {
			t.Fatalf("out-of-bounds region error=%s", sdkText(result))
		}
	}
}

func TestOCRRegionRejectsBasicGeometryBeforeInputOrCapture(t *testing.T) {
	owner := &toolServer{caller: unitCaller(func(context.Context, string, any, any) error {
		t.Fatal("invalid region reached the daemon")
		return nil
	})}
	for _, region := range []screenRegion{{X: -1, Width: 1, Height: 1}, {Width: 0, Height: 1}, {Width: 1, Height: -1}} {
		input := ocrInput{Region: &region}
		if _, err := owner.getScreenText(t.Context(), nil, input); err == nil {
			t.Fatal("get_screen_text accepted invalid region")
		}
		if _, err := owner.executeAndRead(t.Context(), nil, executeInput{ocrInput: input, Command: "must not type"}); err == nil {
			t.Fatal("execute_and_read accepted invalid region")
		}
	}
}

func TestOCRRegionKeepsCMYKUnsupported(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "ocr", "unsupported-cmyk.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractOCR(t.Context(), "must-not-run", data, ocrInput{Region: &screenRegion{Width: 1, Height: 1}}); err == nil || !strings.Contains(err.Error(), "color model") {
		t.Fatalf("cropping changed CMYK handling: %v", err)
	}
}
