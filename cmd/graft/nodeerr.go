package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// nodeErrno maps an errno to the code and libuv description Node puts in a
// file-system error's message.
var nodeErrno = map[syscall.Errno][2]string{
	syscall.ENOENT:    {"ENOENT", "no such file or directory"},
	syscall.ENOTDIR:   {"ENOTDIR", "not a directory"},
	syscall.EACCES:    {"EACCES", "permission denied"},
	syscall.EPERM:     {"EPERM", "operation not permitted"},
	syscall.EISDIR:    {"EISDIR", "illegal operation on a directory"},
	syscall.EEXIST:    {"EEXIST", "file already exists"},
	syscall.ENOSPC:    {"ENOSPC", "no space left on device"},
	syscall.EROFS:     {"EROFS", "read-only file system"},
	syscall.EMFILE:    {"EMFILE", "too many open files"},
	syscall.ENOTEMPTY: {"ENOTEMPTY", "directory not empty"},
}

// nodeFSError is a file-system failure carrying Node's error message,
// "ENOENT: no such file or directory, scandir '/path'", and its errno.
type nodeFSError struct {
	message string
	errno   syscall.Errno
}

func (err *nodeFSError) Error() string { return err.message }

func (err *nodeFSError) Unwrap() error { return err.errno }

func newNodeFSError(err error, syscallName, path string) (*nodeFSError, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return nil, false
	}
	known, ok := nodeErrno[errno]
	if !ok {
		return nil, false
	}
	return &nodeFSError{message: fmt.Sprintf("%s: %s, %s '%s'", known[0], known[1], syscallName, path), errno: errno}, true
}

// buildRootError is the error the TypeScript build throws first for a root it
// cannot list: its walk falls back to readdir, which fails with scandir.
func buildRootError(root string) (*nodeFSError, bool) {
	info, err := os.Stat(root)
	if err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return newNodeFSError(pathErr.Err, "scandir", root)
		}
		return nil, false
	}
	if !info.IsDir() {
		return newNodeFSError(syscall.ENOTDIR, "scandir", root)
	}
	return nil, false
}
