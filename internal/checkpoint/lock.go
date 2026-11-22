package checkpoint

import (
	"errors"
	"fmt"
	"os"
)

// FileLock is an advisory OS-level lock on a sidecar `.lock` file. Use it
// around load→mutate→save sequences so two concurrent `mark` invocations
// can't lose updates.
//
// Why a sidecar (not the checkpoint file itself): atomic rename in Save
// replaces the underlying inode, which would silently release a flock held
// on the old inode.
//
// The locking primitive is platform-specific: flock(2) on Unix
// (lock_unix.go) and LockFileEx on Windows (lock_windows.go).
type FileLock struct {
	f *os.File
}

// AcquireLock blocks until the lock on path+".lock" is held exclusively.
// The lock is released by Close. Lock files are created with 0o600.
func AcquireLock(path string) (*FileLock, error) {
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := lockFile(f); err != nil {
		return nil, errors.Join(fmt.Errorf("acquire lock: %w", err), f.Close())
	}
	return &FileLock{f: f}, nil
}

// Close releases the lock and closes the lock file.
func (l *FileLock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlockErr := unlockFile(l.f)
	closeErr := l.f.Close()
	return errors.Join(unlockErr, closeErr)
}
