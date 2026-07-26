//go:build unix

// Lock uses syscall.Flock, which exists on unix (darwin/linux/bsd) but not
// Windows. ferret is a unix-only personal tool; this build tag makes that
// explicit so a non-unix build fails loudly here rather than silently shipping
// unguarded concurrent writes.
package durable

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Lock takes an advisory exclusive flock on lockPath (creating it and its parent
// dir if needed) and returns a release func. It serializes any read-modify-write
// or append of a shared file across concurrent processes: the acquire is
// blocking (a second holder waits rather than failing), and the flock frees on
// fd close or process death, so a crashed holder never leaves a stale lock —
// unlike an O_EXCL lockfile, which would brick all future writes after one
// SIGKILL. Pass a dedicated sentinel path (e.g. "<file>.lock"), never the
// guarded file itself.
func Lock(lockPath string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
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
