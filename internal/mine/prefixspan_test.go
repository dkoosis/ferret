package mine

import (
	"strings"
	"testing"
)

func mineSeqs(t *testing.T, streams [][]string, opts SeqOpts) (map[string]int, bool) {
	t.Helper()
	c := corpusFrom(streams)
	pats, capped := MineSeqs(c, opts)
	got := map[string]int{}
	for _, p := range pats {
		got[strings.Join(c.Tokens(p.IDs), " ")] = p.Support
	}
	return got, capped
}

func TestSeqsFindGappedPattern(t *testing.T) {
	// a…b recurs in all three streams but never adjacently — n-grams miss it.
	got, _ := mineSeqs(t, [][]string{
		{"a", "x", "b"},
		{"a", "y", "b"},
		{"a", "x", "y", "b"},
	}, SeqOpts{MinSupport: 3, MaxGap: 3, MaxLen: 3})
	if got["a b"] != 3 {
		t.Errorf("a⇝b support = %d, want 3; got %v", got["a b"], got)
	}
}

func TestSeqsMaxGapRespected(t *testing.T) {
	got, _ := mineSeqs(t, [][]string{
		{"a", "x", "y", "b"},
		{"a", "x", "y", "b"},
	}, SeqOpts{MinSupport: 2, MaxGap: 2, MaxLen: 3})
	if _, ok := got["a b"]; ok {
		t.Errorf("a⇝b needs gap 3 but max-gap=2; got %v", got)
	}
}

func TestSeqsSupportCountedOncePerStream(t *testing.T) {
	// pattern occurs 3x within one stream — support is streams, not occurrences
	got, _ := mineSeqs(t, [][]string{
		{"a", "b", "a", "b", "a", "b"},
		{"a", "b"},
	}, SeqOpts{MinSupport: 2, MaxGap: 1, MaxLen: 2})
	if got["a b"] != 2 {
		t.Errorf("a⇝b support = %d, want 2 (distinct streams)", got["a b"])
	}
}

func TestSeqsClosedSuppression(t *testing.T) {
	// a b c in every stream: the sub-patterns retain 100% support and must
	// be suppressed by the full pattern.
	got, _ := mineSeqs(t, [][]string{
		{"a", "b", "c"},
		{"a", "b", "c"},
		{"a", "b", "c"},
	}, SeqOpts{MinSupport: 3, MaxGap: 1, MaxLen: 3})
	if _, ok := got["a b"]; ok {
		t.Error("a⇝b should be suppressed by a⇝b⇝c (full retention)")
	}
	if got["a b c"] != 3 {
		t.Errorf("a⇝b⇝c support = %d, want 3; got %v", got["a b c"], got)
	}
}

func TestSeqsSubThresholdExtensionDoesNotSuppress(t *testing.T) {
	// a b in 3 streams; a b c in only 1 — c-extension is below the floor and
	// is never explored, so it must not suppress a b. (Same lesson as ngrams.)
	got, _ := mineSeqs(t, [][]string{
		{"a", "b", "c"},
		{"a", "b", "x"},
		{"a", "b", "y"},
	}, SeqOpts{MinSupport: 3, MaxGap: 1, MaxLen: 3})
	if got["a b"] != 3 {
		t.Errorf("a⇝b support = %d, want 3 — sub-threshold extension must not suppress", got["a b"])
	}
}

func TestSeqsPatternCap(t *testing.T) {
	// many distinct frequent pairs with a cap of 1 → truncated must be true
	got, capped := mineSeqs(t, [][]string{
		{"a", "b", "c", "d"},
		{"a", "b", "c", "d"},
	}, SeqOpts{MinSupport: 2, MaxGap: 3, MaxLen: 2, MaxPatterns: 1})
	if !capped {
		t.Error("cap of 1 with many patterns: truncated must be reported")
	}
	if len(got) != 1 {
		t.Errorf("patterns = %d, want exactly 1 (cap)", len(got))
	}
}
