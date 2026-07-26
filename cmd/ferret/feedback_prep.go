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

	"github.com/dkoosis/ferret/internal/feedback"
	"github.com/dkoosis/ferret/internal/retrievalevent"
)

// settledTailTurns is the number of Stop-hook firings a freshly-seen
// kind:search event must have existed through — COUNTING the firing that
// discovered it — before it is handed to judge (P-review P1 fix). A misled
// verdict is inferred from a repair tell adjacent to a LATER turn than the
// search's own (score.HelpedRecord.Correlational doc); at the search's own
// Stop hook that tell hasn't happened yet, so judging immediately would
// systematically miss every misled case.
//
// 2 is the minimum that is actually sufficient, not a conservative round
// number: CC appends a user's next prompt to the transcript before Claude
// starts responding to it, so by the time the Stop hook for turn N+1 fires,
// sessionUserTurns/repairAdjacency can already see and classify turn N+1's
// reply to the turn-N search. Waiting a 3rd firing buys no additional
// correctness — it only delays the ask by one extra turn, worsening the
// already-flagged TurnsBack staleness (ferret-162). Tunable default, not a
// configurable knob — no speculative config surface for one user (craft).
const settledTailTurns = 2

// pendingSearch is one kind:search event prep has SEEN (session matches,
// non-empty returned[]) but is holding until its behavioral tail has had a
// chance to land. WaitsSeen starts at 1 at discovery (the discovering Stop
// firing counts as the first), then ages by 1 on every later prep call while
// still pending — so WaitsSeen is always "how many Stop firings this event
// has existed through," and settledTailTurns compares directly against that
// count (see its doc comment for why 2 is the right number, not 3). Keyed by
// EventID in cursorState.Pending. NOT bounded by the ask budget — Reserve is
// only consulted in `check`, never here. prepScan drains at most one entry
// per call (oldestSettled, FIFO), so the map could in principle grow across a
// session that sustains more than one new search per turn faster than it
// settles+drains. Accepted as low practical harm for a single-user tool (no
// size cap or FIFO eviction added — a speculative defense against a load
// pattern this tap was never built for).
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
	if errors.Is(err, errNoRetrievalEvents) {
		// No retrieval-live-*.jsonl exists yet — a fresh project/session before
		// trixi has emitted its first event. That is the ordinary empty-feed
		// state, not a pipeline failure: report nothing pending and exit 0 so the
		// async Stop hook stays silent instead of asyncRewake-nagging Claude with
		// an exit-2 diagnostic every turn. A genuinely unreadable events dir still
		// surfaces its own error below.
		return json.NewEncoder(os.Stdout).Encode(prepResult{Pending: false})
	}
	if err != nil {
		return err
	}

	cPath := cursorPath(data, cmd.Session)

	// Serialize the whole load→scan→save cursor cycle against a concurrent prep
	// for the SAME session. feedback-stop.sh runs async (settings.json), so a
	// slow prep on turn N can still be mid-cycle when turn N+1's Stop hook fires
	// another; without this lock both could load the same offset, double-emit the
	// same search_event_id (two PAID relevance-judge runs), and clobber each
	// other's Pending/offset on save. This is the async-overlap the sibling bank
	// (internal/feedback/bank.go) also documents — prep's cursor, unlike the
	// bank, can't tolerate the race because a double-emit costs money, so it
	// takes the lock rather than accepting last-write-wins.
	unlock, err := feedback.Lock(cPath + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

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
// scratch state, not a log. The atomic rename guards a reader against a torn
// write; serialization against a concurrent prep for the same session (async
// Stop hooks CAN overlap) is the caller's job — cmdFeedbackPrep holds a
// per-session flock across the whole load→scan→save cycle.
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
		if rerr != nil && !errors.Is(rerr, io.EOF) {
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
		if errors.Is(rerr, io.EOF) {
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

// isParentSessionSearch reports whether e is a kind:search row this session's
// human actually issued — the only rows the tap may ask dk about. session_id is
// SHARED between a parent agent and its subagents (contract Trap 2), so matching
// on session_id alone admits every subagent's searches; a subagent retrieval
// graded and asked about as the human's own produces a garbage gold label. We
// therefore also require the parent agent_type and exclude session-fallback rows
// (agent_id degraded, can't be attributed — the same rows AdjudicateEvents drops
// from per-agent verdicts, so emitting one here would only cost a judge run that
// finds no matching record). Erring strict — a parent row with an unexpectedly
// empty agent_type is skipped — costs at most one missed ask, versus asking
// about a search the human never made.
func isParentSessionSearch(e *retrievalevent.Event, session string) bool {
	return e.Kind == retrievalevent.KindSearch &&
		e.SessionID == session &&
		e.AgentType == retrievalevent.AgentTypeParent &&
		e.Attribution != retrievalevent.AttributionSessionFallback
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
		if !isParentSessionSearch(e, session) || len(e.Returned) == 0 {
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
			WaitsSeen:       1, // this discovering Stop firing IS the first (see doc comment)
		}
	}

	// Age every entry that existed BEFORE this call's fresh finds above — a
	// freshly-discovered event already counted its own (discovering) firing
	// above, so it is not double-counted here; only entries that survived
	// from a PRIOR call age further.
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
