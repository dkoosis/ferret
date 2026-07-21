package score

import (
	"time"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/retrievalevent"
)

// SegEvidence is the caller-computed episode-side signal for one task segment,
// aligned by slice index to a Result's Segments. The score package holds the
// deterministic segmentation and the lattice; the per-segment dialogue reading
// (Tell) and read-adjacency (RepairAdjacent) are wired at the cmd layer where
// the dialogue helpers live, then handed back here — so JoinLegs stays a pure
// function testable without that wiring (ferret-bbp.16).
type SegEvidence struct {
	// Tell is the segment's rolled-up dialogue.Outcome — the human's own words
	// (classifyTurnsCross over the segment's user turns).
	Tell dialogue.Outcome
	// RepairAdjacent reports a repair/abandon move sitting immediately after the
	// read that resolved a search this segment owns. v1 leaves it false: true
	// adjacency needs read-event→following-move linkage the segment grain can't
	// see, and misled is the lattice's one correlational verdict — under-flagging
	// it is the conservative default until that linkage lands (follow-up).
	RepairAdjacent bool
}

// OwningSegment returns the index into segs of the segment whose [FirstTS,LastTS]
// wall-clock interval contains ts (join mechanism A, ferret-bbp.16), or -1 when
// no segment owns it — an unparseable ts, a segment with no owned calls (empty
// interval), or a ts falling in a gap between segments. Timestamps are parsed
// (not string-compared): trixi's sidecar clock and the CC transcript clock carry
// different fractional-second precision, so a lexical compare is unsound.
func OwningSegment(segs []Segment, ts string) int {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return -1
	}
	for i := range segs {
		seg := &segs[i]
		if seg.FirstTS == "" || seg.LastTS == "" {
			continue
		}
		first, err := time.Parse(time.RFC3339Nano, seg.FirstTS)
		if err != nil {
			continue
		}
		last, err := time.Parse(time.RFC3339Nano, seg.LastTS)
		if err != nil {
			continue
		}
		if !t.Before(first) && !t.After(last) {
			return i
		}
	}
	return -1
}

// JoinLegs builds the AdjudicateEvents legs map from a live segmentation. For
// each kind:search event it resolves the owning segment by ts-interval
// containment (OwningSegment) and assembles that segment's Leg: SegmentID from
// segID, SegOutcome straight off the segment, Tell + RepairAdjacent from the
// aligned SegEvidence. ev is indexed by segment position (ev[i] describes
// segs[i]); a nil or short ev yields zero evidence for the missing indices. A
// search event that lands in no segment gets no leg — AdjudicateEvents then
// reads the zero Leg for it (no tell, no outcome), never a fabricated one.
//
// The map is keyed by the search event's EventID, exactly the key
// AdjudicateEvents looks a leg up under.
func JoinLegs(segs []Segment, events []retrievalevent.Event, ev []SegEvidence, segID func(Segment) string) map[string]Leg {
	legs := make(map[string]Leg)
	for i := range events {
		e := &events[i]
		if e.Kind != retrievalevent.KindSearch {
			continue
		}
		idx := OwningSegment(segs, e.TS)
		if idx < 0 {
			continue
		}
		seg := segs[idx]
		leg := Leg{
			SegmentID:  segID(seg),
			SegOutcome: seg.Outcome,
		}
		if idx < len(ev) {
			leg.Tell = ev[idx].Tell
			leg.RepairAdjacent = ev[idx].RepairAdjacent
		}
		legs[e.EventID] = leg
	}
	return legs
}
