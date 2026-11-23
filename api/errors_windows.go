//go:build windows && (amd64 || arm64)

package api

import (
	"errors"

	"golang.org/x/sys/windows"
)

func invalidLayoutPath(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_NAME) || errors.Is(err, windows.ERROR_FILENAME_EXCED_RANGE)
}
