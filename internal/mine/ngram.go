package mine

import (
	"encoding/binary"
	"sort"
)

// Gram is one frequent n-gram with support evidence.
type Gram struct {
	IDs        []uint32
	Count      int
	Sessions   int // distinct streams it appears in
	lastStream int
	ExStream   int // exemplar: stream index + event seq of first occurrence
	ExSeq      int
	suppressed bool
}

// CountGrams counts every n-gram for n in [minN, maxN] across all streams.
//
// Sliding-window n-gram counting over event sequences is the serial-episode
// special case of frequent episode discovery: Mannila, Toivonen & Verkamo,
// "Discovery of Frequent Episodes in Event Sequences", Data Mining and
// Knowledge Discovery 1(3), 1997. N-gram statistics over symbol streams trace
// to Shannon, "A Mathematical Theory of Communication", Bell Syst. Tech. J.
// 27, 1948.
func CountGrams(c *Corpus, minN, maxN int) map[string]*Gram {
	grams := map[string]*Gram{}
	for si, st := range c.Streams {
		for n := minN; n <= maxN; n++ {
			countStream(grams, st, si, n)
		}
	}
	return grams
}

// countStream counts every length-n window of one stream into grams.
func countStream(grams map[string]*Gram, st []Tok, si, n int) {
	var key []byte
	for i := 0; i+n <= len(st); i++ {
		key = key[:0]
		for j := i; j < i+n; j++ {
			key = binary.LittleEndian.AppendUint32(key, st[j].ID)
		}
		g, ok := grams[string(key)]
		if !ok {
			ids := make([]uint32, n)
			for j := range ids {
				ids[j] = st[i+j].ID
			}
			g = &Gram{IDs: ids, lastStream: -1, ExStream: si, ExSeq: st[i].Seq}
			grams[string(key)] = g
		}
		g.Count++
		if g.lastStream != si {
			g.Sessions++
			g.lastStream = si
		}
	}
}

// Filter applies the noise floor:
//   - min count / min distinct streams
//   - drop grams whose tokens are all identical (read+ → read+ trivia)
//   - closed-gram suppression: drop a gram when an extension by one token
//     retains ≥80% of its count — the longer pattern subsumes it.
//
// Suppression is a relaxed form of closed-pattern mining: closed itemsets per
// Pasquier, Bastide, Taouil & Lakhal, "Discovering Frequent Closed Itemsets
// for Association Rules", ICDT 1999; closed sequential patterns per Yan, Han
// & Afshar, "CloSpan: Mining Closed Sequential Patterns in Large Databases",
// SDM 2003. The ≥80%-retention slack mirrors δ-tolerance closedness: Cheng,
// Ke & Ng, "δ-Tolerance Closed Frequent Itemsets", ICDM 2006.
func Filter(grams map[string]*Gram, minCount, minSessions int) []*Gram {
	passes := func(g *Gram) bool {
		return g.Count >= minCount && g.Sessions >= minSessions && !uniform(g.IDs)
	}
	suppressClosed(grams, passes)

	var out []*Gram
	for _, g := range grams {
		if g.suppressed || !passes(g) {
			continue
		}
		out = append(out, g)
	}
	// v0 ranking: cross-session reach first, then volume, then length;
	// ID-lexicographic last so ties are deterministic (map order isn't).
	// Swap in lift/savings scoring at v1 without touching the counter.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if len(out[i].IDs) != len(out[j].IDs) {
			return len(out[i].IDs) > len(out[j].IDs)
		}
		return lessIDs(out[i].IDs, out[j].IDs)
	})
	return out
}

func lessIDs(a, b []uint32) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// suppressClosed marks the prefix and suffix of each gram suppressed when the
// extension retains ≥80% of the shorter gram's count. Only a gram that itself
// clears the noise floor may suppress: otherwise a sub-threshold extension
// kills its prefix and then gets filtered, leaving neither in the output.
func suppressClosed(grams map[string]*Gram, passes func(*Gram) bool) {
	for _, g := range grams {
		if len(g.IDs) < 2 || !passes(g) {
			continue
		}
		sub := func(ids []uint32) {
			key := make([]byte, 0, len(ids)*4)
			for _, id := range ids {
				key = binary.LittleEndian.AppendUint32(key, id)
			}
			if s, ok := grams[string(key)]; ok && g.Count*10 >= s.Count*8 {
				s.suppressed = true
			}
		}
		sub(g.IDs[:len(g.IDs)-1]) // prefix
		sub(g.IDs[1:])            // suffix
	}
}

func uniform(ids []uint32) bool {
	for _, id := range ids[1:] {
		if id != ids[0] {
			return false
		}
	}
	return true
}
