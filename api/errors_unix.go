//go:build (linux || darwin) && (amd64 || arm64)

package api

import (
	"errors"
	"syscall"
)

func invalidLayoutPath(err error) bool { return errors.Is(err, syscall.ENAMETOOLONG) }
