package mine

import (
	"strings"
	"testing"
)

// rankAll mines + ranks a synthetic corpus, returning cards keyed by
// pattern string for assertion convenience.
func rankAll(t *testing.T, streams [][]string, seq SeqOpts, opts RankOpts) (map[string]*Card, int) {
	t.Helper()
	c := corpusFrom(streams)
	pats, _ := MineSeqs(c, seq)
	cards, noise := RankPatterns(c, pats, opts)
	got := map[string]*Card{}
	for _, card := range cards {
		got[strings.Join(c.Tokens(card.IDs), " ")] = card
	}
	return got, noise
}

func TestRankCohesiveChainBeatsCooccurrence(t *testing.T) {
	// a→b is deterministic (b always follows a); x and y co-occur in every
	// stream but y follows many different predecessors, so x⇝y is incoherent.
	streams := make([][]string, 0, 8)
	preds := []string{"p", "q", "r", "s", "t", "u", "v", "w"}
	for i := range 8 {
		streams = append(streams, []string{"a", "b", "x", preds[i], "y"})
	}
	got, _ := rankAll(t, streams,
		SeqOpts{MinSupport: 8, MaxGap: 3, MaxLen: 2},
		RankOpts{})
	ab, ok := got["a b"]
	if !ok {
		t.Fatalf("a⇝b missing; got %v", keys(got))
	}
	if ab.Bucket != BucketScript {
		t.Errorf("a⇝b bucket = %s, want script (bits=%.2f)", ab.Bucket, ab.Bits)
	}
	if xy, ok := got["x y"]; ok {
		if xy.Bucket == BucketScript {
			t.Errorf("x⇝y bucket = script, want non-script (bits=%.2f)", xy.Bits)
		}
		if xy.Score >= ab.Score {
			t.Errorf("x⇝y score %.2f should be < a⇝b score %.2f", xy.Score, ab.Score)
		}
	}
}

func TestRankFoldsSubsequenceIntoSuperPattern(t *testing.T) {
	// a⇝b⇝c everywhere: the gapped subsequence a⇝c survives the miner's
	// prefix-only closure but must fold into the triple here.
	streams := make([][]string, 0, 5)
	for range 5 {
		streams = append(streams, []string{"a", "b", "c"})
	}
	got, _ := rankAll(t, streams,
		SeqOpts{MinSupport: 5, MaxGap: 3, MaxLen: 3},
		RankOpts{})
	if _, ok := got["a c"]; ok {
		t.Error("a⇝c should fold into a⇝b⇝c")
	}
	abc, ok := got["a b c"]
	if !ok {
		t.Fatalf("a⇝b⇝c missing; got %v", keys(got))
	}
	if abc.Folded == 0 {
		t.Error("a⇝b⇝c should report folded sub-patterns")
	}
}

func TestRankDoesNotFoldIndependentPattern(t *testing.T) {
	// a⇝b is far more frequent than a⇝b⇝c — folding it away would hide
	// the dominant pattern behind a rare extension.
	streams := [][]string{
		{"a", "b", "c"}, {"a", "b", "c"},
		{"a", "b", "x"}, {"a", "b", "y"}, {"a", "b", "z"},
		{"a", "b", "w"}, {"a", "b", "v"}, {"a", "b", "u"},
	}
	got, _ := rankAll(t, streams,
		SeqOpts{MinSupport: 2, MaxGap: 1, MaxLen: 3},
		RankOpts{})
	if _, ok := got["a b"]; !ok {
		t.Errorf("a⇝b (support 8) must survive a⇝b⇝c (support 2); got %v", keys(got))
	}
}

func TestRankFailMarkedPatternIsFriction(t *testing.T) {
	streams := make([][]string, 0, 5)
	for range 5 {
		streams = append(streams, []string{"edit!", "read", "edit!", "read"})
	}
	got, _ := rankAll(t, streams,
		SeqOpts{MinSupport: 5, MaxGap: 1, MaxLen: 2},
		RankOpts{})
	er, ok := got["edit! read"]
	if !ok {
		t.Fatalf("edit!⇝read missing; got %v", keys(got))
	}
	if er.Bucket != BucketFriction {
		t.Errorf("edit!⇝read bucket = %s, want friction", er.Bucket)
	}
}

func TestRankRevisitIsLoop(t *testing.T) {
	streams := make([][]string, 0, 5)
	for range 5 {
		streams = append(streams, []string{"a", "b", "a", "b", "a"})
	}
	got, _ := rankAll(t, streams,
		SeqOpts{MinSupport: 5, MaxGap: 1, MaxLen: 3},
		RankOpts{})
	found := false
	for pat, card := range got {
		if strings.Count(pat, "a") >= 2 || strings.Count(pat, "b") >= 2 {
			found = true
			if card.Bucket != BucketLoop {
				t.Errorf("%q bucket = %s, want loop", pat, card.Bucket)
			}
		}
	}
	if !found {
		t.Fatalf("no revisiting pattern mined; got %v", keys(got))
	}
}

func TestRankNoiseCeilingDropsIncoherentWatch(t *testing.T) {
	// x⇝y co-occurs in every stream but with maximal predecessor diversity
	// and a huge gap-driven vocabulary — its cohesion must hit the ceiling.
	streams := make([][]string, 0, 12)
	fillers := []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12"}
	for i := range 12 {
		streams = append(streams, []string{"x", fillers[i], fillers[(i+5)%12], "y"})
	}
	got, noise := rankAll(t, streams,
		SeqOpts{MinSupport: 12, MaxGap: 3, MaxLen: 2},
		RankOpts{NoiseBits: 1.5})
	if _, ok := got["x y"]; ok {
		t.Errorf("x⇝y should be noise at a 1.5-bit ceiling (got bucket %s, bits %.2f)",
			got["x y"].Bucket, got["x y"].Bits)
	}
	if noise == 0 {
		t.Error("noise count should be > 0")
	}
}

func keys(m map[string]*Card) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
