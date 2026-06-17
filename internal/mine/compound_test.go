package mine

import (
	"strings"
	"testing"
)

// compoundCorpus builds a Corpus where every token in a stream is given the
// SAME Seq within a "call group": consecutive tokens that belong to one
// compound bash invocation (e.g. `git add && git commit && git push`) share a
// Seq, exactly as the ingest assigns it (all segments of one tool_use line
// inherit that line's seq). Each inner slice of a stream is one call group.
//
// This is the discriminator the miners use to tell a within-call segment
// adjacency from a genuine cross-call transition: same Seq ⇒ same call ⇒ NOT a
// cross-call routine.
func compoundCorpus(streams [][][]string) *Corpus {
	c := &Corpus{}
	intern := map[string]uint32{}
	id := func(t string) uint32 {
		if v, ok := intern[t]; ok {
			return v
		}
		v := uint32(len(c.Vocab))
		intern[t] = v
		c.Vocab = append(c.Vocab, t)
		return v
	}
	for _, calls := range streams {
		var st []Tok
		seq := 0
		for _, call := range calls {
			for _, tok := range call {
				st = append(st, Tok{ID: id(tok), Seq: seq, Bytes: 1})
			}
			seq++ // next call group gets a fresh seq, as a new tool_use line would
		}
		c.Streams = append(c.Streams, st)
		c.StreamKeys = append(c.StreamKeys, "p/s@")
	}
	return c
}

// TestCompoundCallNoCrossCallSeq is the acceptance test for ferret-07s: a
// single compound/looped bash call (its segments sharing one Seq) must NOT be
// mined as a cross-call sequence. Here every stream's ONLY structure is the
// same-call chain git_add → git_commit → git_push; with no genuine multi-call
// sequence anywhere, MineSeqs must return nothing.
func TestCompoundCallNoCrossCallSeq(t *testing.T) {
	streams := make([][][]string, 0, 5)
	for range 5 {
		// one stream = one compound call: add && commit && push (one Seq)
		streams = append(streams, [][]string{{"git_add", "git_commit", "git_push"}})
	}
	c := compoundCorpus(streams)

	pats, _ := MineSeqs(c, SeqOpts{MinSupport: 3, MaxGap: 3, MaxLen: 5})
	for _, p := range pats {
		toks := c.Tokens(p.IDs)
		t.Errorf("compound single-call chain mined as a cross-call sequence: %v (support %d)",
			strings.Join(toks, "⇝"), p.Support)
	}
}

// TestCompoundCallNoCrossCallGram is the n-gram arm: the same single-call chain
// must not surface as a recurring contiguous n-gram either.
func TestCompoundCallNoCrossCallGram(t *testing.T) {
	streams := make([][][]string, 0, 5)
	for range 5 {
		streams = append(streams, [][]string{{"git_add", "git_commit", "git_push"}})
	}
	c := compoundCorpus(streams)

	grams := Filter(CountGrams(c, 2, 3), 2, 2)
	for _, g := range grams {
		t.Errorf("compound single-call chain mined as a cross-call n-gram: %v (count %d)",
			c.Tokens(g.IDs), g.Count)
	}
}

// TestCompoundCallNoFollowsEdge is the directly-follows arm: within-call
// segment adjacencies must not become graph edges.
func TestCompoundCallNoFollowsEdge(t *testing.T) {
	streams := [][][]string{
		{{"git_add", "git_commit", "git_push"}},
	}
	c := compoundCorpus(streams)

	f := BuildFollows(c)
	if len(f.Edges) != 0 {
		t.Errorf("within-call adjacencies produced %d follows edges, want 0: %+v",
			len(f.Edges), f.Edges)
	}
}

// TestGenuineCrossCallStillMined is the guard against over-correction: when the
// SAME three actions occur as three SEPARATE calls (distinct Seqs), they are a
// real cross-call routine and must still be mined.
func TestGenuineCrossCallStillMined(t *testing.T) {
	streams := make([][][]string, 0, 5)
	for range 5 {
		// three separate calls, each its own Seq → genuine cross-call routine
		streams = append(streams, [][]string{{"git_add"}, {"git_commit"}, {"git_push"}})
	}
	c := compoundCorpus(streams)

	pats, _ := MineSeqs(c, SeqOpts{MinSupport: 3, MaxGap: 3, MaxLen: 5})
	want := "git_add⇝git_commit⇝git_push"
	found := false
	for _, p := range pats {
		if strings.Join(c.Tokens(p.IDs), "⇝") == want {
			found = true
		}
	}
	if !found {
		t.Errorf("genuine cross-call routine %s was dropped; patterns: %v", want, pats)
	}

	f := BuildFollows(c)
	if len(f.Edges) == 0 {
		t.Error("genuine cross-call transitions produced no follows edges")
	}
}

// TestMixedCompoundThenSeparate covers the partial case: a compound add&&commit
// call (one Seq) followed by a separate push call (next Seq). The within-call
// add→commit pair is not a cross-call transition, but commit→push (across the
// call boundary) is. Only the genuine boundary edge may appear.
func TestMixedCompoundThenSeparate(t *testing.T) {
	streams := make([][][]string, 0, 5)
	for range 5 {
		streams = append(streams, [][]string{{"git_add", "git_commit"}, {"git_push"}})
	}
	c := compoundCorpus(streams)

	f := BuildFollows(c)
	addID, commitID, pushID := tokID(c, "git_add"), tokID(c, "git_commit"), tokID(c, "git_push")
	for _, e := range f.Edges {
		if e.From == addID && e.To == commitID {
			t.Error("within-call add→commit must not be a follows edge")
		}
	}
	sawBoundary := false
	for _, e := range f.Edges {
		if e.From == commitID && e.To == pushID {
			sawBoundary = true
		}
	}
	if !sawBoundary {
		t.Error("cross-call commit→push boundary edge was dropped")
	}
}

func tokID(c *Corpus, tok string) uint32 {
	for id, v := range c.Vocab {
		if v == tok {
			return uint32(id)
		}
	}
	panic("tokID: token not in vocab: " + tok)
}
