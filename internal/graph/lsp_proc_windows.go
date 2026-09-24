//go:build windows

package graph

import (
	"os/exec"
	"strconv"
	"syscall"
)

// startInOwnProcessGroup starts the server in a new process group, detached
// from the console's Ctrl+C handling of graft itself.
func startInOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killProcessGroup kills the server and the processes it started. Windows has
// no signal for a process group, so taskkill walks the process tree; the
// direct kill covers a system without taskkill.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	tree := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	if err := tree.Run(); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
