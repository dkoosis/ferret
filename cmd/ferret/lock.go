package main

import (
	"path/filepath"

	"github.com/dkoosis/ferret/internal/durable"
)

// lockData takes an advisory exclusive lock on the data dir so two ingests
// racing on the same ~/.ferret serialize instead of both writing events.jsonl
// at once (ferret-0vz). ensureData makes ingest a side effect of every mining
// subcommand, so two ferret processes on a fresh data dir both decide to ingest;
// the unique-temp write keeps the published artifact from interleaving, and this
// lock keeps them from doing the redundant concurrent work in the first place.
//
// The sentinel (.ingest.lock) sits in the data dir; durable.Lock owns the
// blocking, crash-safe flock mechanism (and the unix-only build constraint).
func lockData(dataDir string) (release func(), err error) {
	return durable.Lock(filepath.Join(dataDir, ".ingest.lock"))
}
