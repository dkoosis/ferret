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
func CountGrams(c *Corpus, minN, maxN int) map[string]*Gram {
	grams := map[string]*Gram{}
	var key []byte
	for si, st := range c.Streams {
		for n := minN; n <= maxN; n++ {
			if len(st) < n {
				continue
			}
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
	}
	return grams
}

// Filter applies the noise floor:
//   - min count / min distinct streams
//   - drop grams whose tokens are all identical (read+ → read+ trivia)
//   - closed-gram suppression: drop a gram when an extension by one token
//     retains ≥80% of its count — the longer pattern subsumes it.
func Filter(grams map[string]*Gram, minCount, minSessions int) []*Gram {
	for _, g := range grams {
		if len(g.IDs) < 2 {
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

	var out []*Gram
	for _, g := range grams {
		if g.suppressed || g.Count < minCount || g.Sessions < minSessions {
			continue
		}
		if uniform(g.IDs) {
			continue
		}
		out = append(out, g)
	}
	// v0 ranking: cross-session reach first, then volume, then length.
	// Swap in lift/savings scoring at v1 without touching the counter.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return len(out[i].IDs) > len(out[j].IDs)
	})
	return out
}

func uniform(ids []uint32) bool {
	for _, id := range ids[1:] {
		if id != ids[0] {
			return false
		}
	}
	return true
}
