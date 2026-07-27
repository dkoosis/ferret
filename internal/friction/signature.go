package friction

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/durable"
)

// SigFileName is the basename of the known-signatures file under the ferret data
// dir. It mirrors the fix ledger (fixes.jsonl): a passive, append-friendly JSONL
// export, one Signature per line. v1 sources known signatures from this file; a
// later bead can populate it from friction nugs without changing this reader.
const SigFileName = "friction_signatures.jsonl"

// SigPath returns the signatures file path for a ferret data dir.
func SigPath(dataDir string) string { return filepath.Join(dataDir, SigFileName) }

// LoadSignatures reads every Signature from a JSONL file in order. A missing
// file is an empty set, not an error, so a caller can seed a detector
// unconditionally before any signature has been recorded — an empty seed simply
// makes the detector learn signatures from the stream (within-corpus recurrence).
//
// Blank lines are skipped. A malformed line is a hard error naming the file and
// line: a signature file is small and hand- or tool-curated, so silent salvage
// would hide a real authoring mistake rather than tolerate expected corruption.
func LoadSignatures(path string) ([]Signature, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Signature
	r := bufio.NewReader(f)
	line := 0
	for {
		raw, rerr := r.ReadString('\n')
		line++
		if s := strings.TrimSpace(raw); s != "" {
			var sig Signature
			if err := json.Unmarshal([]byte(s), &sig); err != nil {
				return nil, fmt.Errorf("friction: %s:%d: malformed signature: %w", path, line, err)
			}
			out = append(out, sig)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return out, nil
			}
			return nil, rerr
		}
	}
}

// PersistLearned makes the learned corpus durable so a fingerprint first seen
// this run is a KNOWN signature next run — the durability that makes cross-run
// recurrence (occurrence 2 in a later process) reachable.
//
// It rewrites the WHOLE file atomically (temp + fsync + rename, mirroring
// cmd/gen-corpus writeAtomic) from the deduped union of what is ON DISK NOW and
// the learned set. Deduping against the freshly-reloaded file — not the caller's
// stale in-memory view — is what prevents duplicate lines when two runs persist:
// each run re-reads, unions, and replaces, so the file can never accumulate a
// repeated fingerprint (worst case under concurrency is a lost update, never
// corruption). Rewriting whole also repairs a pre-existing file that lacked a
// trailing newline, which a blind append would have fused into an unparseable
// line. Existing labels win over learned ones (curated text is never clobbered).
// Returns the count of fingerprints newly added to the file.
//
// The whole load-merge-rewrite runs under lockSignatures so two concurrent
// callers (e.g. two `ferret recurrence` runs) cannot each load the same
// on-disk set, merge their own learned signature, and rewrite: without the
// lock, the second writer's rewrite silently drops the first writer's
// addition (a lost update, not corruption — but the dropped signature never
// becomes durable).
func PersistLearned(path string, learned []Signature) (int, error) {
	release, err := lockSignatures(path)
	if err != nil {
		return 0, err
	}
	defer release()

	onDisk, err := LoadSignatures(path)
	if err != nil {
		return 0, err
	}
	labels, added := mergeSignatures(onDisk, learned)
	if added == 0 && len(onDisk) == len(labels) {
		// No new fingerprints and the on-disk set is already dedup-clean
		// (parsed fine above): leave the file untouched.
		return 0, nil
	}
	if err := writeSignaturesAtomic(path, sortedSignatures(labels)); err != nil {
		return 0, err
	}
	return added, nil
}

// mergeSignatures folds learned into the on-disk set, keyed by fingerprint, and
// returns the fp→label map plus the count of fingerprints not previously on disk.
// Empty fingerprints are dropped; an existing (curated) label is never clobbered
// by a learned one, though a missing label is filled.
func mergeSignatures(onDisk, learned []Signature) (labels map[string]string, added int) {
	labels = make(map[string]string, len(onDisk)+len(learned))
	for _, s := range onDisk {
		if s.Fingerprint != "" {
			labels[s.Fingerprint] = s.Label
		}
	}
	for _, s := range learned {
		if s.Fingerprint == "" {
			continue
		}
		existing, ok := labels[s.Fingerprint]
		if !ok {
			labels[s.Fingerprint] = s.Label
			added++
			continue
		}
		if existing == "" && s.Label != "" {
			labels[s.Fingerprint] = s.Label // fill a missing label, don't clobber
		}
	}
	return labels, added
}

// sortedSignatures renders the fp→label map as a slice sorted by fingerprint for
// a deterministic on-disk file order.
func sortedSignatures(labels map[string]string) []Signature {
	fps := make([]string, 0, len(labels))
	for fp := range labels {
		fps = append(fps, fp)
	}
	sort.Strings(fps)
	sigs := make([]Signature, len(fps))
	for i, fp := range fps {
		sigs[i] = Signature{Fingerprint: fp, Label: labels[fp]}
	}
	return sigs
}

// writeSignaturesAtomic writes the full signature set to path atomically via
// durable.WriteTempRename (temp+fsync+rename), then fsyncs the parent dir so the
// rename survives a crash. A rewrite, not an append, because the set is
// recomputed whole.
func writeSignaturesAtomic(path string, sigs []Signature) error {
	var buf bytes.Buffer
	for _, s := range sigs {
		b, err := json.Marshal(s)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := durable.WriteTempRename(path, buf.Bytes()); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir fsyncs a directory so a just-published rename within it is durable
// (see durable.SyncDir). A package var so a test can observe/override it.
var syncDir = durable.SyncDir

// lockSignatures takes an advisory exclusive lock on the signatures file's
// data dir so two concurrent PersistLearned calls serialize instead of
// interleaving their load-merge-rewrite. Mirrors the flock-around-RMW pattern
// already used by internal/fixes/lock.go (lockLedger, for RecordSub) and
// internal/feedback/budget.go (lockBudget, for Reserve) — built by ferret-isz
// (merged #63) for the analogous fixes-ledger race.
//
// The sentinel (.friction_signatures.lock) is dir-scoped to the signatures
// file; durable.Lock owns the blocking, crash-safe flock mechanism.
func lockSignatures(path string) (release func(), err error) {
	return durable.Lock(filepath.Join(filepath.Dir(path), ".friction_signatures.lock"))
}
