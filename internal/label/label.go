// Package label is ferret's in-session feedback ledger: a durable, append-only
// record of the t=0 human labels the feedback tap harvests — for each ask, the
// moment it rated, the question dk saw, and his one-char valence (or that he
// ignored it).
//
// Without it the feedback loop has no memory: ferret asks "was that retrieval
// worth it?", dk answers, and the label evaporates — the scorers it was meant to
// calibrate never see it. The ledger is the gold-label store the answer-side
// (ferret-j33) writes and the calibration readers join against. An *ignored* ask
// is recorded too: a non-answer is itself signal (the ask wasn't worth dk's
// keystroke), and dropping it would bias the kept labels toward the answerable.
//
// Write discipline mirrors internal/fixes: append-only JSONL, an flock so
// concurrent appends can't splice into one torn line, and an fsync (plus a
// first-create parent-dir fsync) so a recorded label survives a crash. The lock
// sentinel is label-specific (.labels.lock) so it does not over-serialize
// against the unrelated fixes ledger sharing the data dir.
package label

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// stderr is the diagnostic-log seam (mirrors internal/fixes and the events
// codec): the salvaged corrupt-trailing-line warning logs here so a test can
// capture it with a plain buffer instead of hijacking process-global os.Stderr.
var stderr io.Writer = os.Stderr

// syncDir fsyncs a directory so a first-create within it survives a crash. A new
// file's directory entry only mutates the parent dir; the dir inode itself must
// be fsync'd or the change can be lost in the dirty-inode window (mirrors
// internal/fixes' syncDir). Package var so a test can observe the publish path
// invokes it.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		return errors.Join(err, d.Close())
	}
	return d.Close()
}

// FileName is the ledger's basename under the ferret data dir.
const FileName = "labels.jsonl"

// ErrInvalidValence is returned by Append when a label carries a blank or
// unrecognized verdict — a writer bug, never a back-compat default. Callers can
// match it with errors.Is.
var ErrInvalidValence = errors.New("label: invalid valence")

// Valence is dk's verdict on an ask. Yes/No/Skip are the deterministic leading
// tokens the answer-side recognizes; Ignored is recorded when an ask went
// unanswered (dk typed work, not a verdict) — itself signal, so it is kept, not
// dropped.
const (
	ValenceYes     = "yes"
	ValenceNo      = "no"
	ValenceSkip    = "skip"
	ValenceIgnored = "ignored"
)

// ValidValence reports whether s is a recognized verdict. Unlike fixes'
// disposition, the empty string is NOT valid: every label carries an explicit
// valence (Ignored included), so a blank one is a writer bug, not a back-compat
// default.
func ValidValence(s string) bool {
	switch s {
	case ValenceYes, ValenceNo, ValenceSkip, ValenceIgnored:
		return true
	default:
		return false
	}
}

// Label is one recorded feedback answer.
//
// Session keys the label to the session the ask fired in — the same key the
// pending-ask state scopes by, so a label can never be mis-joined across
// sessions.
//
// Recorded is when the label was written (the answer turn's time), the join
// anchor the calibration readers align on.
//
// TargetRef is the moment the ask rated — the stable reference the ask-side
// banked and the answer-side carried through pending state. It is what makes the
// valence attributable: dk supplies the pulse, ferret supplies the target.
//
// Valence is dk's verdict (see the Valence constants). Text is the optional
// "say-more" — present only when dk elaborated past the one char.
type Label struct {
	Session   string    `json:"session"`
	Recorded  time.Time `json:"ts"`
	TargetRef string    `json:"target_ref"`
	Question  string    `json:"question"`
	Valence   string    `json:"valence"`
	Text      string    `json:"text,omitempty"`
}

// Path returns the ledger path for a ferret data dir (~/.ferret/labels.jsonl).
func Path(dataDir string) string { return filepath.Join(dataDir, FileName) }

// Load reads every label in append order. A missing ledger is an empty ledger,
// not an error, so a calibration reader can join unconditionally before any ask
// has been answered.
//
// A single corrupt TRAILING line is salvaged with a stderr warning — the
// signature of an append torn by a crash or short write. Every label ahead of it
// still loads, so one torn append does not brick all future reads. A corrupt
// line with valid labels AFTER it is genuine mid-ledger corruption and stays a
// hard error: silently treating it as "no labels" would erase harvested ground
// truth without warning. Mirrors internal/fixes.Load.
func Load(path string) ([]Label, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Label{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	lines, err := readLines(f)
	if err != nil {
		return nil, err
	}
	return parse(path, lines)
}

// readLines returns every non-blank line of the ledger. It uses an uncapped
// bufio.Reader rather than a Scanner: a "say-more" Text can push one JSON line
// past any fixed token cap, and Scanner would surface bufio.ErrTooLong before the
// trailing-salvage loop runs, hard-erroring the whole ledger and defeating the
// salvage guarantee. Mirrors internal/fixes.readLedgerLines.
func readLines(f *os.File) ([]string, error) {
	var lines []string
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			lines = append(lines, s)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return lines, nil // the final unterminated line was captured above
			}
			return nil, rerr
		}
	}
}

// parse unmarshals each line into a Label. A corrupt TRAILING line is salvaged
// with a stderr warning; a corrupt line with valid labels after it is genuine
// mid-ledger corruption and stays a hard error (see Load's contract).
func parse(path string, lines []string) ([]Label, error) {
	out := make([]Label, 0, len(lines))
	for i, line := range lines {
		var l Label
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			if i == len(lines)-1 {
				fmt.Fprintf(stderr, "label: %s: corrupt trailing ledger line dropped (1); the lost answer is gone, not recoverable\n", path)
				break
			}
			return nil, fmt.Errorf("label: corrupt ledger line in %s: %w", path, err)
		}
		out = append(out, l)
	}
	return out, nil
}

// Append adds one label to the ledger, creating the file and its parent dir on
// first use (a label can be recorded before any ingest has materialised the data
// dir). The ledger is append-only. The line is flushed and fsync'd before
// returning, and on first-create the parent dir is fsync'd too, so a recorded
// label is not lost to a crash. Mirrors internal/fixes.Append.
//
// A blank or unrecognized Valence is rejected before any write: every label must
// carry an explicit verdict (Ignored included), and a silent blank row would
// pollute the gold-label set the scorers calibrate against.
func Append(path string, l Label) error {
	if !ValidValence(l.Valence) {
		return fmt.Errorf("%w %q", ErrInvalidValence, l.Valence)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Serialize concurrent appends so two writes can't interleave into one torn
	// ledger line. lockLedger also creates the dir.
	release, lerr := lockLedger(path)
	if lerr != nil {
		return lerr
	}
	defer release()

	_, statErr := os.Stat(path)
	firstCreate := errors.Is(statErr, os.ErrNotExist)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	b, err := json.Marshal(l)
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
	if err := f.Close(); err != nil {
		return err
	}
	if firstCreate {
		// The append just created labels.jsonl: its directory entry is durable
		// only once the parent dir inode is fsync'd (see syncDir).
		return syncDir(filepath.Dir(path))
	}
	return nil
}
