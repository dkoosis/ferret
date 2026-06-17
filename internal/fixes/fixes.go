// Package fixes is the ferret fix ledger: a durable, append-only record of
// which motif was fixed by which artifact, when, and what it cost at the time.
//
// Without it ferret scans, proposes fixes, then forgets — next month's scan
// re-surfaces the same friction as if nothing changed, and you can't tell a fix
// that worked from one that didn't. The ledger lets `ferret report
// --since-fixes` compute a burn-delta across ingests: it joins each finding to
// its recorded fix by the motif key (the stable sort key ferret already ranks
// by), then reports baseline→current burn. Computed, not eyeballed.
package fixes

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileName is the ledger's basename under the ferret data dir.
const FileName = "fixes.jsonl"

// Disposition is the adjudication verdict an entry records. Adjudication does
// not always yield a fix: a motif can be verified unfixable (DispositionWontfix,
// e.g. a PreToolUse hook can't reclaim an already-spent failed payload) or
// parked until another fix lands and the ranking can be re-judged
// (DispositionWatch). Only DispositionFix captures a burn baseline and computes
// a baseline→current delta; the non-fix verdicts suppress the motif from the
// actionable report (with their reason shown) so an adjudicated-but-unfixed
// motif stops re-surfacing as a fresh candidate every scan.
const (
	DispositionFix     = "fix"
	DispositionWontfix = "wontfix"
	DispositionWatch   = "watch"
)

// ValidDisposition reports whether s is a recognized verdict. The empty string
// is valid because it normalizes to DispositionFix (see Entry.Disp) — the
// writer may leave it unset and old ledger rows omit it entirely.
func ValidDisposition(s string) bool {
	switch s {
	case "", DispositionFix, DispositionWontfix, DispositionWatch:
		return true
	default:
		return false
	}
}

// Entry is one recorded fix.
//
// Motif is the comma-joined motif token sequence — the same sequence ferret
// report sorts by, and the STABLE join key across ingests: a fix recorded today
// still matches its finding next month even as counts and burn move.
//
// BaselineBurn is the motif's measured burn (in tokens) at the moment the fix
// was recorded — the "before" of the before→after delta. Capturing it at write
// time is what makes the later delta computed rather than eyeballed.
//
// Disposition is the adjudication verdict (see the Disposition constants). It is
// omitempty and a missing/empty value reads as DispositionFix via Disp(), so
// rows written before the field existed keep their original baseline→delta
// behavior. Only a fix records a meaningful BaselineBurn; wontfix/watch leave it
// at zero.
type Entry struct {
	Motif        string    `json:"motif"`
	Fix          string    `json:"fix"`
	Note         string    `json:"note,omitempty"`
	AddedAt      time.Time `json:"addedAt"`
	BaselineBurn int       `json:"baselineBurn"`
	Disposition  string    `json:"disposition,omitempty"`
}

// Disp returns the entry's verdict, defaulting a missing/empty Disposition to
// DispositionFix. This is the single back-compat point: every reader asks Disp,
// never the raw field, so old ledger rows (which omit the field) and new fix rows
// behave identically.
func (e Entry) Disp() string {
	if e.Disposition == "" {
		return DispositionFix
	}
	return e.Disposition
}

// Suppressed reports whether the entry's verdict removes its motif from the
// actionable report (wontfix/watch) instead of computing a burn-delta (fix).
func (e Entry) Suppressed() bool {
	switch e.Disp() {
	case DispositionWontfix, DispositionWatch:
		return true
	default:
		return false
	}
}

// Path returns the ledger path for a ferret data dir (~/.ferret/fixes.jsonl).
func Path(dataDir string) string { return filepath.Join(dataDir, FileName) }

// MotifKey is the canonical join key for a motif's token sequence. It is the
// single place the motif→string mapping is defined, so the writer (fixes add)
// and the reader (report --since-fixes) cannot drift.
func MotifKey(tokens []string) string { return strings.Join(tokens, ",") }

// Load reads every ledger entry in append order. A missing ledger is an empty
// ledger, not an error, so callers can join unconditionally before any fix has
// been recorded. A malformed line IS an error: silently treating a corrupt
// ledger as "no fixes" would erase the loop's memory without warning.
func Load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("fixes: corrupt ledger line in %s: %w", path, err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Append adds one entry to the ledger, creating the file and its parent dir on
// first use (a fix can be recorded before any ingest has materialised the data
// dir). The ledger is append-only: a motif fixed, regressed, and re-fixed keeps
// all three rows, so the full history survives. The line is flushed and fsync'd
// before returning so a recorded fix is not lost to a crash.
func Append(path string, e Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return errors.Join(err, f.Close())
	}
	b = append(b, '\n')
	if _, err := f.Write(b); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}

// Index maps each motif key to its most recently recorded fix. The ledger is
// append-ordered, so the last entry for a motif wins — a re-fix after a
// regression supersedes the original. This is the join table report consumes.
func Index(entries []Entry) map[string]Entry {
	idx := make(map[string]Entry, len(entries))
	for _, e := range entries {
		idx[e.Motif] = e // later (more recent) entries overwrite earlier ones
	}
	return idx
}

// Annotation pairs a motif's recorded fix with its current burn this ingest —
// the joined view report renders as "fixed <date>, burn <baseline>→<current>".
type Annotation struct {
	Entry   Entry
	Current int // current burn (tokens) for the motif this ingest
}

// Arrow reports the delta direction: ↓ when burn fell (the fix landed), ↑ when
// it rose (regression), = when unchanged.
func (a Annotation) Arrow() string {
	switch {
	case a.Current < a.Entry.BaselineBurn:
		return "↓"
	case a.Current > a.Entry.BaselineBurn:
		return "↑"
	default:
		return "="
	}
}
