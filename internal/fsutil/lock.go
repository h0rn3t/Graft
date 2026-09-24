package fsutil

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// lockPoll is how often Lock retries a lock another process holds.
const lockPoll = 25 * time.Millisecond

// Lock takes an exclusive advisory lock on path, creating the file and its
// directory when missing, and waits until the lock is free or ctx is done.
// The operating system drops the lock when the holder exits, so a crashed
// process never leaves a stale lock behind. release unlocks and closes the
// file; the lock file itself stays, since removing it would race a waiter.
func Lock(ctx context.Context, path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G304: callers pass graft-owned cache paths
	if err != nil {
		return nil, err
	}
	for {
		locked, err := tryLock(file)
		if err != nil {
			_ = file.Close() // the lock error is the one to report
			return nil, err
		}
		if locked {
			return func() {
				_ = unlock(file) // closing the file releases the lock anyway
				_ = file.Close() // nothing was written through this handle
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close() // the context error is the one to report
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
