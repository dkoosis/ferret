package analyst

// Golden-set harness for the offline relevance metrics (search-quality spec,
// Phase 2): R4 context precision@k, R5 context recall, R6 nDCG@k. The analyst
// relevance judge (relevance.go) supplies graded labels; this file is the pure,
// network-free arithmetic that turns those labels into the metrics dk iterates
// against.
//
// The pipeline per episode:
//
//  1. BuildPool — assemble the candidate pool the judge sees: the nugs the query
//     RETURNED, unioned with a sample of store nugs it did NOT return, then
//     SHUFFLED and stripped of rank/score (NugCandidate carries only ID+Text).
//     Pooling is what makes recall computable — R5 asks "of the relevant nugs
//     that EXIST, what fraction were returned", which is undefined unless the
//     judge also grades un-returned nugs (Spärck Jones & van Rijsbergen 1975,
//     the TREC pooling method). Shuffling + rank-blindness defeats the
//     presentation-bias trap (a judge that sees retrieval order anchors on it).
//
//  2. RunRelevance — the judge grades every pooled candidate 0..3.
//
//  3. ScoreEpisode — REJOIN the grades to the retrieval order (which the judge
//     never saw) and compute R4/R5/R6.
//
// "Relevant" for the set metrics (R4 precision, R5 recall) is grade >= 2 (the
// spec's "materially helps"). R6 nDCG uses the full graded scale — that is the
// point of grading rather than binary judging: a ranking metric must reward
// putting the exact-answer (3) above the merely-relevant (2).

import (
	"math"
	"math/rand"
)

// relevantGrade is the cut for the binary set metrics (R4, R5): grade >= 2
// ("materially helps") counts as relevant. nDCG (R6) ignores this and uses the
// full 0..3 gain scale.
const relevantGrade = GradeRelevant

// Episode is one retrieval episode to score: the human's prompt, the query
// Claude sent, and the nugs the search RETURNED (in retrieval order). The
// returned order is preserved here for rejoin in ScoreEpisode; it is NOT shown
// to the judge — BuildPool strips it.
type Episode struct {
	Name     string         // episode id (e.g. the Event/session key)
	Prompt   string         // the human's request, verbatim
	Query    string         // the query Claude sent to get_nug
	Returned []NugCandidate // nugs the search returned, in rank order
}

// EpisodeScore holds the three graded-relevance metrics for one episode.
type EpisodeScore struct {
	Episode      string  `json:"episode"`
	PrecisionAtK float64 `json:"precisionAtK"` // R4
	Recall       float64 `json:"recall"`       // R5
	NDCGAtK      float64 `json:"nDCGAtK"`      // R6
	K            int     `json:"k"`
}

// BuildPool assembles the candidate pool the relevance judge grades for one
// episode: ep.Returned unioned with up to sampleN store nugs the query did NOT
// return, then shuffled with rng so the judge sees no retrieval order. De-dupes
// by ID (a returned nug also present in store is included once, as returned).
//
// rng is injected (not the global source) so the shuffle is deterministic and
// testable; the production caller passes a freshly seeded *rand.Rand.
func BuildPool(ep Episode, store []NugCandidate, sampleN int, rng *rand.Rand) []NugCandidate {
	returnedIDs := make(map[string]bool, len(ep.Returned))
	pool := make([]NugCandidate, 0, len(ep.Returned)+sampleN)
	for _, c := range ep.Returned {
		if returnedIDs[c.ID] {
			continue
		}
		returnedIDs[c.ID] = true
		pool = append(pool, c)
	}

	// Collect the unreturned store nugs, then sample up to sampleN of them.
	var unreturned []NugCandidate
	for _, c := range store {
		if returnedIDs[c.ID] {
			continue
		}
		unreturned = append(unreturned, c)
	}
	rng.Shuffle(len(unreturned), func(i, j int) {
		unreturned[i], unreturned[j] = unreturned[j], unreturned[i]
	})
	if sampleN < len(unreturned) {
		unreturned = unreturned[:sampleN]
	}
	pool = append(pool, unreturned...)

	// Shuffle the whole pool so returned items are not handed out in rank order —
	// rank-blindness for the judge.
	rng.Shuffle(len(pool), func(i, j int) {
		pool[i], pool[j] = pool[j], pool[i]
	})
	return pool
}

