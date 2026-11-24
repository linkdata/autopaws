package main

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strings"
	"testing"

	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/apiwire"
)

func TestOCRToolsForwardLanguageAndRegion(t *testing.T) {
	path := fakeTesseract(t, "dimensions")
	frame := sdkCapture{jpeg: sdkJPEG(t, 5, 3), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	fake := sdkNewAPI(t, func(_ net.Conn, request apiwire.Request) sdkReply {
		return sdkDefaultReply(request, frame)
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{TesseractPath: path}, apiclient.New(fake.listener.Addr().String())))
	for _, test := range []struct {
		name, language, want string
		region               *screenRegion
	}{
		{"default English", "", "10x6", nil},
		{"English and Swedish", "eng+swe", "10x6", nil},
		{"language and region", "eng+swe", "2x4", &screenRegion{X: 4, Y: 1, Width: 1, Height: 2}},
		{"script model", "script/Latin", "10x6", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AUTOPAWS_OCR_EXPECT_LANGUAGE", test.language)
			for _, tool := range []string{"get_screen_text", "execute_and_read"} {
				args := map[string]any{}
				if test.language != "" {
					args["language"] = test.language
				}
				if test.region != nil {
					args["region"] = test.region
				}
				if tool == "execute_and_read" {
					args["command"], args["wait_seconds"] = "literal command", 0
				}
				result := sdkTool(t, session, tool, args, false)
				if text := strings.TrimSpace(sdkText(result)); text != test.want {
					t.Fatalf("%s OCR arguments=%q, want %q", tool, text, test.want)
				}
			}
		})
	}
}

func TestOCRLanguageValidationBeforeInputOrCapture(t *testing.T) {
	for _, language := range []string{"", "eng", "eng+swe", "chi_sim", "script/Latin", "eng+script/Latin"} {
		if err := (ocrInput{Language: language}).validate(); err != nil {
			t.Errorf("valid language %q rejected: %v", language, err)
		}
	}
	owner := &toolServer{caller: unitCaller(func(context.Context, string, any, any) error {
		t.Fatal("invalid language reached the daemon")
		return nil
	})}
	for _, language := range []string{" ", "eng swe", "eng\nswe", "+eng", "eng+", "eng++swe", "script/", "script//Latin", "-l", "../eng", "/eng", "C:\\eng", "eng;echo"} {
		input := ocrInput{Language: language}
		if _, err := owner.getScreenText(t.Context(), nil, input); err == nil || !strings.Contains(err.Error(), "language") {
			t.Errorf("get_screen_text accepted %q: %v", language, err)
		}
		if _, err := owner.executeAndRead(t.Context(), nil, executeInput{ocrInput: input, Command: "must not type"}); err == nil || !strings.Contains(err.Error(), "language") {
			t.Errorf("execute_and_read accepted %q: %v", language, err)
		}
	}
}

func TestOCRLanguagePreservesTesseractFailure(t *testing.T) {
	path := fakeTesseract(t, "fail")
	t.Setenv("AUTOPAWS_OCR_EXPECT_LANGUAGE", "missing_model")
	_, err := extractOCR(t.Context(), path, unitJPEG(t), ocrInput{Language: "missing_model"})
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 17 || !strings.Contains(err.Error(), "fake Tesseract failure") {
		t.Fatalf("language-specific OCR failure lost: %v", err)
	}
}

func TestOCRLanguageRejectsPartialModelLoading(t *testing.T) {
	for _, test := range []struct {
		mode, language string
		wantError      bool
	}{
		{"partial_language", "eng+missing_model", true},
		{"warning", "eng", false},
	} {
		t.Run(test.mode, func(t *testing.T) {
			path := fakeTesseract(t, test.mode)
			t.Setenv("AUTOPAWS_OCR_EXPECT_LANGUAGE", test.language)
			text, err := extractOCR(t.Context(), path, unitJPEG(t), ocrInput{Language: test.language})
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "missing_model") || text != "" {
					t.Fatalf("partial model load returned text %q, error %v", text, err)
				}
			} else if err != nil || text != "recognized fallback text" {
				t.Fatalf("ordinary Tesseract warning lost text: %q, %v", text, err)
			}
		})
	}
}
