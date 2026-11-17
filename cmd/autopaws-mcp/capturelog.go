package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

func writeCaptureLog(directory, tool string, data []byte) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create capture log directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("protect capture log directory: %w", err)
	}
	file, err := os.CreateTemp(directory, time.Now().UTC().Format("20060102T150405.000000000Z")+"_"+tool+"_*.jpg")
	if err != nil {
		return fmt.Errorf("create capture log: %w", err)
	}
	return writeCaptureFile(file, data)
}

type captureLogFile interface {
	io.WriteCloser
	Name() string
}

// Keep file completion independent of opening so write/close failures have the
// same cleanup behavior, including a filesystem that rejects a delayed flush.
func writeCaptureFile(file captureLogFile, data []byte) error {
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(file.Name())
		return fmt.Errorf("write capture log: %w", errors.Join(writeErr, closeErr))
	}
	return nil
}
