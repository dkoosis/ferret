package mine

import "testing"

// TestCountMotifBelowAnyFloor: CountMotif measures a motif straight from the
// corpus (no mining, no min-support), by the same gapped greedy match measure
// uses; a token outside the vocabulary yields the zero count.
func TestCountMotifBelowAnyFloor(t *testing.T) {
	c := bytesCorpus([][]tb{
		{{"A", 400}, {"x", 0}, {"B", 400}, {"A", 400}, {"B", 400}},
		{{"A", 400}, {"B", 400}},
		{{"B", 400}, {"A", 400}},
	})
	got := CountMotif(c, []string{"A", "B"}, 3)
	want := MotifCount{Count: 3, Sessions: 2, Burn: 6 * 400 / BytesPerToken}
	if got != want {
		t.Errorf("CountMotif = %+v, want %+v", got, want)
	}
	if z := CountMotif(c, []string{"A", "nope"}, 3); z != (MotifCount{}) {
		t.Errorf("unknown token = %+v, want zero", z)
	}
	if z := CountMotif(c, nil, 3); z != (MotifCount{}) {
		t.Errorf("empty motif = %+v, want zero", z)
	}
	// maxGap=1: the A,x,B pair no longer matches; A,B pairs in streams 0 and 1 do.
	if g := CountMotif(c, []string{"A", "B"}, 1); g.Count != 2 || g.Sessions != 2 {
		t.Errorf("maxGap=1 = %+v, want count 2 sessions 2", g)
	}
}
