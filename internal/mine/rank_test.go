package mine

import (
	"math"
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

func TestRankMergesPermutationFamily(t *testing.T) {
	// edit!⇝read, read⇝edit!, edit!⇝read⇝edit are one phenomenon over base
	// tokens {edit, read}; the bucket must show one card, not three.
	streams := make([][]string, 0, 6)
	for range 6 {
		streams = append(streams, []string{"edit!", "read", "edit!", "read", "edit", "read"})
	}
	got, _ := rankAll(t, streams,
		SeqOpts{MinSupport: 6, MaxGap: 1, MaxLen: 3},
		RankOpts{})
	var friction []*Card
	for _, card := range got {
		if card.Bucket == BucketFriction {
			friction = append(friction, card)
		}
	}
	if len(friction) != 1 {
		t.Fatalf("friction cards = %d, want 1 (family merge); got %v", len(friction), keys(got))
	}
	if friction[0].Variants == 0 {
		t.Error("surviving friction card should report merged variants")
	}
}

func TestRankKeepsDistinctFamiliesApart(t *testing.T) {
	// {a,b} churn and {c,d} churn are different phenomena — no cross-merge.
	streams := make([][]string, 0, 6)
	for range 6 {
		streams = append(streams, []string{"a", "b", "a", "c", "d", "c"})
	}
	got, _ := rankAll(t, streams,
		SeqOpts{MinSupport: 6, MaxGap: 1, MaxLen: 3},
		RankOpts{})
	fams := map[string]bool{}
	for pat, card := range got {
		if card.Bucket != BucketLoop {
			continue
		}
		switch {
		case strings.Contains(pat, "a"):
			fams["ab"] = true
		case strings.Contains(pat, "c"):
			fams["cd"] = true
		}
	}
	if !fams["ab"] || !fams["cd"] {
		t.Errorf("both loop families must survive; got %v", keys(got))
	}
}

func keys(m map[string]*Card) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestRankUnpredictableEntryDoesNotSinkRoutine pins the median cohesion
// statistic (ferret-jtv). A real routine is entered unpredictably and then
// runs deterministically: many different tokens precede the trigger, but every
// step after it always follows the one before. Under a mean, that single
// high-surprisal entry transition was enough to push the whole chain past
// ScriptBits into the watch bucket report discards as noise — which is how
// dk's ship sequence and wrap preflight stayed invisible. The median ignores
// the outlier the way it is supposed to.
func TestRankUnpredictableEntryDoesNotSinkRoutine(t *testing.T) {
	// Each stream enters the a→b→c→d routine from a different predecessor, so
	// the ?→a transition is maximally surprising while a→b→c→d is certain.
	preds := []string{"p", "q", "r", "s", "t", "u", "v", "w"}
	streams := make([][]string, 0, len(preds))
	for _, p := range preds {
		streams = append(streams, []string{p, "a", "b", "c", "d"})
	}
	got, _ := rankAll(t, streams,
		SeqOpts{MinSupport: 8, MaxGap: 3, MaxLen: 5},
		RankOpts{})
	routine, ok := got["a b c d"]
	if !ok {
		t.Fatalf("a⇝b⇝c⇝d missing; got %v", keys(got))
	}
	if routine.Bucket != BucketScript {
		t.Errorf("a⇝b⇝c⇝d bucket = %s, want script (bits=%.2f)", routine.Bucket, routine.Bits)
	}
}

// TestPatternBitsMedianLeavesPairsAlone pins the blast radius of the median:
// with a single transition there is nothing to take a median over, so every
// 2-gram scores exactly what the mean gave it. That is what keeps a work
// rhythm like Read⇝Edit — one genuinely uncertain transition, not a routine —
// from being promoted into the script bucket alongside the real routines.
func TestPatternBitsMedianLeavesPairsAlone(t *testing.T) {
	streams := [][]string{
		{"a", "b", "x", "p", "y"},
		{"a", "b", "x", "q", "y"},
		{"a", "b", "x", "r", "y"},
		{"a", "b", "x", "s", "y"},
	}
	c := corpusFrom(streams)
	grams, total := trainGrams(c, 3)
	for _, pair := range [][]string{{"a", "b"}, {"x", "y"}} {
		ids := idsFor(c, pair...)
		want := -math.Log2(scoreIDs(ids, 1, grams, total, 3))
		if got := patternBits(ids, grams, total, 3); got != want {
			t.Errorf("patternBits(%v) = %v, want the single transition's bits %v", pair, got, want)
		}
	}
}

// TestMedian pins the statistic itself, including the even-count average.
func TestMedian(t *testing.T) {
	cases := []struct {
		name string
		in   []float64
		want float64
	}{
		{"empty", nil, 0},
		{"single", []float64{2.5}, 2.5},
		{"odd count takes the middle", []float64{9, 1, 2}, 2},
		{"even count averages the middles", []float64{4, 1, 2, 3}, 2.5},
		{"unsorted input", []float64{5, 0.25, 0.5, 0.25}, 0.375},
		{"one outlier does not move it", []float64{0.25, 0.25, 0.25, 99}, 0.25},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := median(c.in); got != c.want {
				t.Errorf("median(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
