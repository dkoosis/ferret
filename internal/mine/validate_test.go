package mine

import (
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// TestValidateJoinsOutcomes builds a tiny corpus by hand: two streams that
// support a friction pattern both failed; one script stream succeeded. The
// report must surface the friction bucket at a higher fail-share than base.
func TestValidateJoinsOutcomes(t *testing.T) {
	// vocab: 0=open 1=test! 2=submit
	c := &Corpus{
		Vocab: []string{"open", "sh:python!", "submit"},
		StreamKeys: []string{
			"swe-agent/a@", "swe-agent/b@", "swe-agent/c@",
		},
		Streams: [][]Tok{
			{{ID: 0}, {ID: 1}, {ID: 2}}, // a: friction (fail-marked)
			{{ID: 0}, {ID: 1}, {ID: 2}}, // b: friction
			{{ID: 0}, {ID: 2}},          // c: clean
		},
	}
	cards := []*Card{
		{IDs: []uint32{0, 1}, Bucket: BucketFriction},
		{IDs: []uint32{0, 2}, Bucket: BucketScript},
	}
	outcomes := map[string]event.Outcome{
		"swe-agent/a@": {Stream: "swe-agent/a@", Target: false},
		"swe-agent/b@": {Stream: "swe-agent/b@", Target: false},
		"swe-agent/c@": {Stream: "swe-agent/c@", Target: true},
	}

	v := Validate(c, cards, outcomes, "swe-agent", 1, 1)
	if v.Streams != 3 {
		t.Fatalf("base streams = %d, want 3", v.Streams)
	}
	wantBase := 100 * 2.0 / 3.0
	if v.BaseFail < wantBase-0.1 || v.BaseFail > wantBase+0.1 {
		t.Errorf("base fail = %.1f, want %.1f", v.BaseFail, wantBase)
	}

	byBucket := map[string]BucketReport{}
	for _, b := range v.Buckets {
		byBucket[b.Bucket] = b
	}
	fr, ok := byBucket[BucketFriction]
	if !ok {
		t.Fatal("no friction bucket in report")
	}
	if fr.Streams != 2 || fr.Failures != 2 || fr.FailShare != 100 {
		t.Errorf("friction = %+v; want 2 streams / 2 fails / 100%%", fr)
	}
	if fr.Lift <= 1.0 {
		t.Errorf("friction lift = %.2f, want > 1 (concentrates failures)", fr.Lift)
	}
}

// TestContainsSubseqGap pins validate's "supports" to the miner's gap window:
// a pattern whose items sit further apart than maxGap must not claim the
// stream, even though an unbounded gapped match would find it.
func TestContainsSubseqGap(t *testing.T) {
	seq := []uint32{0, 9, 9, 1} // 0 .. 1 with two tokens between
	sub := []uint32{0, 1}
	if !containsSubseqGap(seq, sub, 3) {
		t.Error("gap=3 spans the pair, want match")
	}
	if containsSubseqGap(seq, sub, 1) {
		t.Error("gap=1 cannot span two intervening tokens, want no match")
	}
	if !containsSubseqGap(seq, sub, 0) { // 0 => unbounded fallback
		t.Error("gap<=0 falls back to unbounded, want match")
	}
	// Later occurrence satisfies a tight gap the first one can't: 0 at pos 0
	// is too far from 1 at pos 3, but 0 at pos 2 is adjacent.
	if !containsSubseqGap([]uint32{0, 9, 0, 1}, sub, 1) {
		t.Error("second 0 is adjacent to 1, want match at gap=1")
	}
}

// TestValidateMinStreamsFilter drops a bucket below the support floor.
func TestValidateMinStreamsFilter(t *testing.T) {
	c := &Corpus{
		Vocab:      []string{"open", "submit"},
		StreamKeys: []string{"swe-agent/a@"},
		Streams:    [][]Tok{{{ID: 0}, {ID: 1}}},
	}
	cards := []*Card{{IDs: []uint32{0, 1}, Bucket: BucketScript}}
	outcomes := map[string]event.Outcome{"swe-agent/a@": {Target: true}}

	v := Validate(c, cards, outcomes, "swe-agent", 2, 3)
	if len(v.Buckets) != 0 {
		t.Errorf("buckets = %d, want 0 (1 stream < min-streams 2)", len(v.Buckets))
	}
}
