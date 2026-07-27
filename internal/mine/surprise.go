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

// TokenSurprise returns the per-token surprisal (bits) for every stream, aligned
// to c.Streams by index. It trains the same corpus backoff model ScoreSurprise
// uses, but keeps the per-token granularity a span-level consumer needs: the
// stream-mean ScoreSurprise reports collapses a whole session to one number,
// whereas `ferret emit` windows a Seq range and needs the surprisal of the tokens
// inside that window (gg-eqn.1, per-span surprisal). No MinToks filter — a
// candidate span can live in a short stream, and the caller decides which windows
// clear its threshold.
func TokenSurprise(c *Corpus, order int) [][]float64 {
	grams, total := trainGrams(c, order)
	out := make([][]float64, len(c.Streams))
	var ids []uint32
	for si, st := range c.Streams {
		ids = ids[:0]
		for _, t := range st {
			ids = append(ids, t.ID)
		}
		bits := make([]float64, len(ids))
		for i := range ids {
			bits[i] = -math.Log2(scoreIDs(ids, i, grams, total, order))
		}
		out[si] = bits
	}
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
	bits := make([]float64, len(scores))
	for i, s := range scores {
		bits[i] = s.Bits
	}
	m, sd := MeanStdDev(bits)
	return m + sd
}

// MeanStdDev returns the population mean and standard deviation of xs (both 0 for
// empty input, std 0 for a single element). It is the shared spread primitive so
// callers that need a variance metric — FrictionCut's σ-above-mean cut here and
// the pass^k consistency spread in internal/score — compute it one way instead of
// each reinventing it (ferret-kuv.5 Q3: extend this, do not add a parallel σ).
func MeanStdDev(xs []float64) (mean, std float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	mean = sum / float64(len(xs))
	v := 0.0
	for _, x := range xs {
		d := x - mean
		v += d * d
	}
	return mean, math.Sqrt(v / float64(len(xs)))
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
	// unigram floor: normally the token is in the vocab so its count is ≥1 and
	// total ≥1. Guard the degenerate inputs the command boundary also rejects
	// (ferret-v42): an empty corpus (total==0 → 0/0=NaN) or order<1 leaving the
	// model untrained (count==0 → n/0=+Inf). A NaN/Inf flows through -log2 into
	// the mean and FrictionCut, where it silently mislabels the routine/friction
	// split (every comparison with NaN is false). Return a tiny positive score so
	// -log2 stays finite — a maximally-surprising token, the honest reading.
	num := grams[pack(i, i)]
	if num <= 0 || total <= 0 {
		return surpriseFloor
	}
	return alpha * float64(num) / float64(total)
}

// surpriseFloor is the smallest score scoreIDs returns, so -log2 of it is a
// large-but-FINITE surprisal (~40 bits) rather than +Inf/NaN on degenerate input.
const surpriseFloor = 1e-12
