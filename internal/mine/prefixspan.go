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
	// Floor the support to 1: MinSupport<=0 makes EVERY token a frequent root and
	// every extension clear the growth floor, so the projected-database recursion
	// explodes combinatorially (the MaxPatterns cap bounds only emission, not the
	// descent) → OOM/hang on a real corpus (ferret-g2o). The command boundary
	// rejects it with a clear error; this is the library's own safety net for any
	// other caller. MaxGap/MaxLen below 1 are self-limiting (no extension clears),
	// so they need no floor here.
	if opts.MinSupport < 1 {
		opts.MinSupport = 1
	}
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
	// Support is cross-call support: streams whose match spans ≥2 distinct
	// bash calls. A pattern matched only within single compound calls (one
	// `a && b && c` invocation) is not a cross-call routine (ferret-07s).
	if support := m.crossCallSupport(proj); len(pat) >= 2 && support >= m.opts.MinSupport &&
		bestChild*10 < support*8 {
		m.emit(pat, proj, support)
	}
}

// crossCallSupport counts distinct streams in proj that hold at least one span
// covering two distinct calls (start and end in different Seqs). A span confined
// to one call is the within-call segment adjacency of a single compound bash
// invocation — not a cross-call occurrence.
func (m *seqMiner) crossCallSupport(proj *projection) int {
	n := 0
	for i, si := range proj.streams {
		st := m.c.Streams[si]
		for _, sp := range proj.spans[i] {
			if st[sp.start].Seq != st[sp.end].Seq {
				n++
				break
			}
		}
	}
	return n
}

// extSupports counts, per candidate extension item, the distinct streams where
// extending the pattern yields a CROSS-CALL occurrence — i.e. the new span
// (sp.start … p) covers two distinct calls. An extension that stays inside one
// compound call is a within-call segment adjacency, not a cross-call step, and
// must not earn support (ferret-07s).
func (m *seqMiner) extSupports(proj *projection) map[uint32]int {
	sup := map[uint32]int{}
	for i, si := range proj.streams {
		st := m.c.Streams[si]
		seen := map[uint32]bool{}
		for _, sp := range proj.spans[i] {
			for p := sp.end + 1; p <= sp.end+m.opts.MaxGap && p < len(st); p++ {
				id := st[p].ID
				if seen[id] || st[sp.start].Seq == st[p].Seq {
					continue
				}
				seen[id] = true
				sup[id]++
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

func (m *seqMiner) emit(pat []uint32, proj *projection, support int) {
	if uniform(pat) {
		return // gapped Read…Read trivia — run-collapse only catches adjacent
	}
	if m.opts.MaxPatterns > 0 && len(m.out) >= m.opts.MaxPatterns {
		m.truncated = true
		return
	}
	ids := make([]uint32, len(pat))
	copy(ids, pat)
	// Exemplar must be a genuine (cross-call) occurrence, so the card a reviewer
	// inspects is a real multi-call routine, not a within-call segment chain.
	exStream, exSeq := m.crossCallExemplar(proj)
	m.out = append(m.out, &SeqPattern{
		IDs: ids, Support: support,
		ExStream: exStream, ExSeq: exSeq,
	})
}

// crossCallExemplar returns the stream index and start Seq of the first span
// that spans two distinct calls — the routine's first genuine cross-call match.
func (m *seqMiner) crossCallExemplar(proj *projection) (exStream, exSeq int) {
	for i, si := range proj.streams {
		st := m.c.Streams[si]
		for _, sp := range proj.spans[i] {
			if st[sp.start].Seq != st[sp.end].Seq {
				return si, st[sp.start].Seq
			}
		}
	}
	return proj.streams[0], 0
}
