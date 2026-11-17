package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeOCRExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fake executable for path lookup only"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestOCRPathResolutionPrecedenceAndMissing(t *testing.T) {
	root := t.TempDir()
	pathDir := filepath.Join(root, "path")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	name := "tesseract"
	if runtime.GOOS == "windows" {
		name += ".exe"
		t.Setenv("PATHEXT", ".EXE")
	}
	pathExecutable := filepath.Join(pathDir, name)
	override := filepath.Join(root, "override", name)
	programFiles, programFilesX86 := filepath.Join(root, "Program Files"), filepath.Join(root, "Program Files (x86)")
	primary := filepath.Join(programFiles, "Tesseract-OCR", "tesseract.exe")
	secondary := filepath.Join(programFilesX86, "Tesseract-OCR", "tesseract.exe")
	for _, path := range []string{pathExecutable, override, primary, secondary} {
		writeOCRExecutable(t, path)
	}
	if got, err := findTesseract(override, "windows", programFiles, programFilesX86); err != nil || got != override {
		t.Fatalf("override=%q,%v", got, err)
	}
	if got, err := resolveTesseract(override); err != nil || got != override {
		t.Fatalf("startup resolver=%q,%v", got, err)
	}
	if got, err := findTesseract(filepath.Join(root, "missing"), "windows", programFiles, programFilesX86); err == nil || got != "" {
		t.Fatalf("bad override fell back=%q,%v", got, err)
	}
	if got, err := findTesseract("", "windows", programFiles, programFilesX86); err != nil || got != pathExecutable {
		t.Fatalf("PATH priority=%q,%v", got, err)
	}
	if err := os.Remove(pathExecutable); err != nil {
		t.Fatal(err)
	}
	if got, err := findTesseract("", "windows", programFiles, programFilesX86); err != nil || got != primary {
		t.Fatalf("ProgramFiles priority=%q,%v", got, err)
	}
	if got, err := findTesseract("", "linux", programFiles, programFilesX86); err == nil || got != "" {
		t.Fatalf("non-Windows used install directories=%q,%v", got, err)
	}
	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := findTesseract("", "windows", programFiles, programFilesX86); err != nil || got != secondary {
		t.Fatalf("directory accepted or x86 fallback missed=%q,%v", got, err)
	}
	if err := os.Remove(secondary); err != nil {
		t.Fatal(err)
	}
	for _, directories := range [][2]string{{programFiles, programFilesX86}, {"", ""}} {
		if got, err := findTesseract("", "windows", directories[0], directories[1]); err == nil || got != "" {
			t.Fatalf("missing executable=%q,%v", got, err)
		}
	}
}
