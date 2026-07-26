package fixes

import (
	"path/filepath"

	"github.com/dkoosis/ferret/internal/durable"
)

// lockLedger takes an advisory exclusive lock on the ledger's data dir so two
// `ferret fixes add` calls serialize instead of interleaving their appends. An
// O_APPEND write is atomic only up to a filesystem threshold; a user --note can
// push one entry past it, so two concurrent over-threshold appends can splice
// into one torn, over-long mid-ledger line that Load then refuses to salvage
// (ferret-isz). Serializing the writers removes the race at its source.
//
// The sentinel (.fixes.lock) is dir-scoped to the ledger; durable.Lock owns the
// blocking, crash-safe flock mechanism (and the unix-only build constraint).
func lockLedger(path string) (release func(), err error) {
	return durable.Lock(filepath.Join(filepath.Dir(path), ".fixes.lock"))
}
