package label

import (
	"path/filepath"

	"github.com/dkoosis/ferret/internal/durable"
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
// durable.Lock owns the flock mechanism (and the unix-only build constraint).
func lockLedger(path string) (release func(), err error) {
	return durable.Lock(filepath.Join(filepath.Dir(path), ".labels.lock"))
}
