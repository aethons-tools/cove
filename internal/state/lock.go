package state

import (
	"errors"
	"os"
	"syscall"
)

// ErrLocked indicates the state lock is held by another process: a shared lock
// (an open connection) blocks the exclusive lock destroy needs, and an
// exclusive lock (a destroy in progress) blocks a shared lock.
//
// Locking is advisory (flock) and Unix-only (darwin + linux — the supported
// targets). There is a small TOCTOU window between acquiring the exclusive lock
// and unlinking the file; that matches the "no connections we know of" guarantee.
var ErrLocked = errors.New("state file is locked by another process")

// Lock is a held advisory lock on the state file.
type Lock struct{ f *os.File }

// Release drops the lock and closes the file. Safe on a nil/zero Lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil
	return err
}

func acquireFor(kitDir string, inst Instance, how int) (*Lock, error) {
	f, err := os.OpenFile(PathFor(kitDir, inst), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotCreated
	}
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &Lock{f: f}, nil
}

// AcquireSharedFor takes a shared (read) lock on the given instance's state
// file — many holders may share it at once.
func AcquireSharedFor(kitDir string, inst Instance) (*Lock, error) {
	return acquireFor(kitDir, inst, syscall.LOCK_SH)
}

// AcquireExclusiveFor takes an exclusive (write) lock on the given instance's
// state file; returns ErrLocked if any shared or exclusive lock is held.
func AcquireExclusiveFor(kitDir string, inst Instance) (*Lock, error) {
	return acquireFor(kitDir, inst, syscall.LOCK_EX)
}

// AcquireShared takes a shared lock on the interactive instance.
func AcquireShared(kitDir string) (*Lock, error) { return AcquireSharedFor(kitDir, Interactive) }

// AcquireExclusive takes an exclusive lock on the interactive instance.
func AcquireExclusive(kitDir string) (*Lock, error) { return AcquireExclusiveFor(kitDir, Interactive) }
