package mine

import (
	"slices"
	"sort"
)

// SeqPattern is one frequent gapped subsequence with support evidence.
type SeqPattern struct {
	IDs      []uint32
	Support  int // distinct streams containing the pattern
	ExStream int // exemplar: stream index + event seq of first match start
	ExSeq    int
}

// SeqOpts bounds the pattern-growth search; all limits are hard.
type SeqOpts struct {
	MinSupport  int // min distinct streams
	MaxGap      int // max positions between consecutive items (1 = adjacent)
	MaxLen      int // max pattern length
	MaxPatterns int // emission cap; overflow is reported, never silent
}

// span is one match of the pattern in a stream: positions of the first and
// last matched items.
type span struct{ start, end int }

// projection is the pseudo-projected database for one pattern: per containing
// stream, every distinct match (earliest start per end, ends ascending).
type projection struct {
	streams []int
	spans   [][]span
}

// MineSeqs finds frequent gapped subsequences across streams by pattern
// growth with pseudo-projection: Pei, Han et al., "PrefixSpan: Mining
// Sequential Patterns Efficiently by Prefix-Projected Pattern Growth",
// ICDE 2001. The max-gap constraint follows cSPADE: Zaki, "Sequence Mining
// in Categorical Domains", CIKM 2000. Support is distinct streams, counted
// once per stream regardless of occurrences.
//
// Unlike CountGrams (contiguous windows), a gap lets the same routine
// surface through interleaved noise: edit → [read] → test still matches
// edit→test at MaxGap ≥ 2.
//
// Returns patterns plus whether MaxPatterns truncated the output.
func MineSeqs(c *Corpus, opts SeqOpts) ([]*SeqPattern, bool) {
	m := &seqMiner{c: c, opts: opts}

	occ := itemProjections(c)
	roots := make([]uint32, 0, len(occ))
	for id, pr := range occ {
		if len(pr.streams) >= opts.MinSupport {
			roots = append(roots, id)
		}
	}
	// deterministic recursion order so a hit emission cap truncates the same way every run
	slices.Sort(roots)
	for _, id := range roots {
		m.grow([]uint32{id}, occ[id])
	}

	sort.Slice(m.out, func(i, j int) bool {
		if m.out[i].Support != m.out[j].Support {
			return m.out[i].Support > m.out[j].Support
		}
		if len(m.out[i].IDs) != len(m.out[j].IDs) {
			return len(m.out[i].IDs) > len(m.out[j].IDs)
		}
		return lessIDs(m.out[i].IDs, m.out[j].IDs)
	})
	return m.out, m.truncated
}

type seqMiner struct {
	c         *Corpus
	opts      SeqOpts
	out       []*SeqPattern
	truncated bool
}

// itemProjections builds the single-item projection for every token.
func itemProjections(c *Corpus) map[uint32]*projection {
	occ := map[uint32]*projection{}
	for si, st := range c.Streams {
		for p, t := range st {
			pr := occ[t.ID]
			if pr == nil {
				pr = &projection{}
				occ[t.ID] = pr
			}
			if n := len(pr.streams); n == 0 || pr.streams[n-1] != si {
				pr.streams = append(pr.streams, si)
				pr.spans = append(pr.spans, []span{})
			}
			last := len(pr.spans) - 1
			pr.spans[last] = append(pr.spans[last], span{p, p})
		}
	}
	return occ
}

// grow recursively extends pat by every frequent item reachable within
// MaxGap, then emits pat unless a frequent extension retains ≥80% of its
// support (δ-tolerant closedness, matching suppressClosed in ngram.go —
// and as there, only an extension that itself clears the floor may suppress).
func (m *seqMiner) grow(pat []uint32, proj *projection) {
	if proj == nil {
		return
	}
	bestChild := 0
	if len(pat) < m.opts.MaxLen {
		sup := m.extSupports(proj)
		exts := make([]uint32, 0, len(sup))
		for id, s := range sup {
			if s >= m.opts.MinSupport {
				exts = append(exts, id)
			}
		}
		slices.Sort(exts)
		for _, id := range exts {
			if sup[id] > bestChild {
				bestChild = sup[id]
			}
			m.grow(append(pat[:len(pat):len(pat)], id), m.project(proj, id))
		}
	}
	if len(pat) >= 2 && bestChild*10 < len(proj.streams)*8 {
		m.emit(pat, proj)
	}
}

// extSupports counts, per candidate extension item, the distinct streams
// where it occurs within MaxGap of a current match end.
func (m *seqMiner) extSupports(proj *projection) map[uint32]int {
	sup := map[uint32]int{}
	for i, si := range proj.streams {
		st := m.c.Streams[si]
		seen := map[uint32]bool{}
		for _, sp := range proj.spans[i] {
			for p := sp.end + 1; p <= sp.end+m.opts.MaxGap && p < len(st); p++ {
				if id := st[p].ID; !seen[id] {
					seen[id] = true
					sup[id]++
				}
			}
		}
	}
	return sup
}

// project extends proj by one item: every position of id within MaxGap of a
// current match end becomes a new span end (earliest start wins per end).
func (m *seqMiner) project(proj *projection, id uint32) *projection {
	np := &projection{}
	for i, si := range proj.streams {
		st := m.c.Streams[si]
		var spans []span
		lastEnd := -1
		for _, sp := range proj.spans[i] {
			for p := sp.end + 1; p <= sp.end+m.opts.MaxGap && p < len(st); p++ {
				if st[p].ID != id || p <= lastEnd {
					continue
				}
				spans = append(spans, span{sp.start, p})
				lastEnd = p
			}
		}
		if len(spans) > 0 {
			np.streams = append(np.streams, si)
			np.spans = append(np.spans, spans)
		}
	}
	return np
}

func (m *seqMiner) emit(pat []uint32, proj *projection) {
	if uniform(pat) {
		return // gapped Read…Read trivia — run-collapse only catches adjacent
	}
	if m.opts.MaxPatterns > 0 && len(m.out) >= m.opts.MaxPatterns {
		m.truncated = true
		return
	}
	ids := make([]uint32, len(pat))
	copy(ids, pat)
	si := proj.streams[0]
	var exSeq int
	if len(proj.spans) > 0 && len(proj.spans[0]) > 0 {
		st := m.c.Streams[si]
		if p := proj.spans[0][0].start; st != nil && p < len(st) {
			exSeq = st[p].Seq
		}
	}
	m.out = append(m.out, &SeqPattern{
		IDs: ids, Support: len(proj.streams),
		ExStream: si, ExSeq: exSeq,
	})
}
