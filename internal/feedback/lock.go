package feedback

import (
	"path/filepath"

	"github.com/dkoosis/ferret/internal/durable"
)

// Lock takes an advisory exclusive flock on lockPath and returns a release func,
// for any caller that must serialize a read-modify-write of a shared file. It is
// a thin re-export of durable.Lock kept because cmdFeedbackPrep holds a
// per-session lock through this package's boundary. Pass a dedicated sentinel
// (e.g. "<file>.lock"), never the guarded file itself.
func Lock(lockPath string) (release func(), err error) {
	return durable.Lock(lockPath)
}

// lockBudget takes an advisory exclusive lock so the whole read-check-write of
// Reserve is atomic across concurrent sessions — without it two sessions could
// both read the same "spent" count and each grant one past the shared daily cap.
// The sentinel is budget-specific (.feedback-budget.lock).
func lockBudget(path string) (release func(), err error) {
	return durable.Lock(filepath.Join(filepath.Dir(path), ".feedback-budget.lock"))
}
