//go:build unix

// The ledger append lock uses syscall.Flock, which exists on unix
// (darwin/linux/bsd) but not Windows. ferret is a unix-only personal tool; this
// build tag makes that explicit so a non-unix build fails loudly here rather
// than silently shipping unguarded concurrent appends. Mirrors internal/fixes.
package label

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lockLedger takes an advisory exclusive lock so two concurrent Append calls
// serialize instead of interleaving their writes. An O_APPEND write is atomic
// only up to a filesystem threshold; a "say-more" Text can push one label past
// it, so two concurrent over-threshold appends could splice into one torn line
// that Load then refuses to salvage. Serializing the writers removes the race.
//
// The sentinel is label-specific (.labels.lock), NOT the shared dir lock:
// keying the lock on the data dir would over-serialize label appends against the
// unrelated fixes ledger in the same dir (the trap noted in the fixes audit).
// The flock releases when the fd closes (or the process dies), so a crash never
// leaves a stale lock. The acquire is blocking: the second appender waits.
func lockLedger(path string) (release func(), err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".labels.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
