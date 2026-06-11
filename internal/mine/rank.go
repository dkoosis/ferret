package mine

import (
	"math"
	"sort"
	"strings"
)

// Card is one ranked candidate in the review queue: a mined pattern with
// the evidence a reviewer needs to accept or reject it.
type Card struct {
	IDs             []uint32
	Support         int     // distinct streams
	Bits            float64 // mean conditional bits per transition (cohesion; low = deterministic)
	Score           float64
	Bucket          string
	Folded          int // sub-patterns absorbed by this card
	ExStream, ExSeq int
}

// Rank buckets, in display order.
const (
	BucketFriction = "friction" // contains a fail-marked token
	BucketLoop     = "loop"     // revisits a token (a ⇝ b ⇝ a)
	BucketScript   = "script"   // low-entropy chain: prime automation candidate
	BucketWatch    = "watch"    // frequent but not yet classifiable
)

// Buckets lists the bucket names in display order.
var Buckets = []string{BucketFriction, BucketLoop, BucketScript, BucketWatch}

// RankOpts tune the scorer; zero values get defaults from DefaultRankOpts.
type RankOpts struct {
	Order      int     // gram-model order for cohesion scoring
	ScriptBits float64 // cohesion ≤ this → script bucket
	NoiseBits  float64 // cohesion ≥ this scores 0 → noise (watch only)
	Delta      float64 // fold tolerance: sub-pattern folds into super-pattern retaining ≥ Delta of its support
}

func DefaultRankOpts() RankOpts {
	return RankOpts{Order: 3, ScriptBits: 1.0, NoiseBits: 3.0, Delta: 0.8}
}

// withDefaults fills zero fields from DefaultRankOpts.
func (o RankOpts) withDefaults() RankOpts {
	def := DefaultRankOpts()
	if o.Order <= 0 {
		o.Order = def.Order
	}
	if o.ScriptBits <= 0 {
		o.ScriptBits = def.ScriptBits
	}
	if o.NoiseBits <= 0 {
		o.NoiseBits = def.NoiseBits
	}
	if o.Delta <= 0 {
		o.Delta = def.Delta
	}
	return o
}

// RankPatterns turns a mined pattern dump into a short review queue.
//
// Frequency alone ranks corpus background (prompt→Read, the read⇄search
// interleave) above real candidates. The fix is cohesion: score each
// pattern's internal transitions under the same backoff gram model that
// ScoreSurprise trains. A deterministic chain (git_add→git_commit) costs
// well under a bit per transition; a pair that merely co-occurs costs
// several. This is lift against the corpus null model, reusing counts we
// already have — see Brants et al., EMNLP 2007 for the backoff scoring.
//
// Sub-patterns that a longer pattern explains are folded into it first
// (the miner's δ-closure only suppresses prefix extensions, not gapped
// subsequences like a⇝c inside a⇝b⇝c). Watch-bucket patterns whose
// cohesion hits the noise ceiling are dropped and only counted.
func RankPatterns(c *Corpus, pats []*SeqPattern, opts RankOpts) (cards []*Card, noise int) {
	opts = opts.withDefaults()
	grams, total := trainGrams(c, opts.Order)
	kept := fold(pats, opts.Delta)

	for _, k := range kept {
		p := k.pat
		bits := patternBits(p.IDs, grams, total, opts.Order)
		bucket := classify(c, p.IDs, bits, opts.ScriptBits)
		score := math.Log2(float64(p.Support)) * (opts.NoiseBits - math.Min(bits, opts.NoiseBits))
		if bucket == BucketWatch && score <= 0 {
			noise++
			continue
		}
		cards = append(cards, &Card{
			IDs: p.IDs, Support: p.Support, Bits: bits, Score: score,
			Bucket: bucket, Folded: k.folded, ExStream: p.ExStream, ExSeq: p.ExSeq,
		})
	}

	sort.Slice(cards, func(i, j int) bool { return lessCards(cards[i], cards[j]) })
	return cards, noise
}

// lessCards orders by bucket, then within friction/loop by support (the
// evidence) and within script/watch by score, with deterministic ties.
func lessCards(a, b *Card) bool {
	if a.Bucket != b.Bucket {
		return bucketOrder(a.Bucket) < bucketOrder(b.Bucket)
	}
	if a.Bucket == BucketFriction || a.Bucket == BucketLoop {
		if a.Support != b.Support {
			return a.Support > b.Support
		}
	} else if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.Support != b.Support {
		return a.Support > b.Support
	}
	return lessIDs(a.IDs, b.IDs)
}

func bucketOrder(b string) int {
	for i, name := range Buckets {
		if name == b {
			return i
		}
	}
	return len(Buckets)
}

type foldedPat struct {
	pat    *SeqPattern
	folded int
}

// fold absorbs each pattern into the longest already-kept super-pattern
// that retains ≥ delta of its support. Longest first, so super-patterns
// are kept before their subsequences are considered.
func fold(pats []*SeqPattern, delta float64) []*foldedPat {
	byLen := make([]*SeqPattern, len(pats))
	copy(byLen, pats)
	sort.Slice(byLen, func(i, j int) bool {
		if len(byLen[i].IDs) != len(byLen[j].IDs) {
			return len(byLen[i].IDs) > len(byLen[j].IDs)
		}
		if byLen[i].Support != byLen[j].Support {
			return byLen[i].Support > byLen[j].Support
		}
		return lessIDs(byLen[i].IDs, byLen[j].IDs)
	})

	var kept []*foldedPat
	for _, p := range byLen {
		absorbed := false
		for _, k := range kept {
			if len(k.pat.IDs) > len(p.IDs) &&
				float64(k.pat.Support) >= delta*float64(p.Support) &&
				isSubseq(p.IDs, k.pat.IDs) {
				k.folded++
				absorbed = true
				break
			}
		}
		if !absorbed {
			kept = append(kept, &foldedPat{pat: p})
		}
	}
	return kept
}

// isSubseq reports whether sub occurs in order (not necessarily
// contiguously) within sup.
func isSubseq(sub, sup []uint32) bool {
	i := 0
	for _, id := range sup {
		if i < len(sub) && id == sub[i] {
			i++
		}
	}
	return i == len(sub)
}

// patternBits is the mean stupid-backoff surprisal of the pattern's
// transitions, treating the pattern as a contiguous token sequence under
// the corpus gram model. Gapped occurrences pay for their gaps here:
// only chains the corpus actually runs back-to-back score as cohesive.
func patternBits(ids []uint32, grams map[string]int, total, order int) float64 {
	if len(ids) < 2 {
		return 0
	}
	bits := 0.0
	for i := 1; i < len(ids); i++ {
		bits += -math.Log2(scoreIDs(ids, i, grams, total, order))
	}
	return bits / float64(len(ids)-1)
}

func classify(c *Corpus, ids []uint32, bits, scriptBits float64) string {
	seen := map[uint32]bool{}
	for _, id := range ids {
		if failMarked(c.Vocab[id]) {
			return BucketFriction
		}
		if seen[id] {
			return BucketLoop
		}
		seen[id] = true
	}
	if bits <= scriptBits {
		return BucketScript
	}
	return BucketWatch
}

// failMarked detects the MarkFail decorations: "!" (fail) and "?" (cfail),
// possibly followed by the run-collapse "+".
func failMarked(tok string) bool {
	tok = strings.TrimSuffix(tok, "+")
	return strings.HasSuffix(tok, "!") || strings.HasSuffix(tok, "?")
}
