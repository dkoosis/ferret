//go:build unix

// The budget lock uses syscall.Flock (unix-only). ferret is a unix-only personal
// tool; this build tag makes that explicit so a non-unix build fails loudly here
// rather than silently shipping an unguarded shared cap. Mirrors internal/fixes
// and internal/label.
package feedback

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lockBudget takes an advisory exclusive lock so the whole read-check-write of
// Reserve is atomic across concurrent sessions — without it two sessions could
// both read the same "spent" count and each grant one past the shared daily cap.
//
// The sentinel is budget-specific (.feedback-budget.lock); the flock releases
// when the fd closes (or the process dies), so a crashed session never leaves a
// stale lock. The acquire is blocking: the second session waits its turn.
func lockBudget(path string) (release func(), err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".feedback-budget.lock"), os.O_CREATE|os.O_RDWR, 0o644)
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
