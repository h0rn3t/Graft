//go:build windows

package main

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// detachedProcAttr starts the background sync in its own process group with
// no console, so the host's console signals do not reach it and no window
// flashes up.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
		HideWindow:    true,
	}
}
