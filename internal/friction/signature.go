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