// PrecisionAtK is R4: of the top-k returned nugs, the fraction judged relevant
// (grade >= relevantGrade). The denominator is min(k, len(returned)) — precision
// over what was actually shown. Empty returned → 0.
func PrecisionAtK(returned []string, grades map[string]RelGrade, k int) float64 {
	if len(returned) == 0 || k <= 0 {
		return 0
	}
	n := min(len(returned), k)
	hits := 0
	for _, id := range returned[:n] {
		if grades[id] >= relevantGrade {
			hits++
		}
	}
	return float64(hits) / float64(n)
}

// Recall is R5: of the relevant nugs that EXIST in the judged pool, the fraction
// the query returned. Denominator is every pool nug graded relevant (returned or
// not) — the reason BuildPool samples un-returned nugs. When no relevant nug
// exists in the pool, recall is undefined; we report 0 and the episode is a
// coverage gap (the nug isn't in the store), routed to corpus analysis rather
// than counted as a recall success.
func Recall(returned []string, poolGrades map[string]RelGrade) float64 {
	relevantTotal := 0
	for _, g := range poolGrades {
		if g >= relevantGrade {
			relevantTotal++
		}
	}
	if relevantTotal == 0 {
		return 0
	}
	returnedRelevant := 0
	for _, id := range returned {
		if poolGrades[id] >= relevantGrade {
			returnedRelevant++
		}
	}
	return float64(returnedRelevant) / float64(relevantTotal)
}

// NDCGAtK is R6: normalized discounted cumulative gain over the top-k returned
// nugs, using exponential gain (2^grade - 1) and log2 position discount —
// Järvelin & Kekäläinen 2002, "Cumulated gain-based evaluation of IR
// techniques". The exponential gain (vs. linear) is the de-facto standard
// (Burges et al. 2005, LambdaRank/TREC) and sharpens the reward for ranking an
// exact (3) nug above a merely-relevant (2) one. IDCG is the DCG of the same
// returned grades sorted descending; nDCG = DCG/IDCG. All-zero relevance →
// IDCG=0 → return 0 (not NaN).
func NDCGAtK(returned []string, grades map[string]RelGrade, k int) float64 {
	n := min(len(returned), k)
	if n == 0 {
		return 0
	}
	gains := make([]float64, n)
	for i, id := range returned[:n] {
		gains[i] = math.Exp2(float64(grades[id])) - 1
	}
	dcg := discountedSum(gains)

	ideal := append([]float64(nil), gains...)
	// Sort descending (insertion sort — n is tiny, k results).
	for i := 1; i < len(ideal); i++ {
		for j := i; j > 0 && ideal[j] > ideal[j-1]; j-- {
			ideal[j], ideal[j-1] = ideal[j-1], ideal[j]
		}
	}
	idcg := discountedSum(ideal)
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// discountedSum is sum_i gain_i / log2(i+2) for i=0.. (position 1-indexed: the
// rank-1 discount is log2(2)=1).
func discountedSum(gains []float64) float64 {
	var sum float64
	for i, g := range gains {
		sum += g / math.Log2(float64(i+2))
	}
	return sum
}

// ScoreEpisode rejoins the judge's grades (over the shuffled, rank-blind pool)
// to the episode's retrieval order and computes R4/R5/R6. grades maps every
// pooled nugId → its grade; ScoreEpisode reads the returned order from ep, which
// the judge never saw — that separation is what makes the metrics trustworthy.
func ScoreEpisode(ep Episode, grades map[string]RelGrade, k int) EpisodeScore {
	returned := make([]string, len(ep.Returned))
	for i, c := range ep.Returned {
		returned[i] = c.ID
	}
	return EpisodeScore{
		Episode:      ep.Name,
		PrecisionAtK: PrecisionAtK(returned, grades, k),
		Recall:       Recall(returned, grades),
		NDCGAtK:      NDCGAtK(returned, grades, k),
		K:            k,
	}
}
