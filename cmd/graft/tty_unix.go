//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// isTerminal reports whether file is a terminal, like Node's isTTY.
func isTerminal(file *os.File) bool {
	_, err := unix.IoctlGetTermios(int(file.Fd()), ioctlGetTermios)
	return err == nil
}

// makeRaw puts the terminal into raw mode and returns a restore function.
func makeRaw(file *os.File) (func(), error) {
	fd := int(file.Fd())
	saved, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	raw := *saved
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &raw); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, saved) }, nil // best effort: the process is exiting anyway
}
