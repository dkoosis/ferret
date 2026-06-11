package mine

import (
	"encoding/binary"
	"math"
	"sort"
)

// StreamScore is one stream's mean per-token surprisal under the corpus model.
type StreamScore struct {
	Stream string  `json:"stream"`
	Toks   int     `json:"toks"`
	Bits   float64 `json:"bits"` // mean -log2 score(tok|context); low = routine, high = thrash
}

// SurpriseOpts bounds the model and the ranking.
type SurpriseOpts struct {
	Order   int // context length: predict each token from up to Order prior tokens
	MinToks int // skip shorter streams — their means are noise
}

// ScoreSurprise trains a backoff n-gram model over the whole corpus and
// scores every stream by mean per-token surprisal. This is the PPM idea
// (Cleary & Witten, "Data Compression Using Adaptive Coding and Partial
// String Matching", IEEE Trans. Comm. 1984) with stupid backoff in place of
// escape probabilities (Brants et al., "Large Language Models in Machine
// Translation", EMNLP 2007): highest-order context with a count wins, each
// backoff multiplies the score by 0.4. Scores aren't normalized
// probabilities — fine for ranking, which is all this is for.
//
// Low surprisal = the model keeps predicting the session = routine,
// scriptable. High surprisal = exploratory or stuck. (Korvemaker & Greiner
// used the same signal to predict shell commands, AAAI 2000.)
//
// The model is trained on the corpus including the stream being scored, so
// every stream contributes its own counts; with thousands of streams the
// self-bias is uniform and doesn't reorder the ranking.
func ScoreSurprise(c *Corpus, opts SurpriseOpts) []StreamScore {
	grams, total := trainGrams(c, opts.Order)

	var out []StreamScore
	var ids []uint32
	for si, st := range c.Streams {
		if len(st) < opts.MinToks {
			continue
		}
		ids = ids[:0]
		for _, t := range st {
			ids = append(ids, t.ID)
		}
		bits := 0.0
		for i := range ids {
			bits += -math.Log2(scoreIDs(ids, i, grams, total, opts.Order))
		}
		out = append(out, StreamScore{
			Stream: c.StreamKeys[si], Toks: len(st), Bits: bits / float64(len(st)),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bits != out[j].Bits {
			return out[i].Bits < out[j].Bits
		}
		return out[i].Stream < out[j].Stream
	})
	return out
}

// trainGrams counts every packed id sequence of length 1..order+1.
func trainGrams(c *Corpus, order int) (map[string]int, int) {
	grams := map[string]int{}
	total := 0
	var key []byte
	for _, st := range c.Streams {
		total += len(st)
		for i := range st {
			for n := 1; n <= order+1 && n <= i+1; n++ {
				key = key[:0]
				for j := i - n + 1; j <= i; j++ {
					key = binary.LittleEndian.AppendUint32(key, st[j].ID)
				}
				grams[string(key)]++
			}
		}
	}
	return grams, total
}

// scoreIDs is the stupid-backoff score of the token at position i.
func scoreIDs(ids []uint32, i int, grams map[string]int, total, order int) float64 {
	pack := func(lo, hi int) string {
		key := make([]byte, 0, (hi-lo+1)*4)
		for j := lo; j <= hi; j++ {
			key = binary.LittleEndian.AppendUint32(key, ids[j])
		}
		return string(key)
	}
	alpha := 1.0
	for k := min(order, i); k >= 1; k-- {
		if num := grams[pack(i-k, i)]; num > 0 {
			return alpha * float64(num) / float64(grams[pack(i-k, i-1)])
		}
		alpha *= 0.4
	}
	// unigram floor: the token is in the vocab, so its count is ≥ 1
	return alpha * float64(grams[pack(i, i)]) / float64(total)
}
