package fixes

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/durable"
)

// SubFileName is the substitution ledger's basename under the ferret data dir.
// Kept separate from FileName (the burn-fix ledger): a tool-for-intent
// substitution has no burn baseline and no baseline→current delta — it carries
// an occurrence count instead — so the two ledgers share the package's lock +
// tolerant-read machinery but not the Entry shape (ferret-kuv.15).
const SubFileName = "substitutions.jsonl"

// Substitution is one confirmed tool-for-intent mismatch, distilled from a
// `ferret adjudicate` finding (fit=mismatch) into a durable, executable
// substitution rule a hook or CLAUDE.md nudge can consume. Unlike the burn
// ledger's append-only Entry, a substitution is deduped on write: the same
// (IntentClass, Better) pair bumps Occurrences rather than adding a row, so the
// ledger reads as a rule set, not an event log.
type Substitution struct {
	// IntentClass is the normalized task intent the mismatch belongs to — the
	// dedup key's first half (e.g. "symbol-callers", "symbol-def"). Kebab, stable
	// across sessions so the same class recorded next month bumps the same rule.
	IntentClass string `json:"intentClass"`
	// WrongTool is the tool actually reached for (e.g. "rg", "grep").
	WrongTool string `json:"wrongTool"`
	// Better is the executable substitution template — the fix a consumer runs
	// instead (e.g. "snipe callers <sym>"). Dedup key's second half.
	Better string `json:"better"`
	// Example is one verbatim offending call, for traceability (the most recent
	// wins on a dedup bump).
	Example string `json:"example,omitempty"`
	// Session is the source session prefix the rule was last confirmed in.
	Session string `json:"session,omitempty"`
	// Occurrences counts how many confirmed mismatches folded into this rule.
	Occurrences int `json:"occurrences"`
	// AddedAt is when the rule was first recorded; UpdatedAt moves on each bump.
	AddedAt   time.Time `json:"addedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// subKey is the canonical dedup key: intent class + substitution template. Two
// findings that map the same intent to the same fix are the same rule, however
// their wrong-tool or example differ. Escaped-joined so a '|' inside a field
// can't collide two distinct keys (mirrors MotifKey's escape discipline).
func (s Substitution) subKey() string {
	return escapeField(s.IntentClass) + "|" + escapeField(s.Better)
}

// escapeField escapes '\' and '|' so a field value can't forge the subKey
// separator.
func escapeField(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "|", `\|`)
}

// SubPath returns the substitution-ledger path for a ferret data dir.
func SubPath(dataDir string) string { return filepath.Join(dataDir, SubFileName) }

// LoadSubs reads the substitution ledger, tolerating a missing file (no rules
// yet → empty) and salvaging a single corrupt trailing line the same way Load
// does for the burn ledger.
func LoadSubs(path string) ([]Substitution, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Substitution{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	lines, err := readLedgerLines(f)
	if err != nil {
		return nil, err
	}
	return parseSubs(path, lines)
}

// parseSubs unmarshals each line into a Substitution, salvaging a single corrupt
// TRAILING line the way parseLedger does for the burn ledger (see Load).
func parseSubs(path string, lines []string) ([]Substitution, error) {
	out := make([]Substitution, 0, len(lines))
	for i, line := range lines {
		var s Substitution
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			if i == len(lines)-1 {
				fmt.Fprintf(stderr, "fixes: %s: corrupt trailing substitution line dropped (1); re-record to repair\n", path)
				break
			}
			return nil, fmt.Errorf("fixes: corrupt substitution line in %s: %w", path, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// RecordSub folds one confirmed substitution into the ledger under the data-dir
// lock: if a rule with the same subKey exists it bumps Occurrences and refreshes
// Example/Session/WrongTool/UpdatedAt; otherwise it appends a new rule with
// Occurrences 1. The whole set is rewritten atomically (temp + rename) so a
// crash mid-write leaves the prior ledger intact, never a half-set. now is
// passed in (not read from the clock) so callers stay deterministic under test.
// Returns the resulting rule and whether it was newly created.
func RecordSub(path string, in Substitution, now time.Time) (Substitution, bool, error) {
	release, err := lockLedger(path)
	if err != nil {
		return Substitution{}, false, err
	}
	defer release()

	subs, err := LoadSubs(path)
	if err != nil {
		return Substitution{}, false, err
	}

	key := in.subKey()
	created := true
	for i := range subs {
		if subs[i].subKey() != key {
			continue
		}
		subs[i].Occurrences++
		subs[i].UpdatedAt = now
		if in.Example != "" {
			subs[i].Example = in.Example
		}
		if in.Session != "" {
			subs[i].Session = in.Session
		}
		if in.WrongTool != "" {
			subs[i].WrongTool = in.WrongTool
		}
		in = subs[i]
		created = false
		break
	}
	if created {
		in.Occurrences = 1
		in.AddedAt = now
		in.UpdatedAt = now
		subs = append(subs, in)
	}

	if err := writeSubs(path, subs); err != nil {
		return Substitution{}, false, err
	}
	return in, created, nil
}

// writeSubs rewrites the whole ledger atomically: marshal every rule to a temp
// file in the same dir, fsync, then rename over the target. A rewrite (not an
// append) is required because a dedup bump mutates an existing row.
func writeSubs(path string, subs []Substitution) error {
	var buf []byte
	for i := range subs {
		b, err := json.Marshal(subs[i])
		if err != nil {
			return err
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}
	if err := durable.WriteTempRename(path, buf); err != nil {
		return err
	}
	// fsync the parent dir so the publish rename is crash-durable — os.Rename
	// alone only dirties the dir inode (mirrors internal/event's syncDir).
	return syncDir(filepath.Dir(path))
}
