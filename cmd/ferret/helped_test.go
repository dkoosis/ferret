package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/retrievalevent"
)

// writeUserTurnsTranscript writes a minimal session jsonl carrying one user
// turn at answerTS with the given content, for sessionUserTurns to walk.
func writeUserTurnsTranscript(t *testing.T, answerTS, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	line := `{"type":"user","timestamp":"` + answerTS + `","sessionId":"s","message":{"role":"user","content":"` + content + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm
}

// TestRepairAdjacency: a search is repair-adjacent iff the FIRST user turn after
// one of its reads is a repair move. A repair before the read, or a non-repair
// turn immediately after (with the repair only later), must NOT flag it — that
// is the over-flag guard the misled verdict rides on (bbp.17).
func TestRepairAdjacency(t *testing.T) {
	search := func(id, at string) retrievalevent.Event {
		return retrievalevent.Event{SchemaVersion: 1, Kind: retrievalevent.KindSearch, EventID: id, TS: at}
	}
	read := func(id, at, ref string) retrievalevent.Event {
		return retrievalevent.Event{SchemaVersion: 1, Kind: retrievalevent.KindRead, EventID: id, TS: at,
			SearchRef: &retrievalevent.SearchRef{EventID: ref}}
	}

	turns := []userTurn{
		{ts: ts(t, "2026-06-29T20:50:00Z"), move: dialogue.MoveRepair},  // BEFORE any read
		{ts: ts(t, "2026-06-29T20:53:00Z"), move: dialogue.MoveRepair},  // right after readA
		{ts: ts(t, "2026-06-29T20:57:00Z"), move: dialogue.MoveNeutral}, // right after readB
		{ts: ts(t, "2026-06-29T20:58:00Z"), move: dialogue.MoveRepair},  // later in B's segment, not adjacent
	}
	events := []retrievalevent.Event{
		search("A", "2026-06-29T20:52:00Z"), read("ra", "2026-06-29T20:52:30Z", "A"),
		search("B", "2026-06-29T20:56:00Z"), read("rb", "2026-06-29T20:56:30Z", "B"),
		search("C", "2026-06-29T21:00:00Z"), // no read at all → never adjacent
	}

	adj := repairAdjacency(events, turns)
	if !adj["A"] {
		t.Error("A: repair immediately after its read → want adjacent")
	}
	if adj["B"] {
		t.Error("B: first turn after its read is neutral (repair only later) → want NOT adjacent")
	}
	if adj["C"] {
		t.Error("C: no read → want NOT adjacent")
	}
}

// TestSessionUserTurnsExcludesProbeAnswer is the ferret-j33 §6/P0-2
// repair-adjacency contamination test: a granted ask answered
// "no — it clearly wasn't relevant" tags itself MoveRepair under plain
// sessionUserTurns (no exclude), and repairAdjacency then wrongly marks the
// PRECEDING search repair-adjacent — a false misled signal manufactured by
// the probe answer's own raw text, not by anything the human said about that
// search. With the label-derived exclude map, the turn is skipped entirely
// and the search is no longer marked repair-adjacent.
func TestSessionUserTurnsExcludesProbeAnswer(t *testing.T) {
	const answerTS = "2026-07-26T12:00:03Z"
	path := writeUserTurnsTranscript(t, answerTS, "no — it clearly wasn't relevant")

	events := []retrievalevent.Event{
		{SchemaVersion: 1, Kind: retrievalevent.KindSearch, EventID: "A", TS: "2026-07-26T12:00:00Z"},
		{SchemaVersion: 1, Kind: retrievalevent.KindRead, EventID: "ra", TS: "2026-07-26T12:00:01Z",
			SearchRef: &retrievalevent.SearchRef{EventID: "A"}},
	}

	baseline, err := sessionUserTurns(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline) != 1 || baseline[0].move != dialogue.MoveRepair {
		t.Fatalf("contaminated baseline: want one MoveRepair turn, got %+v", baseline)
	}
	if adj := repairAdjacency(events, baseline); !adj["A"] {
		t.Error("contaminated baseline: want search A wrongly flagged repair-adjacent")
	}

	excluded, err := sessionUserTurns(path, map[string]bool{answerTS: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded) != 0 {
		t.Fatalf("excluded probe answer must be skipped outright, got %+v", excluded)
	}
	if adj := repairAdjacency(events, excluded); adj["A"] {
		t.Error("with the probe turn excluded, search A must NOT be repair-adjacent")
	}
}

// TestFirstTurnAfter pins the boundary: strictly-after, earliest wins, empty when
// nothing follows.
func TestFirstTurnAfter(t *testing.T) {
	turns := []userTurn{
		{ts: ts(t, "2026-06-29T20:00:00Z"), move: dialogue.MoveAccept},
		{ts: ts(t, "2026-06-29T20:05:00Z"), move: dialogue.MoveRepair},
	}
	if m, ok := firstTurnAfter(turns, ts(t, "2026-06-29T20:02:00Z")); !ok || m != dialogue.MoveRepair {
		t.Errorf("got %v/%v, want repair/true", m, ok)
	}
	if _, ok := firstTurnAfter(turns, ts(t, "2026-06-29T21:00:00Z")); ok {
		t.Error("nothing after → want ok=false")
	}
}
