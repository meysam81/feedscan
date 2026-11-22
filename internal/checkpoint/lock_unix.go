//go:build unix

package checkpoint

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile blocks until an exclusive flock(2) on f is held.
func lockFile(f *os.File) error {
	return unix.Flock(fd(f), unix.LOCK_EX)
}

// unlockFile releases the flock(2) held on f.
func unlockFile(f *os.File) error {
	return unix.Flock(fd(f), unix.LOCK_UN)
}

// fd converts *os.File to the int file descriptor unix.Flock expects. The
// conversion from uintptr is safe in practice — file descriptors fit in an
// int on every supported platform — but gosec G115 flags the cast, so we
// isolate it in one place we can audit.
func fd(f *os.File) int {
	return int(f.Fd()) //nolint:gosec // file descriptor always fits in int
}
