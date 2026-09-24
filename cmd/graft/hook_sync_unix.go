//go:build unix

package main

import "syscall"

// detachedProcAttr starts the background sync in a session of its own, so a
// host that kills the hook's process group does not stop the build midway.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
