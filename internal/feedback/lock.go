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

// Lock takes an advisory exclusive flock on lockPath (creating it and its parent
// dir if needed) and returns a release func. It generalizes the discipline
// lockBudget uses — a blocking LOCK_EX that frees on fd close or process death,
// so a crashed holder never leaves a stale lock — for any caller that must
// serialize a read-modify-write of a shared file. Pass a dedicated sentinel
// (e.g. "<file>.lock"), never the guarded file itself.
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

// lockBudget takes an advisory exclusive lock so the whole read-check-write of
// Reserve is atomic across concurrent sessions — without it two sessions could
// both read the same "spent" count and each grant one past the shared daily cap.
// The sentinel is budget-specific (.feedback-budget.lock).
func lockBudget(path string) (release func(), err error) {
	return Lock(filepath.Join(filepath.Dir(path), ".feedback-budget.lock"))
}
