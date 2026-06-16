package mine

import "testing"

func TestSurpriseRanksRoutineBelowThrash(t *testing.T) {
	routine := make([]string, 0, 30)
	for range 10 {
		routine = append(routine, "edit", "test", "commit")
	}
	thrash := []string{
		"edit", "grep", "test", "read", "edit", "commit", "grep", "edit",
		"read", "test", "grep", "commit", "edit", "read", "grep", "test",
		"commit", "read", "edit", "grep", "test", "edit", "read", "commit",
		"grep", "edit", "test", "read", "commit", "edit",
	}
	streams := make([][]string, 0, 6)
	streams = append(streams, thrash)
	for range 5 {
		streams = append(streams, routine)
	}
	c := corpusFrom(streams)
	scores := ScoreSurprise(c, SurpriseOpts{Order: 3, MinToks: 10})
	if len(scores) != 6 {
		t.Fatalf("scores = %d, want 6", len(scores))
	}
	// ascending by bits: the thrash stream (30 toks, scrambled) must rank last
	last := scores[len(scores)-1]
	if last.Toks != len(thrash) {
		t.Errorf("most surprising stream has %d toks, want the scrambled one (%d)", last.Toks, len(thrash))
	}
	if first := scores[0]; first.Bits >= last.Bits {
		t.Errorf("routine bits %.2f should be < thrash bits %.2f", first.Bits, last.Bits)
	}
}

func TestSurpriseIndexAndMeanBits(t *testing.T) {
	scores := []StreamScore{
		{Stream: "p/a@", Bits: 1.0},
		{Stream: "p/b@", Bits: 3.0},
	}
	idx := SurpriseIndex(scores)
	if idx["p/a@"] != 1.0 || idx["p/b@"] != 3.0 {
		t.Errorf("SurpriseIndex = %v, want a@=1.0 b@=3.0", idx)
	}
	if m := MeanBits(scores); m != 2.0 {
		t.Errorf("MeanBits = %.2f, want 2.0", m)
	}
	if m := MeanBits(nil); m != 0 {
		t.Errorf("MeanBits(nil) = %.2f, want 0", m)
	}
}

func TestFrictionCut(t *testing.T) {
	// bits {1,3}: mean 2, σ 1 → cut 3. The σ margin lifts the cut above the mean
	// so average-surprise motifs stay routine.
	if c := FrictionCut([]StreamScore{{Bits: 1}, {Bits: 3}}); c != 3 {
		t.Errorf("FrictionCut = %.2f, want 3 (mean 2 + σ 1)", c)
	}
	// Zero variance → cut collapses to the mean.
	if c := FrictionCut([]StreamScore{{Bits: 2}, {Bits: 2}}); c != 2 {
		t.Errorf("FrictionCut (no spread) = %.2f, want 2", c)
	}
	if c := FrictionCut(nil); c != 0 {
		t.Errorf("FrictionCut(nil) = %.2f, want 0", c)
	}
}

func TestSurpriseSkipsShortStreams(t *testing.T) {
	c := corpusFrom([][]string{
		{"a", "b"},
		{"a", "b", "a", "b", "a", "b", "a", "b", "a", "b"},
	})
	scores := ScoreSurprise(c, SurpriseOpts{Order: 2, MinToks: 5})
	if len(scores) != 1 {
		t.Fatalf("scores = %d, want 1 (2-token stream skipped)", len(scores))
	}
}
