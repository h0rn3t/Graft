// Package fsutil holds the file primitives graft shares across packages:
// atomic replacement that keeps a file's mode, and an advisory lock that dies
// with its holder.
package fsutil

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces path with data through a synced temporary file in
// the same directory, so a reader sees the old content or the new content and
// never a torn write. A symlink at path is followed, so a user's dotfile link
// keeps pointing at its target. An existing file keeps its mode; a new file
// gets perm. Missing parent directories are created.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }() // nothing to flush; the write result is what matters
	return WriteFileAtomicIn(root, filepath.Base(path), data, perm)
}

// WriteFileAtomicIn is WriteFileAtomic confined to root: name and every
// directory on the way to it resolve inside root, so a symlink planted in a
// repository cannot redirect the write outside it. A symlink at name itself
// is replaced by a regular file rather than written through.
func WriteFileAtomicIn(root *os.Root, name string, data []byte, perm fs.FileMode) error {
	if dir := filepath.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	mode := perm
	if info, err := root.Lstat(name); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}
	temporary := name + "." + rand.Text()[:10] + ".tmp"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temporary) }() // fails harmlessly once renamed
	if _, err := file.Write(data); err != nil {
		_ = file.Close() // the write error is the one to report
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close() // the sync error is the one to report
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Chmod(temporary, mode); err != nil {
		return err
	}
	return root.Rename(temporary, name)
}
