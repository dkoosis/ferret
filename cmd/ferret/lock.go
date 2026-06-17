//go:build unix

// The ingest lock uses syscall.Flock, which exists on unix (darwin/linux/bsd)
// but not Windows. ferret is a unix-only personal tool; this constraint makes
// that assumption explicit so a non-unix build fails loudly here rather than
// silently shipping unprotected concurrent ingest.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lockData takes an advisory exclusive lock on the data dir so two ingests
// racing on the same ~/.ferret serialize instead of both writing events.jsonl
// at once (ferret-0vz). ensureData makes ingest a side effect of every mining
// subcommand, so two ferret processes on a fresh data dir both decide to ingest;
// the unique-temp write keeps the published artifact from interleaving, and this
// lock keeps them from doing the redundant concurrent work in the first place.
//
// The lock is a flock on a sentinel file. flock releases automatically when the
// fd closes (or the process dies), so a crash never leaves a stale lock — unlike
// an O_EXCL lockfile, which would brick all future ingests after one SIGKILL.
// The acquire is blocking: the second ingest waits for the first to finish
// rather than failing.
func lockData(dataDir string) (release func(), err error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, ".ingest.lock"), os.O_CREATE|os.O_RDWR, 0o644)
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
