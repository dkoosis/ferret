package mine

import "testing"

func corpusFrom(streams [][]string) *Corpus {
	c := &Corpus{}
	intern := map[string]uint32{}
	for _, toks := range streams {
		st := make([]Tok, 0, len(toks))
		for i, t := range toks {
			id, ok := intern[t]
			if !ok {
				id = uint32(len(c.Vocab))
				intern[t] = id
				c.Vocab = append(c.Vocab, t)
			}
			st = append(st, Tok{ID: id, Seq: i})
		}
		c.Streams = append(c.Streams, st)
		c.StreamKeys = append(c.StreamKeys, "p/s@")
	}
	return c
}

func TestNgramSupergramSuppression(t *testing.T) {
	// edit→test→diff occurs 4x across 4 streams; its sub-bigrams should be
	// suppressed because the trigram retains 100% of their count.
	streams := make([][]string, 0, 4)
	for range 4 {
		streams = append(streams, []string{"x", "edit", "test", "diff", "y"})
	}
	c := corpusFrom(streams)
	grams := Filter(CountGrams(c, 2, 3), 2, 2)
	for _, g := range grams {
		toks := c.Tokens(g.IDs)
		if len(toks) == 2 && toks[0] == "edit" && toks[1] == "test" {
			t.Errorf("sub-bigram edit→test should be suppressed by edit→test→diff")
		}
	}
	found := false
	for _, g := range grams {
		toks := c.Tokens(g.IDs)
		if len(toks) == 3 && toks[0] == "edit" && toks[2] == "diff" {
			found = true
			if g.Count != 4 || g.Sessions != 4 {
				t.Errorf("trigram count=%d sessions=%d, want 4/4", g.Count, g.Sessions)
			}
		}
	}
	if !found {
		t.Fatal("trigram edit→test→diff not found")
	}
}

func TestCollapse(t *testing.T) {
	c := corpusFrom([][]string{{"read", "read", "read", "edit", "read"}})
	intern := map[string]uint32{}
	for i, v := range c.Vocab {
		intern[v] = uint32(i)
	}
	c.collapse(intern)
	got := c.Tokens(idsOf(c.Streams[0]))
	want := []string{"read+", "edit", "read"}
	if len(got) != len(want) {
		t.Fatalf("collapse = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("collapse = %v, want %v", got, want)
		}
	}
}

func idsOf(st []Tok) []uint32 {
	out := make([]uint32, len(st))
	for i, t := range st {
		out[i] = t.ID
	}
	return out
}
