//go:build !windows

package ffmpegmedia

import "os/exec"

func ConfigureCommand(*exec.Cmd) {}
