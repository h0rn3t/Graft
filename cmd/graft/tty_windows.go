//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// isTerminal reports whether file is a console, like Node's isTTY.
func isTerminal(file *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(file.Fd()), &mode) == nil
}

// terminalSize returns the console window's width and height in cells.
func terminalSize(file *os.File) (int, int, error) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(file.Fd()), &info); err != nil {
		return 0, 0, err
	}
	return int(info.Window.Right-info.Window.Left) + 1, int(info.Window.Bottom-info.Window.Top) + 1, nil
}

// makeRaw switches the console to raw virtual-terminal input and returns a restore function.
func makeRaw(file *os.File) (func(), error) {
	handle := windows.Handle(file.Fd())
	var saved uint32
	if err := windows.GetConsoleMode(handle, &saved); err != nil {
		return nil, err
	}
	raw := saved &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_LINE_INPUT)
	raw |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := windows.SetConsoleMode(handle, raw); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(handle, saved) }, nil // best effort: the process is exiting anyway
}
