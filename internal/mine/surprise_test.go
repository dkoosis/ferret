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
