package main

import (
	"os"
	"syscall"
	"testing"
)

func TestCaptureLogPermissionProtection(t *testing.T) {
	// An existing directory must be protected too, rather than inheriting the
	// permissive mode of a directory that a user created before enabling logs.
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeCaptureLog(directory, "capture_screen", unitJPEG(t)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("capture log directory is not private: %v,%v", info, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("capture log file is not private: %v,%v", info, err)
		}
	}
	// The system temporary directory belongs to another user. A non-root
	// process cannot change its permissions, and must report that failure before
	// creating a screenshot there. The ownership check prevents any chmod when
	// this guarantee does not hold (for example in a root-run container).
	info, err = os.Stat(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 || info.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid()) {
		t.Skip("permission-denial probe needs a temporary directory owned by another user")
	}
	if err := writeCaptureLog(os.TempDir(), "capture_screen", unitJPEG(t)); err == nil {
		t.Fatal("directory protection failure was suppressed")
	}
}
