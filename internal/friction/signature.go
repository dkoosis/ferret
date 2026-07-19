package friction

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// AppendSignatures appends signatures to the JSONL file, creating the file and
// its parent dir on first use (a corpus can be scanned before any signature has
// been recorded). Append-only mirrors the fix ledger: the file grows, never
// rewrites, so a hand-curated label on an existing line is never clobbered. The
// write is flushed and fsync'd so a recorded signature survives a crash. An empty
// slice is a no-op.
func AppendSignatures(path string, sigs []Signature) error {
	if len(sigs) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, s := range sigs {
		b, mErr := json.Marshal(s)
		if mErr != nil {
			return errors.Join(mErr, f.Close())
		}
		if _, wErr := w.Write(append(b, '\n')); wErr != nil {
			return errors.Join(wErr, f.Close())
		}
	}
	if err := w.Flush(); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}

// PersistLearned appends the fingerprints in learned that are not already in
// known to the signatures file, so a signature first seen this run is a KNOWN
// signature next run — the durability that makes cross-run recurrence (occurrence
// 2 in a later process) reachable. It is the delta of learned − known by
// fingerprint; labels ride along. Returns the count appended.
func PersistLearned(path string, known, learned []Signature) (int, error) {
	seen := make(map[string]bool, len(known))
	for _, s := range known {
		seen[s.Fingerprint] = true
	}
	var newSigs []Signature
	for _, s := range learned {
		if s.Fingerprint == "" || seen[s.Fingerprint] {
			continue
		}
		seen[s.Fingerprint] = true // dedupe within learned too
		newSigs = append(newSigs, s)
	}
	if err := AppendSignatures(path, newSigs); err != nil {
		return 0, err
	}
	return len(newSigs), nil
}
