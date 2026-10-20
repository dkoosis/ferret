package mine

// MotifCount is one motif's measured footprint in a corpus: how many times it
// occurs, in how many distinct streams (sessions), and what those occurrences
// cost. It carries exactly the numbers a report finding carries for the motif.
type MotifCount struct {
	Count    int // total non-overlapping occurrences
	Sessions int // distinct streams containing the motif
	Burn     int // member-call cost in tokens, as Finding.Burn
}

// CountMotif measures a motif given as token strings, straight from the corpus
// by the same gapped greedy match a Finding uses (measure at maxGap). It does
// not mine, so a motif below any min-support floor still gets its true count.
// A token absent from the corpus vocabulary means the motif cannot occur: the
// zero MotifCount comes back.
func CountMotif(c *Corpus, tokens []string, maxGap int) MotifCount {
	if len(tokens) == 0 {
		return MotifCount{}
	}
	vocab := make(map[string]uint32, len(c.Vocab))
	for id, v := range c.Vocab {
		vocab[v] = uint32(id)
	}
	ids := make([]uint32, len(tokens))
	for i, t := range tokens {
		id, ok := vocab[t]
		if !ok {
			return MotifCount{}
		}
		ids[i] = id
	}
	count, sessions, burnBytes, _ := measure(c, ids, maxGap)
	return MotifCount{Count: count, Sessions: sessions, Burn: burnBytes / BytesPerToken}
}
