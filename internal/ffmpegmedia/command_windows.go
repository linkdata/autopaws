package ffmpegmedia

import (
	"os/exec"
	"syscall"
)

// ConfigureCommand prevents FFmpeg from opening a separate console window.
func ConfigureCommand(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.HideWindow = true
}
