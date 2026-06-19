package score

// Rollup is the session/corpus aggregate over a set of retrieval episodes — the
// RU northstar plus the component rates and the Q/R/C deterministic scorers
// (spec §RU, §A, §B, §C). RU and its three legs are computed over ANSWERABLE
// episodes only (coverage gaps and good abandonments measure the store, not the
// ranker, so they leave RU's denominator); Q1/Q2/R3a are over all episodes.
type Rollup struct {
	Episodes   int `json:"episodes"`
	Answerable int `json:"answerable"`

	// RU northstar + its three component rates, each in both consumed variants.
	RUStrict           float64 `json:"ruStrict"`
	RULoose            float64 `json:"ruLoose"`
	ConsumedRateStrict float64 `json:"consumedRateStrict"`
	ConsumedRateLoose  float64 `json:"consumedRateLoose"`
	FirstTryRate       float64 `json:"firstTryRate"`
	NonAbandonRate     float64 `json:"nonAbandonRate"`

	// A. Query quality (HopInterp).
	Q1SelfRequeryRate float64 `json:"q1SelfRequeryRate"`
	Q2MeanDepth       float64 `json:"q2MeanDepth"`

	// B. Result quality (HopRetrieval).
	R1HitStrict      float64 `json:"r1HitStrict"`
	R1HitLoose       float64 `json:"r1HitLoose"`
	R2MRR            float64 `json:"r2Mrr"`
	R2Ranked         int     `json:"r2Ranked"` // episodes with a known consumed rank
	R3aEmpty         int     `json:"r3aEmpty"`
	R3aOversized     int     `json:"r3aOversized"`
	R3aEmptyRate     float64 `json:"r3aEmptyRate"`
	R3aOversizedRate float64 `json:"r3aOversizedRate"`
	R7GroundingRate  float64 `json:"r7GroundingRate"`
	R7Retrieving     int     `json:"r7Retrieving"` // episodes that returned ≥1 result

	// C. Coverage (the confounder held separate).
	C1CoverageGap int `json:"c1CoverageGap"`
	C2GoodAbandon int `json:"c2GoodAbandon"`
}

// counts are the integer tallies accumulated over the episode set before the
// final rate division — split out so the fold stays a flat, low-complexity loop.
type counts struct {
	episodes, answerable                                int
	servedStrict, servedLoose                           int
	consumedStrict, consumedLoose, firstTry, nonAbandon int
	selfRequery, depthSum                               int
	mrrRanked                                           int
	mrrSum                                              float64
	empty, oversized, coverageGap, goodAbandon          int
	retrieving, grounded                                int
}

// fold tallies one episode into the counts.
func (c *counts) fold(e Episode) {
	c.episodes++
	c.depthSum += e.Queries
	c.selfRequery += b2i(e.SelfRequery)
	c.empty += b2i(e.EmptyResult)
	c.oversized += b2i(e.Oversized)
	c.coverageGap += b2i(e.CoverageGap)
	c.goodAbandon += b2i(e.GoodAbandon)
	if e.ConsumedRank > 0 {
		c.mrrRanked++
		c.mrrSum += 1 / float64(e.ConsumedRank)
	}
	if e.Results > 0 {
		c.retrieving++
		c.grounded += b2i(e.ConsumedStrict)
	}
	if !e.Answerable {
		return
	}
	c.answerable++
	c.consumedStrict += b2i(e.ConsumedStrict)
	c.consumedLoose += b2i(e.ConsumedLoose)
	c.firstTry += b2i(e.FirstTry())
	c.nonAbandon += b2i(e.NonAbandon())
	c.servedStrict += b2i(e.ServedStrict())
	c.servedLoose += b2i(e.ServedLoose())
}

// Aggregate folds episodes into a Rollup. Rates degrade to 0 over an empty
// denominator (no episodes / no answerable / no retrieving) rather than dividing
// by zero, so an empty corpus reports a clean zeroed scorecard.
func Aggregate(eps []Episode) Rollup {
	var c counts
	for i := range eps {
		c.fold(eps[i])
	}
	a := c.answerable
	return Rollup{
		Episodes:           c.episodes,
		Answerable:         a,
		RUStrict:           rate(c.servedStrict, a),
		RULoose:            rate(c.servedLoose, a),
		ConsumedRateStrict: rate(c.consumedStrict, a),
		ConsumedRateLoose:  rate(c.consumedLoose, a),
		FirstTryRate:       rate(c.firstTry, a),
		NonAbandonRate:     rate(c.nonAbandon, a),
		Q1SelfRequeryRate:  rate(c.selfRequery, c.episodes),
		Q2MeanDepth:        rate(c.depthSum, c.episodes),
		R1HitStrict:        rate(c.consumedStrict, a),
		R1HitLoose:         rate(c.consumedLoose, a),
		R2MRR:              rate2(c.mrrSum, c.mrrRanked),
		R2Ranked:           c.mrrRanked,
		R3aEmpty:           c.empty,
		R3aOversized:       c.oversized,
		R3aEmptyRate:       rate(c.empty, c.episodes),
		R3aOversizedRate:   rate(c.oversized, c.episodes),
		R7GroundingRate:    rate(c.grounded, c.retrieving),
		R7Retrieving:       c.retrieving,
		C1CoverageGap:      c.coverageGap,
		C2GoodAbandon:      c.goodAbandon,
	}
}

// b2i is 1 for true, 0 for false — lets the fold stay a flat list of tallies
// instead of one if-block per signal.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// rate2 divides a float sum by an int denominator, returning 0 for a zero
// denominator (the MRR mean over ranked episodes).
func rate2(sum float64, d int) float64 {
	if d == 0 {
		return 0
	}
	return sum / float64(d)
}

// rate divides n by d as a float, returning 0 for a zero denominator.
func rate(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}
