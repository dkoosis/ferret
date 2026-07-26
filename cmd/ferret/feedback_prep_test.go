package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dkoosis/ferret/internal/retrievalevent"
)

// writeJSONL appends each event as one JSON line to path, creating it if
// needed. Test-only fixture helper.
func writeJSONL(t *testing.T, path string, events ...retrievalevent.Event) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i := range events {
		b, err := json.Marshal(&events[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

func searchEvent(id, session string, nugIDs ...string) retrievalevent.Event {
	returned := make([]retrievalevent.Returned, len(nugIDs))
	for i, id := range nugIDs {
		returned[i] = retrievalevent.Returned{NugID: id, Score: 0.9}
	}
	return retrievalevent.Event{
		SchemaVersion: retrievalevent.SchemaVersion,
		Kind:          retrievalevent.KindSearch,
		EventID:       id,
		TS:            "2026-07-25T15:00:00Z",
		SessionID:     session,
		AgentType:     retrievalevent.AgentTypeParent, // the human's own agent by default
		Returned:      returned,
	}
}

// TestPrepScanExcludesSubagentAndSessionFallback: session_id is SHARED between a
// parent and its subagents, so a subagent's search (agent_type != parent) and a
// session-fallback row (agent_id degraded, unattributable) must never enter the
// pending map — grading one as the human's own retrieval and asking about it
// would record a garbage gold label. Regression test for ferret-5g7.
func TestPrepScanExcludesSubagentAndSessionFallback(t *testing.T) {
	subagent := searchEvent("evt-subagent", "s1", "n1")
	subagent.AgentType = "general-purpose"

	fallback := searchEvent("evt-fallback", "s1", "n2")
	fallback.Attribution = retrievalevent.AttributionSessionFallback

	parent := searchEvent("evt-parent", "s1", "n3")

	found := []scannedEvent{
		{event: subagent, offset: 0},
		{event: fallback, offset: 100},
		{event: parent, offset: 200},
	}
	_, next := prepScan("s1", cursorState{}, found, settledTailTurns)
	if _, tracked := next.Pending["evt-subagent"]; tracked {
		t.Error("a subagent search (agent_type != parent) must be excluded")
	}
	if _, tracked := next.Pending["evt-fallback"]; tracked {
		t.Error("a session-fallback row must be excluded")
	}
	if _, tracked := next.Pending["evt-parent"]; !tracked {
		t.Errorf("the parent's own search must be tracked, got %+v", next.Pending)
	}
}

// TestScanNewLinesFiltersAndAdvancesOffset: scanNewLines is a dumb decode+scan
// — it does not know about sessions or eligibility, only "decode every
// complete line, report its offset". Filtering is prepScan's job (tested
// separately below); this pins scanNewLines' own contract: every well-formed
// line decodes, and the returned offset covers exactly what was read.
func TestScanNewLinesFiltersAndAdvancesOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	writeJSONL(t, path,
		searchEvent("evt-a", "other-session", "n1"),
		searchEvent("evt-b", "s1", "n2", "n3"),
	)

	found, offset, err := scanNewLines(path, 0)
	if err != nil {
		t.Fatalf("scanNewLines: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("scanNewLines found %d events, want 2", len(found))
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if offset != fi.Size() {
		t.Errorf("offset = %d, want EOF (%d)", offset, fi.Size())
	}

	// A second call from the new offset finds nothing new.
	found2, offset2, err := scanNewLines(path, offset)
	if err != nil {
		t.Fatalf("scanNewLines (2nd call): %v", err)
	}
	if len(found2) != 0 {
		t.Errorf("2nd scanNewLines found %d events, want 0 (nothing new since offset)", len(found2))
	}
	if offset2 != offset {
		t.Errorf("2nd scanNewLines offset = %d, want unchanged %d", offset2, offset)
	}
}

// TestScanNewLinesLeavesTornLineUnconsumed: a line with no trailing newline
// (the producer mid-append) must not be decoded or counted into the offset —
// the next call re-reads it once it's complete, rather than risking a torn
// decode of a half-written row.
func TestScanNewLinesLeavesTornLineUnconsumed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	b, err := json.Marshal(searchEvent("evt-a", "s1", "n1"))
	if err != nil {
		t.Fatal(err)
	}
	// One complete line, then a torn (no trailing \n) partial line.
	content := append(append([]byte{}, b...), '\n')
	content = append(content, []byte(`{"schema_version":1,"kind":"search","event_id":"evt-b"`)...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	found, offset, err := scanNewLines(path, 0)
	if err != nil {
		t.Fatalf("scanNewLines: %v", err)
	}
	if len(found) != 1 || found[0].event.EventID != "evt-a" {
		t.Fatalf("scanNewLines found %+v, want exactly evt-a", found)
	}
	if int(offset) != len(content)-len(`{"schema_version":1,"kind":"search","event_id":"evt-b"`) {
		t.Errorf("offset %d must stop before the torn line", offset)
	}
}

// TestScanNewLinesSkipsUnparseableLine: a malformed or schema-mismatched line
// is skipped (not fatal — a live tail must not wedge on one bad row) and IS
// consumed, so the tail keeps moving past permanently-bad data.
func TestScanNewLinesSkipsUnparseableLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	good := searchEvent("evt-good", "s1", "n1")
	goodB, err := json.Marshal(good)
	if err != nil {
		t.Fatal(err)
	}
	content := "not json at all\n" + string(goodB) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	found, offset, err := scanNewLines(path, 0)
	if err != nil {
		t.Fatalf("scanNewLines: %v", err)
	}
	if len(found) != 1 || found[0].event.EventID != "evt-good" {
		t.Fatalf("scanNewLines found %+v, want exactly evt-good", found)
	}
	if int(offset) != len(content) {
		t.Errorf("offset %d must consume the bad line too, want %d", offset, len(content))
	}
}

// TestLoadCursorMissingOrCorruptSelfHeals: mirrors budget.go's loadState
// self-heal contract — a missing or corrupt cursor file reads as the zero
// state (offset 0, no pending), never a hard error.
func TestLoadCursorMissingOrCorruptSelfHeals(t *testing.T) {
	dir := t.TempDir()
	missing := loadCursor(filepath.Join(dir, "nope.json"))
	if missing.Offset != 0 || len(missing.Pending) != 0 {
		t.Errorf("missing cursor = %+v, want zero state", missing)
	}

	corruptPath := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	corrupt := loadCursor(corruptPath)
	if corrupt.Offset != 0 || len(corrupt.Pending) != 0 {
		t.Errorf("corrupt cursor = %+v, want zero state", corrupt)
	}
}

// TestSaveLoadCursorRoundtrip: the cursor persists offset + pending map
// exactly, including the settled-tail bookkeeping fields.
func TestSaveLoadCursorRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor.json")
	want := cursorState{
		File:   "retrieval-live-2026-07.jsonl",
		Offset: 1234,
		Pending: map[string]*pendingSearch{
			"evt-a": {NugIDs: []string{"n1", "n2"}, FirstSeenOffset: 10, WaitsSeen: 1},
		},
	}
	if err := saveCursor(path, want); err != nil {
		t.Fatalf("saveCursor: %v", err)
	}
	got := loadCursor(path)
	if got.File != want.File || got.Offset != want.Offset {
		t.Errorf("loadCursor = %+v, want %+v", got, want)
	}
	if p := got.Pending["evt-a"]; p == nil || p.WaitsSeen != 1 || p.FirstSeenOffset != 10 {
		t.Errorf("pending entry not preserved: %+v", got.Pending)
	}
}

// TestPrepScanFiltersOtherSessionAndEmptyReturned: a row from another session,
// and a row with an empty returned[], must never enter the pending map — they
// are permanently ignorable, not merely delayed.
func TestPrepScanFiltersOtherSessionAndEmptyReturned(t *testing.T) {
	found := []scannedEvent{
		{event: searchEvent("evt-other-session", "other", "n1"), offset: 0},
		{event: searchEvent("evt-empty", "s1"), offset: 100}, // no returned[]
	}
	res, next := prepScan("s1", cursorState{}, found, settledTailTurns)
	if res.Pending {
		t.Errorf("no eligible row present, want pending=false, got %+v", res)
	}
	if len(next.Pending) != 0 {
		t.Errorf("filtered rows must not enter the pending map, got %+v", next.Pending)
	}
}

// TestPrepScanSettledTailHoldsFreshEventThenEmits: a freshly-seen event is
// held (pending:false) on its discovering call, then emitted on the very next
// call once waits_seen reaches the threshold — the P-review P1 fix, pinned to
// EXACTLY 2 total Stop firings (not 3): CC appends the human's next prompt to
// the transcript before Claude starts responding to it, so by the second
// Stop firing repairAdjacency can already see and classify that reply. A 3rd
// firing would buy no correctness and only stale the ask further (see
// settledTailTurns' doc comment).
func TestPrepScanSettledTailHoldsFreshEventThenEmits(t *testing.T) {
	const threshold = 2
	ev := searchEvent("evt-a", "s1", "n1", "n2")

	// Call 1 (the search's own Stop): fresh discovery. Held — only 1 firing so far.
	res1, cur1 := prepScan("s1", cursorState{}, []scannedEvent{{event: ev, offset: 0}}, threshold)
	if res1.Pending {
		t.Fatalf("call 1 (fresh discovery) must be held, got %+v", res1)
	}
	if p := cur1.Pending["evt-a"]; p == nil || p.WaitsSeen != 1 {
		t.Fatalf("call 1: evt-a must enter pending with waits_seen=1 (this firing counts as the first), got %+v", cur1.Pending)
	}

	// Call 2 (the next full user turn's Stop): 2 firings now — must emit.
	final, after := prepScan("s1", cur1, nil, threshold)
	if !final.Pending || final.SearchEventID != "evt-a" {
		t.Fatalf("call 2 must emit evt-a once settled, got %+v", final)
	}
	if len(final.NugIDs) != 2 {
		t.Errorf("emitted nug_ids = %v, want the 2 originally returned", final.NugIDs)
	}
	if _, still := after.Pending["evt-a"]; still {
		t.Error("an emitted event must be removed from the pending map")
	}
}

// TestPrepScanFIFOOrdersMultiplePending: two events discovered together and
// settled together are tracked simultaneously, and emitted oldest-first
// (FIFO, one per call) — the accepted default for multiple eligible events in
// one scan window.
func TestPrepScanFIFOOrdersMultiplePending(t *testing.T) {
	const threshold = 2
	found := []scannedEvent{
		{event: searchEvent("evt-first", "s1", "n1"), offset: 0},
		{event: searchEvent("evt-second", "s1", "n2"), offset: 200},
	}
	res0, cur := prepScan("s1", cursorState{}, found, threshold)
	if res0.Pending {
		t.Fatalf("both events are freshly discovered (waits_seen=1 < threshold=2) — must be held, got %+v", res0)
	}
	if len(cur.Pending) != 2 {
		t.Fatalf("both events must be tracked simultaneously, got %+v", cur.Pending)
	}

	// Age both past threshold — FIFO, one emission per call.
	res1, cur2 := prepScan("s1", cur, nil, threshold)
	if !res1.Pending || res1.SearchEventID != "evt-first" {
		t.Fatalf("oldest (evt-first) must emit first, got %+v", res1)
	}
	res2, cur3 := prepScan("s1", cur2, nil, threshold)
	if !res2.Pending || res2.SearchEventID != "evt-second" {
		t.Fatalf("evt-second must emit next, got %+v", res2)
	}
	if len(cur3.Pending) != 0 {
		t.Errorf("pending map must be empty after both emit, got %+v", cur3.Pending)
	}
}

// TestPrepScanIgnoresAlreadyPendingDuplicate: a search event id already
// tracked in the pending map must not be re-added (and must not reset its
// waits_seen) if it somehow reappears in a scan window.
func TestPrepScanIgnoresAlreadyPendingDuplicate(t *testing.T) {
	cur := cursorState{Pending: map[string]*pendingSearch{
		"evt-a": {NugIDs: []string{"n1"}, FirstSeenOffset: 0, WaitsSeen: 1},
	}}
	found := []scannedEvent{{event: searchEvent("evt-a", "s1", "n1"), offset: 0}}
	_, next := prepScan("s1", cur, found, 5)
	if p := next.Pending["evt-a"]; p == nil || p.WaitsSeen != 2 {
		t.Errorf("a re-scanned already-pending id must just age normally (2), not reset: %+v", next.Pending["evt-a"])
	}
}
