//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package fsutil

import "os"

// Platforms without flock or LockFileEx run unlocked: graft's writers there
// fall back to the atomic rename alone.
func tryLock(*os.File) (bool, error) { return true, nil }

func unlock(*os.File) error { return nil }
