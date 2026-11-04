package replication

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// LockPath is the lock file that keeps the daemon and `btrepl run` from
// running replication cycles at the same time.
const LockPath = "/run/btrepl.lock"

// ErrCycleRunning is returned by RunCycle when another cycle holds the lock.
var ErrCycleRunning = errors.New("another replication cycle is running")

// lock takes an exclusive flock on path without waiting. The lock is
// released by unlock or when the process exits.
func lock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrCycleRunning
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { f.Close() }, nil
}
