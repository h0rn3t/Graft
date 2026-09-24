//go:build !unix && !windows

package graph

import "os/exec"

// startInOwnProcessGroup is a no-op where process groups are unavailable.
func startInOwnProcessGroup(*exec.Cmd) {}

// killProcessGroup kills the server process itself.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
