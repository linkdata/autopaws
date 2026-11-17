package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureLogOpeningErrors(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(filePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCaptureLog(filePath, "capture_screen", []byte("jpeg")); err == nil {
		t.Fatal("log directory pointing at a file was accepted")
	}
	directory := t.TempDir()
	if err := writeCaptureLog(directory, "invalid/tool", []byte("jpeg")); err == nil {
		t.Fatal("temporary file creation failure was suppressed")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed creation left a file: %v,%v", entries, err)
	}
}

type failedCaptureFile struct {
	path, contents     string
	writeErr, closeErr error
	closed             bool
}

func (file *failedCaptureFile) Name() string { return file.path }

func (file *failedCaptureFile) Write(data []byte) (int, error) {
	file.contents = string(data)
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return len(data), nil
}

func (file *failedCaptureFile) Close() error {
	file.closed = true
	return file.closeErr
}

func TestCaptureLogWriteAndCloseFailuresRemovePartialFile(t *testing.T) {
	writeFailure, closeFailure := errors.New("disk full"), errors.New("flush failed")
	for _, test := range []struct {
		name               string
		writeErr, closeErr error
	}{
		{"write", writeFailure, nil},
		{"close", nil, closeFailure},
		{"write and close", writeFailure, closeFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "partial.jpg")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			file := &failedCaptureFile{path: path, writeErr: test.writeErr, closeErr: test.closeErr}
			err := writeCaptureFile(file, []byte("unchanged JPEG bytes"))
			if !file.closed || file.contents != "unchanged JPEG bytes" || test.writeErr != nil && !errors.Is(err, test.writeErr) || test.closeErr != nil && !errors.Is(err, test.closeErr) {
				t.Fatalf("write/close failure lost: file=%+v error=%v", file, err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial file remained: %v", err)
			}
		})
	}
}

func TestCaptureLogPreservesBytesAndUsesUniquePrivateFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "captures")
	data := unitJPEG(t)
	for range 2 {
		if err := writeCaptureLog(directory, "capture_screen", data); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("captures overwrite each other: %v,%v", entries, err)
	}
	for _, entry := range entries {
		got, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("logged JPEG changed: %v", err)
		}
	}
}
