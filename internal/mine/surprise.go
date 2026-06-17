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

	out := make([]StreamScore, 0)
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

// SurpriseIndex maps each scored stream's key to its mean surprise (bits/tok),
// so a finding can look up how predictable the sessions it recurs in were.
// Streams too short to score are simply absent (the report treats a miss as "no
// surprise signal" and leaves the finding's base kind untouched).
func SurpriseIndex(scores []StreamScore) map[string]float64 {
	idx := make(map[string]float64, len(scores))
	for _, s := range scores {
		idx[s.Stream] = s.Bits
	}
	return idx
}

// MeanBits is the corpus-wide mean surprise across the scored streams.
func MeanBits(scores []StreamScore) float64 {
	if len(scores) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range scores {
		sum += s.Bits
	}
	return sum / float64(len(scores))
}

// FrictionCut is the surprise threshold above which a recurring routine reads as
// friction (fix it) rather than something to script: one standard deviation
// above the corpus-mean session surprise.
//
// A bare mean is the wrong cut. A motif's surprise is averaged over every
// session it appears in, which regresses widespread motifs to the corpus mean —
// so a mean cut bisects the dense centre of the distribution and mislabels
// average-surprise routines (e.g. git_add⇝git_commit, which lands ~at the mean)
// as friction. Requiring a full σ of EXCESS surprise flips only routines whose
// host sessions are genuine outliers — a true "loop wearing a routine's clothes".
func FrictionCut(scores []StreamScore) float64 {
	m := MeanBits(scores)
	if len(scores) == 0 {
		return m
	}
	v := 0.0
	for _, s := range scores {
		d := s.Bits - m
		v += d * d
	}
	return m + math.Sqrt(v/float64(len(scores)))
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
