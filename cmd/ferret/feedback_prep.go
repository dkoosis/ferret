package main

// cmdFeedbackPrep is the pure, network-free half of the live join: it tails
// the live retrieval-event feed from a saved per-session cursor, finds the
// oldest kind:search row for this session whose returned[] is non-empty and
// whose behavioral tail has had a chance to land (settled-tail eligibility,
// below), and hands it off to `feedback judge`. See the plan's Design section
// ("Cursor/offset state for the retrieval-live JSONL tail" and "Settled-tail
// eligibility") for the full rationale.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dkoosis/ferret/internal/retrievalevent"
)

// settledTailTurns is the number of subsequent prep calls a freshly-seen
// kind:search event must sit in the pending map before it is handed to judge
// (P-review P1 fix). A misled verdict is inferred from a repair tell adjacent
// to a LATER turn than the search's own (score.HelpedRecord.Correlational
// doc); at the search's own Stop hook that tell hasn't happened yet, so
// judging immediately would systematically miss every misled case. 2 (the
// search's own Stop, plus at least one full subsequent user turn's Stop) is a
// tunable default, not a configurable knob — no speculative config surface
// for one user (craft).
const settledTailTurns = 2

// pendingSearch is one kind:search event prep has SEEN (session matches,
// non-empty returned[]) but is holding until its behavioral tail has had a
// chance to land. Keyed by EventID in cursorState.Pending. Bounded by the ask
// budget (at most a handful can ever accumulate before Reserve's caps stop
// mattering).
type pendingSearch struct {
	NugIDs          []string `json:"nug_ids"`
	FirstSeenOffset int64    `json:"first_seen_offset"`
	WaitsSeen       int      `json:"waits_seen"`
}

// cursorState is feedback prep's per-session tail-scan bookkeeping: which
// retrieval-live file + byte offset it has scanned to, plus the settled-tail
// pending map. Genuinely transient scratch — mirrors internal/feedback/
// budget.go's own framing ("the budget is transient rate-limit scratch, not
// durable data"): a corrupt/missing file self-heals to the zero state, never a
// hard error.
type cursorState struct {
	File    string                    `json:"file"`
	Offset  int64                     `json:"offset"`
	Pending map[string]*pendingSearch `json:"pending,omitempty"`
}

// prepResult is feedback prep's JSON stdout contract — the hand-off to the
// bash Stop-hook script, which loops `trixi get` over NugIDs before calling
// `feedback judge`.
type prepResult struct {
	Pending       bool     `json:"pending"`
	SearchEventID string   `json:"search_event_id,omitempty"`
	NugIDs        []string `json:"nug_ids,omitempty"`
}

// scannedEvent pairs a decoded retrieval event with the byte offset its line
// started at — the offset pendingSearch.FirstSeenOffset needs for FIFO
// ordering, and the boundary saveCursor persists past.
type scannedEvent struct {
	event  retrievalevent.Event
	offset int64
}

// cmdFeedbackPrep wires the kong CLI flags to the cursor load/scan/save cycle
// and prints the JSON hand-off (or {"pending":false}) to stdout.
func cmdFeedbackPrep() error {
	cmd := &CLI.Feedback.Prep
	if strings.TrimSpace(cmd.Session) == "" {
		return errFeedbackSessionRequired
	}
	data, err := resolveData(cmd.Data)
	if err != nil {
		return err
	}
	eventsFile, err := resolveFeedbackEventsPath(cmd.Events)
	if err != nil {
		return err
	}

	cPath := cursorPath(data, cmd.Session)
	cur := loadCursor(cPath)
	base := filepath.Base(eventsFile)
	if cur.File != base {
		// Month rollover (or first-ever run): restart the tail from 0, but keep
		// any already-pending entries — they are keyed by event_id, not file
		// position, and stay valid across the rollover (Design, Open Question 2:
		// accepted as an out-of-scope edge for a single-user tool).
		cur = cursorState{File: base, Offset: 0, Pending: cur.Pending}
	}

	found, newOffset, err := scanNewLines(eventsFile, cur.Offset)
	if err != nil {
		return err
	}

	res, next := prepScan(cmd.Session, cur, found, settledTailTurns)
	next.File = base
	next.Offset = newOffset

	if err := saveCursor(cPath, next); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(res)
}

// loadCursor reads the cursor file at path. A missing or corrupt file reads as
// the zero state (offset 0, no pending) — the first prep call of the tap's
// life, or a self-heal after a bad write; never a hard error (mirrors
// internal/feedback/budget.go's loadState).
func loadCursor(path string) cursorState {
	b, err := os.ReadFile(path)
	if err != nil {
		return cursorState{}
	}
	var st cursorState
	if err := json.Unmarshal(b, &st); err != nil {
		fmt.Fprintf(os.Stderr, "feedback: %s: corrupt cursor file reset to offset 0 (%v)\n", path, err)
		return cursorState{}
	}
	return st
}

