package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Path resolution happens only at startup. A bad explicit override never falls
// back to a different executable; OCR stays optional in either case.
func resolveTesseract(override string) (string, error) {
	return findTesseract(override, runtime.GOOS, os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"))
}

func findTesseract(override, goos, programFiles, programFilesX86 string) (string, error) {
	if override != "" {
		path, err := exec.LookPath(override)
		if err != nil {
			return "", fmt.Errorf("-tesseract %q: %w", override, err)
		}
		return path, nil
	}
	path, lookupErr := exec.LookPath("tesseract")
	if lookupErr == nil {
		return path, nil
	}
	if goos == "windows" {
		for _, directory := range []string{programFiles, programFilesX86} {
			if directory == "" {
				continue
			}
			path := filepath.Join(directory, "Tesseract-OCR", "tesseract.exe")
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
		}
	}
	return "", lookupErr
}
