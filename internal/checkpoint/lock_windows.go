//go:build windows

package checkpoint

import (
	"os"

	"golang.org/x/sys/windows"
)

// allBytes locks the whole file: LockFileEx takes the byte-range length as
// two uint32 halves.
const allBytes = ^uint32(0)

// lockFile blocks until an exclusive LockFileEx lock on f is held. Without
// LOCKFILE_FAIL_IMMEDIATELY the call waits, matching flock(LOCK_EX).
func lockFile(f *os.File) error {
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, allBytes, allBytes,
		new(windows.Overlapped),
	)
}

// unlockFile releases the LockFileEx lock held on f.
func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0, allBytes, allBytes,
		new(windows.Overlapped),
	)
}
