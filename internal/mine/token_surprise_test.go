package mine

import "testing"

// TestTokenSurprise_ShapeAndPredictability asserts TokenSurprise returns one bits
// slice per stream aligned to c.Streams, and that a token following a
// well-attested context scores LOWER (more predictable) than a token following a
// rare one — the per-token analogue of ScoreSurprise's stream-mean ranking.
func TestTokenSurprise_ShapeAndPredictability(t *testing.T) {
	c := &Corpus{Vocab: []string{"a", "b", "z"}}
	// Three streams repeat a→b; one stream ends a→z (the surprising tail).
	for range 3 {
		c.Streams = append(c.Streams, []Tok{{ID: 0, Seq: 0}, {ID: 1, Seq: 1}})
		c.StreamKeys = append(c.StreamKeys, "p/s@")
	}
	c.Streams = append(c.Streams, []Tok{{ID: 0, Seq: 0}, {ID: 2, Seq: 1}})
	c.StreamKeys = append(c.StreamKeys, "p/s4@")

	bits := TokenSurprise(c, 2)
	if len(bits) != len(c.Streams) {
		t.Fatalf("got %d bit-slices, want %d streams", len(bits), len(c.Streams))
	}
	for si, b := range bits {
		if len(b) != len(c.Streams[si]) {
			t.Fatalf("stream %d: %d bits for %d tokens", si, len(b), len(c.Streams[si]))
		}
	}
	// Position 1 of a repeated a→b stream (index 0) vs the a→z tail (index 3):
	// the well-attested continuation must be less surprising.
	if bits[0][1] >= bits[3][1] {
		t.Errorf("a→b surprisal %.3f should be < a→z surprisal %.3f", bits[0][1], bits[3][1])
	}
}

// TestTokenSurprise_Finite guards the degenerate inputs the command boundary also
// rejects: every per-token bit must stay finite (the surpriseFloor guard), never
// NaN/Inf, so a downstream mean/threshold is well-defined.
func TestTokenSurprise_Finite(t *testing.T) {
	c := &Corpus{
		Vocab:      []string{"x"},
		Streams:    [][]Tok{{{ID: 0, Seq: 0}}},
		StreamKeys: []string{"p/s@"},
	}
	for _, b := range TokenSurprise(c, 3) {
		for i, v := range b {
			if v != v || v > 1e6 { // NaN != NaN
				t.Errorf("token %d surprisal not finite: %v", i, v)
			}
		}
	}
}
