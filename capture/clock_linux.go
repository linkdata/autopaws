//go:build linux && (amd64 || arm64)

package capture

import (
	"syscall"
	"unsafe"
)

func monotonicNow() (int64, error) {
	var timestamp syscall.Timespec
	const clockMonotonic = 1
	_, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockMonotonic, uintptr(unsafe.Pointer(&timestamp)), 0)
	if errno != 0 {
		return 0, errno
	}
	return timestamp.Nano(), nil
}
