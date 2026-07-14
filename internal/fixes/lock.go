//go:build unix

// The ledger append lock uses syscall.Flock, which exists on unix
// (darwin/linux/bsd) but not Windows. ferret is a unix-only personal tool; this
// build tag makes that explicit so a non-unix build fails loudly here rather
// than silently shipping unguarded concurrent appends. Mirrors cmd/ferret/lock.go.
package fixes

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lockLedger takes an advisory exclusive lock on the ledger's data dir so two
// `ferret fixes add` calls serialize instead of interleaving their appends. An
// O_APPEND write is atomic only up to a filesystem threshold; a user --note can
// push one entry past it, so two concurrent over-threshold appends can splice
// into one torn, over-long mid-ledger line that Load then refuses to salvage
// (ferret-isz). Serializing the writers removes the race at its source.
//
// The lock is a flock on a sentinel file that releases when the fd closes (or
// the process dies), so a crash never leaves a stale lock — unlike an O_EXCL
// lockfile, which would brick all future appends after one SIGKILL. The acquire
// is blocking: the second appender waits rather than failing.
func lockLedger(path string) (release func(), err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".fixes.lock"), os.O_CREATE|os.O_RDWR, 0o644)
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
