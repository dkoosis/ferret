// Package spool is ferret's candidate spool: an append-only, monthly-rotated
// JSONL sink for candidate records (sensor→kg v1, epic gg-eqn). It is the
// producer end of the frozen contract in internal/candidate; the trixi-bot
// distiller tails these files.
//
// The write discipline mirrors internal/fixes.Append — flock-serialized,
// O_APPEND, fsync the row, fsync the parent dir on first create — so a candidate
// recorded before a crash is not lost and two concurrent emit runs never splice
// a torn row. Rotation is monthly (candidates-YYYY-MM.jsonl): a single file
// never grows unbounded, and because the name embeds YYYY-MM, lexical order is
// chronological order.
package spool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dkoosis/ferret/internal/candidate"
	"github.com/dkoosis/ferret/internal/durable"
	"github.com/dkoosis/ferret/internal/ledger"
)

// stderr is the diagnostic seam (mirrors internal/fixes): a salvaged
// corrupt-trailing-line warning logs here so a test can capture it with a plain
// buffer instead of hijacking os.Stderr.
var stderr io.Writer = os.Stderr

// syncDir is the dir-fsync seam so a test can observe (and the writer can prove)
// the first-create publish path invokes it — mirrors fixes.syncDir.
var syncDir = durable.SyncDir

// DirName is the spool subdir under the ferret data dir (~/.ferret/spool).
const DirName = "spool"

const (
	filePrefix  = "candidates-"
	fileSuffix  = ".jsonl"
	fileGlob    = filePrefix + "*" + fileSuffix
	monthLayout = "2006-01"
	lockName    = ".candidates.lock"
)

// Dir returns the spool dir for a ferret data dir (~/.ferret/spool).
func Dir(dataDir string) string { return filepath.Join(dataDir, DirName) }

// FileName returns the monthly candidate filename for time t (rotated by UTC
// month, so the boundary is stable regardless of the caller's local zone).
func FileName(t time.Time) string {
	return filePrefix + t.UTC().Format(monthLayout) + fileSuffix
}

// Path returns the monthly candidate file path within dir for time t.
func Path(dir string, t time.Time) string { return filepath.Join(dir, FileName(t)) }

// LoadIDs returns the set of candidate ids already present across every monthly
// spool file in dir. A missing dir is an empty set (no candidates yet). This is
// the idempotency backstop: emit skips any candidate whose id is already
// spooled, so re-running over an unchanged corpus writes no duplicate rows. A
// corrupt TRAILING line in a file is salvaged with a warning (the signature of a
// crash-torn append), mirroring fixes.Load; corruption before the last line is a
// hard error rather than silent id loss.
func LoadIDs(dir string) (map[string]struct{}, error) {
	ids := map[string]struct{}{}
	matches, err := filepath.Glob(filepath.Join(dir, fileGlob))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	for _, m := range matches {
		if err := collectIDs(m, ids); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// collectIDs adds every candidate id in one spool file to ids. It uses an
// uncapped bufio.Reader (a transcript_path can push a row past any fixed token
// cap) and salvages a single corrupt trailing line, matching fixes.Load's
// tolerance for a crash-torn final append.
func collectIDs(path string, ids map[string]struct{}) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	lines, err := ledger.ReadLines(f)
	if err != nil {
		return err
	}
	return parseIDs(path, lines, ids)
}

// parseIDs collects the id of each row into ids. A corrupt TRAILING line is
// salvaged with a warning (a crash-torn append); corruption before the last line
// is a hard error rather than silent id loss.
func parseIDs(path string, lines []string, ids map[string]struct{}) error {
	for i, line := range lines {
		var c candidate.Candidate
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			if i == len(lines)-1 {
				fmt.Fprintf(stderr, "spool: %s: corrupt trailing row dropped (1); re-emit to repair\n", path)
				return nil
			}
			return fmt.Errorf("spool: corrupt row in %s: %w", path, err)
		}
		if c.ID != "" {
			ids[c.ID] = struct{}{}
		}
	}
	return nil
}

// Writer appends candidates to the monthly spool, idempotently. It holds the set
// of ids already spooled (loaded on construction, plus any written this run) and
// skips a candidate whose id it has already seen, so re-emitting an unchanged
// span adds no duplicate row.
type Writer struct {
	dir  string
	seen map[string]struct{}
}

// NewWriter builds a writer for the spool dir, pre-loading the ids already
// present so Append can dedup across process restarts (cursor loss, a crash mid
// run). The dir need not exist yet — Append creates it on first write.
func NewWriter(dir string) (*Writer, error) {
	seen, err := LoadIDs(dir)
	if err != nil {
		return nil, err
	}
	return &Writer{dir: dir, seen: seen}, nil
}

// Append writes one candidate to its month's file and reports whether a row was
// actually written (false = an idempotent skip of an already-spooled id). The
// write is flock-serialized, O_APPEND, fsync'd, with a parent-dir fsync on first
// create — the crash-durability contract the distiller relies on. The month is
// read from the candidate's emitted_at, so a backfilled row lands in the correct
// historical file.
func (w *Writer) Append(c candidate.Candidate) (bool, error) {
	if _, ok := w.seen[c.ID]; ok {
		return false, nil
	}
	month, err := time.Parse(time.RFC3339Nano, c.EmittedAt)
	if err != nil {
		return false, fmt.Errorf("spool: bad emitted_at %q: %w", c.EmittedAt, err)
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return false, err
	}
	// Serialize concurrent appends so two emit runs can't interleave into one
	// torn row (fixes-isz pattern). durable.Lock is blocking + crash-safe.
	release, lerr := durable.Lock(filepath.Join(w.dir, lockName))
	if lerr != nil {
		return false, lerr
	}
	defer release()

	path := Path(w.dir, month)
	_, statErr := os.Stat(path)
	firstCreate := errors.Is(statErr, os.ErrNotExist)

	// Re-check the target file on disk UNDER the lock: the w.seen check above is
	// only the fast path — NewWriter snapshots ids once, so a concurrent emit
	// process may have appended this id since. Two same-instant runs target the
	// same month file (the month comes from emitted_at ≈ now), so re-scanning it
	// closes the cross-process duplicate window the in-memory set alone cannot.
	dup, err := w.dupOnDisk(path, firstCreate, c.ID)
	if err != nil {
		return false, err
	}
	if dup {
		w.seen[c.ID] = struct{}{}
		return false, nil
	}

	if err := ledger.AppendJSON(path, c); err != nil {
		return false, err
	}
	if firstCreate {
		// The append just created the month file: its directory entry is durable
		// only once the parent dir inode is fsync'd (durable.SyncDir).
		if err := syncDir(w.dir); err != nil {
			return false, err
		}
	}
	w.seen[c.ID] = struct{}{}
	return true, nil
}

// dupOnDisk reports whether id already sits in the target month file. It is the
// under-lock arm of the dedup decision; a not-yet-created file holds nothing.
func (w *Writer) dupOnDisk(path string, firstCreate bool, id string) (bool, error) {
	if firstCreate {
		return false, nil
	}
	onDisk := map[string]struct{}{}
	if err := collectIDs(path, onDisk); err != nil {
		return false, err
	}
	_, dup := onDisk[id]
	return dup, nil
}
