package score

import (
	"strconv"
	"testing"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/retrievalevent"
)

// segAt is a test helper: a segment owning [first,last] wall-clock.
func segAt(index int, first, last string) Segment {
	return Segment{Index: index, FirstCall: 0, LastCall: 0, FirstTS: first, LastTS: last}
}

func TestOwningSegment(t *testing.T) {
	segs := []Segment{
		segAt(1, "2026-06-29T20:52:00.000000000Z", "2026-06-29T20:54:00.000000000Z"),
		segAt(2, "2026-06-29T20:55:00.000000000Z", "2026-06-29T21:05:00.000000000Z"),
		{Index: 3, FirstCall: -1, LastCall: -1}, // owns no calls → empty interval
	}
	cases := []struct {
		name string
		ts   string
		want int
	}{
		{"inside first", "2026-06-29T20:52:30.000000000Z", 0},
		{"on first boundary", "2026-06-29T20:52:00.000000000Z", 0},
		{"inside second", "2026-06-29T21:03:14.271000000Z", 1},
		{"in the gap between segments", "2026-06-29T20:54:30.000000000Z", -1},
		{"after every segment", "2026-06-29T22:00:00.000000000Z", -1},
		{"unparseable ts", "not-a-timestamp", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := OwningSegment(segs, c.ts); got != c.want {
				t.Errorf("OwningSegment(%q) = %d, want %d", c.ts, got, c.want)
			}
		})
	}
}

// TestJoinLegs: a search event lands in the segment whose interval contains its
// ts, picking up that segment's SegmentID, Tell and SegOutcome; an event in a
// gap gets no leg (AdjudicateEvents then reads its zero Leg). Reads are not
// searches — they never produce a leg key.
func TestJoinLegs(t *testing.T) {
	pos := &Outcome{Positive: true}
	segs := []Segment{
		segAt(1, "2026-06-29T20:52:00.000000000Z", "2026-06-29T20:53:00.000000000Z"),
		segAt(2, "2026-06-29T20:55:00.000000000Z", "2026-06-29T21:05:00.000000000Z"),
	}
	segs[1].Outcome = pos
	ev := []SegEvidence{
		{Tell: dialogue.OutcomeAbandoned},
		{Tell: dialogue.OutcomeSuccess},
	}
	events := []retrievalevent.Event{
		{SchemaVersion: 1, Kind: retrievalevent.KindSearch, EventID: "s1", TS: "2026-06-29T20:52:30.000000000Z"},
		{SchemaVersion: 1, Kind: retrievalevent.KindSearch, EventID: "s2", TS: "2026-06-29T21:03:14.271000000Z"},
		{SchemaVersion: 1, Kind: retrievalevent.KindSearch, EventID: "gap", TS: "2026-06-29T20:54:00.000000000Z"},
		{SchemaVersion: 1, Kind: retrievalevent.KindRead, EventID: "r1", TS: "2026-06-29T20:52:45.000000000Z"},
	}

	legs := JoinLegs(segs, events, ev, nil, func(s Segment) string { return "seg#" + strconv.Itoa(s.Index) })

	if len(legs) != 2 {
		t.Fatalf("got %d legs, want 2 (two searches inside a segment; gap + read excluded)", len(legs))
	}
	if l := legs["s1"]; l.SegmentID != "seg#1" || l.Tell != dialogue.OutcomeAbandoned || l.SegOutcome != nil {
		t.Errorf("s1 leg = %+v, want seg#1 / abandoned / nil outcome", l)
	}
	if l := legs["s2"]; l.SegmentID != "seg#2" || l.Tell != dialogue.OutcomeSuccess || l.SegOutcome != pos {
		t.Errorf("s2 leg = %+v, want seg#2 / success / positive outcome", l)
	}
	if _, ok := legs["gap"]; ok {
		t.Error("gap search got a leg; want none (no owning segment)")
	}
	if _, ok := legs["r1"]; ok {
		t.Error("read event got a leg; only searches key legs")
	}
}

// TestJoinLegsFeedsAdjudicate is the end-to-end contract: JoinLegs output drives
// AdjudicateEvents to the right verdict, and the per-search repairAdj map is the
// switch between helped and misled (bbp.17, reading 2). The owning segment's Tell
// is NEUTRAL — the repair opened the next segment — so this proves the adjacent
// repair supplies the leg's Tell on its own, not a coarse segment rollup.
func TestJoinLegsFeedsAdjudicate(t *testing.T) {
	segs := []Segment{
		segAt(1, "2026-06-29T20:52:00.000000000Z", "2026-06-29T20:53:00.000000000Z"),
	}
	ev := []SegEvidence{{Tell: dialogue.OutcomeUnknown}} // clean segment rollup
	events := []retrievalevent.Event{
		{SchemaVersion: 1, Kind: retrievalevent.KindSearch, EventID: "s1", TS: "2026-06-29T20:52:30.000000000Z"},
		{SchemaVersion: 1, Kind: retrievalevent.KindRead, EventID: "r1", TS: "2026-06-29T20:52:45.000000000Z",
			SearchRef: &retrievalevent.SearchRef{EventID: "s1"}},
	}
	segID := func(s Segment) string { return "seg#1" }

	// repair immediately after the read → misled, even though the segment Tell is clean.
	recs, _ := AdjudicateEvents(events, JoinLegs(segs, events, ev, map[string]bool{"s1": true}, segID))
	if len(recs) != 1 || recs[0].Verdict != VerdictMisled {
		t.Fatalf("adjacent repair on a clean segment: verdict = %v, want misled", recs)
	}
	if !recs[0].Correlational {
		t.Error("misled record must be flagged correlational")
	}
	if recs[0].SegmentID != "seg#1" {
		t.Errorf("segment_id = %q, want seg#1", recs[0].SegmentID)
	}

	// same clean segment, no adjacent repair → linked but no tell → helped.
	recs, _ = AdjudicateEvents(events, JoinLegs(segs, events, ev, nil, segID))
	if len(recs) != 1 || recs[0].Verdict != VerdictHelped {
		t.Fatalf("non-adjacent: verdict = %v, want helped", recs)
	}
}

// TestJoinLegsRepairAdjacentConflictWins: a repair adjacent to the read BUT the
// segment shipped a terminal artifact (SegOutcome.Positive) → conflict, not
// misled — the ratified lattice's conflict arm outranks misled (two ferret
// signals disagree, surface for adjudication). Reading 2's documented interaction.
func TestJoinLegsRepairAdjacentConflictWins(t *testing.T) {
	segs := []Segment{segAt(1, "2026-06-29T20:52:00.000000000Z", "2026-06-29T20:53:00.000000000Z")}
	segs[0].Outcome = &Outcome{Positive: true}
	ev := []SegEvidence{{Tell: dialogue.OutcomeUnknown}}
	events := []retrievalevent.Event{
		{SchemaVersion: 1, Kind: retrievalevent.KindSearch, EventID: "s1", TS: "2026-06-29T20:52:30.000000000Z"},
		{SchemaVersion: 1, Kind: retrievalevent.KindRead, EventID: "r1", TS: "2026-06-29T20:52:45.000000000Z",
			SearchRef: &retrievalevent.SearchRef{EventID: "s1"}},
	}
	recs, _ := AdjudicateEvents(events, JoinLegs(segs, events, ev, map[string]bool{"s1": true}, func(Segment) string { return "seg#1" }))
	if len(recs) != 1 || recs[0].Verdict != VerdictConflict {
		t.Fatalf("repair-adjacent + shipped: verdict = %v, want conflict", recs)
	}
}
