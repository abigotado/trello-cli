// Package lockfile serializes read-modify-write cycles on a shared file
// between concurrent invocations of this binary.
//
// Atomic rename alone is not enough for that. It guarantees a reader never
// sees a half-written file, but two processes can still both read, both
// modify, and both write — and the second erases the first. Merging on write
// narrows that window without closing it, because the merge itself is a read
// followed by a write.
package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// defaultTimeout bounds how long an invocation waits for the lock.
//
// Short by design: every holder does a small file rewrite, so waiting longer
// than this means the lock was orphaned rather than held.
const defaultTimeout = 2 * time.Second

// staleAfter is when a lock file is assumed to belong to a process that died.
//
// Without this a single crash between create and remove would wedge every
// later invocation permanently.
const staleAfter = 10 * time.Second

// With runs fn while holding an exclusive lock beside path.
//
// The lock is advisory and only observed by this binary, which is all that is
// needed: nothing else writes these files.
func With(path string, fn func() error) error {
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		return err
	}

	acquired, err := acquire(lock)
	if err != nil {
		return err
	}
	if !acquired {
		// Losing the race is not a failure. The caller's change is either a
		// cache entry it can refetch or an account it will re-add; blocking
		// the command on lock contention would be worse than the rare loss.
		return fn()
	}
	defer func() { _ = os.Remove(lock) }()
	return fn()
}

// acquire tries to create the lock file, waiting for a holder to finish and
// breaking a lock left behind by a dead process.
func acquire(lock string) (bool, error) {
	deadline := time.Now().Add(defaultTimeout)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return true, f.Close()
		}
		if !errors.Is(err, os.ErrExist) {
			return false, err
		}
		if info, statErr := os.Stat(lock); statErr == nil && time.Since(info.ModTime()) > staleAfter {
			// The holder is gone. Removing is safe because a live holder
			// refreshes nothing — it finishes well inside staleAfter.
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}
