package mine

import (
	"sort"

	"github.com/dkoosis/ferret/internal/event"
)

// BucketReport is one bucket's outcome correlation: of the streams that
// support any pattern in the bucket, what share carry target=false, and how
// that compares to the corpus base failure rate.
type BucketReport struct {
	Bucket    string  `json:"bucket"`
	Patterns  int     `json:"patterns"`  // cards in this bucket
	Streams   int     `json:"streams"`   // distinct supporting streams with a known outcome
	Failures  int     `json:"failures"`  // of those, target=false
	FailShare float64 `json:"failShare"` // failures / streams
	Lift      float64 `json:"lift"`      // failShare / baseFail
}

// Validation is the corpus-level outcome join over ranked buckets.
type Validation struct {
	Corpus       string         `json:"corpus"`
	Streams      int            `json:"streams"` // streams with a known outcome
	BaseFailures int            `json:"baseFailures"`
	BaseFail     float64        `json:"baseFail"` // corpus-wide target=false rate
	Buckets      []BucketReport `json:"buckets"`
}

// Validate joins ranked cards against stream outcomes. For each bucket it
// collects the distinct streams supporting any of its patterns and measures
// their failure rate against the corpus base rate.
//
// minSupportStreams drops buckets whose supporting-stream count is below the
// floor, so a tiny-n bucket can't post a wild lift on two streams.
//
// maxGap must match the value passed to MineSeqs: "supports" is measured with
// the same gap-bounded projection the miner used to find the pattern, so a
// bucket can't claim a stream the miner would not have. A maxGap <= 0 falls
// back to unbounded gapped containment (looser; inflates support).
func Validate(c *Corpus, cards []*Card, outcomes map[string]event.Outcome, corpus string, minSupportStreams, maxGap int) *Validation {
	base, baseFails := baseRate(c, outcomes)
	v := &Validation{
		Corpus:       corpus,
		Streams:      base,
		BaseFailures: baseFails,
		BaseFail:     share(baseFails, base),
	}
	baseFail := v.BaseFail

	byBucket := map[string][]*Card{}
	for _, card := range cards {
		byBucket[card.Bucket] = append(byBucket[card.Bucket], card)
	}
	for _, b := range Buckets {
		bc := byBucket[b]
		if len(bc) == 0 {
			continue
		}
		streams := supportingStreams(c, bc, maxGap)
		n, fails := outcomeCounts(c, streams, outcomes)
		if n < minSupportStreams {
			continue
		}
		fs := share(fails, n)
		lift := 0.0
		if baseFail > 0 {
			lift = fs / baseFail
		}
		v.Buckets = append(v.Buckets, BucketReport{
			Bucket: b, Patterns: len(bc), Streams: n,
			Failures: fails, FailShare: fs, Lift: lift,
		})
	}
	return v
}

// baseRate counts streams with a known outcome and how many failed.
func baseRate(c *Corpus, outcomes map[string]event.Outcome) (n, fails int) {
	for _, key := range c.StreamKeys {
		o, ok := outcomes[key]
		if !ok {
			continue
		}
		n++
		if !o.Target {
			fails++
		}
	}
	return n, fails
}

// supportingStreams returns the set of stream indices that contain at least
// one of the bucket's patterns under the miner's gap-bounded projection.
func supportingStreams(c *Corpus, cards []*Card, maxGap int) map[int]bool {
	set := map[int]bool{}
	for si, st := range c.Streams {
		ids := streamIDs(st)
		for _, card := range cards {
			if containsSubseqGap(ids, card.IDs, maxGap) {
				set[si] = true
				break
			}
		}
	}
	return set
}

// outcomeCounts counts streams in the set that have a known outcome and how
// many of those failed.
func outcomeCounts(c *Corpus, streams map[int]bool, outcomes map[string]event.Outcome) (n, fails int) {
	idxs := make([]int, 0, len(streams))
	for si := range streams {
		idxs = append(idxs, si)
	}
	sort.Ints(idxs)
	for _, si := range idxs {
		o, ok := outcomes[c.StreamKeys[si]]
		if !ok {
			continue
		}
		n++
		if !o.Target {
			fails++
		}
	}
	return n, fails
}

func streamIDs(st []Tok) []uint32 {
	ids := make([]uint32, len(st))
	for i, t := range st {
		ids[i] = t.ID
	}
	return ids
}

// containsSubseqGap reports whether sub occurs in order within seq with no
// more than maxGap positions between consecutive items — the same projection
// MineSeqs uses (a next item must fall in (end, end+maxGap]). maxGap <= 0
// falls back to unbounded gapped containment.
//
// It tracks the full set of reachable end-positions per item rather than
// greedily taking the first match, mirroring the miner's multi-span
// projection so a later occurrence can still satisfy a tight gap.
func containsSubseqGap(seq, sub []uint32, maxGap int) bool {
	if len(sub) == 0 {
		return false
	}
	if maxGap <= 0 {
		return containsSubseq(seq, sub)
	}
	// ends: positions in seq where sub[0..k] has matched, ending here.
	var ends []int
	for p, id := range seq {
		if id == sub[0] {
			ends = append(ends, p)
		}
	}
	for k := 1; k < len(sub) && len(ends) > 0; k++ {
		ends = advanceEnds(seq, ends, sub[k], maxGap)
	}
	return len(ends) > 0
}

// advanceEnds extends each match-end in ends to the positions of want that
// fall within maxGap, mirroring the miner's projection (a next item must land
// in (end, end+maxGap]).
func advanceEnds(seq []uint32, ends []int, want uint32, maxGap int) []int {
	var next []int
	lastEnd := -1
	for _, e := range ends {
		for p := e + 1; p <= e+maxGap && p < len(seq); p++ {
			if seq[p] == want && p > lastEnd {
				next = append(next, p)
				lastEnd = p
			}
		}
	}
	return next
}

// containsSubseq reports whether sub occurs in order (unbounded gap) within
// seq. Retained for the maxGap <= 0 fallback.
func containsSubseq(seq, sub []uint32) bool {
	if len(sub) == 0 {
		return false
	}
	i := 0
	for _, id := range seq {
		if id == sub[i] {
			i++
			if i == len(sub) {
				return true
			}
		}
	}
	return false
}

func share(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}