// saveCursor publishes the cursor atomically (temp+fsync+rename+dir-fsync,
// mirrors internal/feedback/budget.go's writeState) — read-modify-write
// scratch state, not a log. No flock: `prep` is the only writer, and one
// session never runs two Stop hooks concurrently.
func saveCursor(path string, st cursorState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, werr := tmp.Write(b); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return werr
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// scanNewLines reads path from byte offset to EOF, decoding each complete
// line as a retrievalevent.Event (tolerantly — see below) and pairing it with
// its own starting byte offset. A line with no trailing newline yet (the
// producer is mid-append) is left UNCONSUMED: neither decoded nor counted
// into newOffset, so the next call re-reads the completed line rather than
// risking a torn decode.
//
// Decoding is deliberately tolerant, unlike retrievalevent.ReadEvents' fatal
// contract (Trap 1: a schema_version mismatch aborts the batch reader). A live
// tail cannot afford that: one malformed or future-schema row must not wedge
// prep forever. A bad line is skipped with a stderr note and IS consumed (its
// bytes count into newOffset) so the tail keeps moving.
func scanNewLines(path string, offset int64) (found []scannedEvent, newOffset int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	r := bufio.NewReader(f)
	pos := offset
	for {
		line, rerr := r.ReadBytes('\n')
		if rerr != nil && rerr != io.EOF {
			return found, pos, rerr
		}
		if isTornLine(line, rerr) {
			break // producer hasn't finished this append yet
		}
		lineStart := pos
		pos += int64(len(line))
		if se, ok := decodeScanLine(path, line, lineStart); ok {
			found = append(found, se)
		}
		if rerr == io.EOF {
			break
		}
	}
	return found, pos, nil
}

// isTornLine reports whether a ReadBytes('\n') result is an incomplete final
// read: EOF reached with bytes present but no trailing newline — the producer
// is still mid-append. scanNewLines leaves it unconsumed rather than risking
// a torn decode.
func isTornLine(line []byte, rerr error) bool {
	return errors.Is(rerr, io.EOF) && len(line) > 0 && line[len(line)-1] != '\n'
}

// decodeScanLine trims and decodes one raw line into a scannedEvent. ok=false
// for a blank line, or a decode/schema-version failure (logged to stderr) —
// both cases are simply omitted from scanNewLines' result, never fatal (see
// its doc comment: a live tail must not wedge on one bad row).
func decodeScanLine(path string, line []byte, lineStart int64) (scannedEvent, bool) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return scannedEvent{}, false
	}
	var e retrievalevent.Event
	if err := json.Unmarshal(trimmed, &e); err != nil || e.SchemaVersion != retrievalevent.SchemaVersion {
		fmt.Fprintf(os.Stderr, "feedback: %s: skipping unparseable/mismatched-schema line at offset %d\n", path, lineStart)
		return scannedEvent{}, false
	}
	return scannedEvent{event: e, offset: lineStart}, true
}

// prepScan is feedback prep's pure core: given loaded cursor state and the
// newly-scanned events since its offset, filters to this session's non-empty
// kind:search rows, folds fresh ones into the pending map, ages every
// already-pending entry by one wait, and emits the oldest settled one (if
// any) — FIFO, one per call (Open Question 5's accepted default: leave the
// rest for later fires). Pure and side-effect-free so the settled-tail state
// machine is unit-testable without touching a file.
func prepScan(session string, cur cursorState, found []scannedEvent, waitThreshold int) (prepResult, cursorState) {
	next := cursorState{File: cur.File, Pending: copyPending(cur.Pending)}

	for i := range found {
		se := &found[i]
		e := &se.event
		if e.Kind != retrievalevent.KindSearch || e.SessionID != session || len(e.Returned) == 0 {
			continue
		}
		if _, exists := next.Pending[e.EventID]; exists {
			continue // already tracked (shouldn't recur in a well-formed feed; defensive)
		}
		if next.Pending == nil {
			next.Pending = map[string]*pendingSearch{}
		}
		ids := make([]string, len(e.Returned))
		for j, r := range e.Returned {
			ids[j] = r.NugID
		}
		next.Pending[e.EventID] = &pendingSearch{
			NugIDs:          ids,
			FirstSeenOffset: se.offset,
			WaitsSeen:       0,
		}
	}

	// Age every entry that existed BEFORE this call's fresh finds above — a
	// freshly-discovered event's first wait starts on the NEXT call, not this
	// one (it has not yet survived a subsequent Stop fire).
	for id := range cur.Pending {
		if p, ok := next.Pending[id]; ok {
			p.WaitsSeen++
		}
	}

	emitID, emit := oldestSettled(next.Pending, waitThreshold)
	if emit == nil {
		return prepResult{Pending: false}, next
	}
	delete(next.Pending, emitID)
	return prepResult{Pending: true, SearchEventID: emitID, NugIDs: emit.NugIDs}, next
}

// oldestSettled returns the pending entry with the smallest FirstSeenOffset
// among those whose WaitsSeen has reached waitThreshold, or ("", nil) when
// none has settled yet.
func oldestSettled(pending map[string]*pendingSearch, waitThreshold int) (string, *pendingSearch) {
	var emitID string
	var emit *pendingSearch
	for id, p := range pending {
		if p.WaitsSeen < waitThreshold {
			continue
		}
		if emit == nil || p.FirstSeenOffset < emit.FirstSeenOffset {
			emitID, emit = id, p
		}
	}
	return emitID, emit
}

// copyPending deep-copies the pending map so prepScan's returned cursorState
// never aliases the caller's input state (a caller may hold cur across the
// call and compare before/after).
func copyPending(m map[string]*pendingSearch) map[string]*pendingSearch {
	if len(m) == 0 {
		return nil
	}
	cp := make(map[string]*pendingSearch, len(m))
	for k, v := range m {
		cv := *v
		cp[k] = &cv
	}
	return cp
}
