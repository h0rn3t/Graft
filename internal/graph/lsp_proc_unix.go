//go:build unix

package graph

import (
	"os/exec"
	"syscall"
)

// startInOwnProcessGroup makes the server lead a new process group, so the
// helpers it spawns can be killed with it.
func startInOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the server's whole process group.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
