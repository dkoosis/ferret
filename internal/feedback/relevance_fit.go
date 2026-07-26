package feedback

// relevantThreshold is the relevance grade at/above which a returned nug counts
// as materially helping the intent — analyst.GradeRelevant (2) on the judge's
// 0..3 scale (0 irrelevant · 1 marginal · 2 relevant · 3 exact). Held as a local
// int so the pure aggregation needs no dependency on the analyst SDK package; the
// live layer passes analyst.NugJudgment.Grade (analyst.RelGrade, int-backed)
// straight through.
const relevantThreshold = 2

// SearchFit aggregates the per-nug relevance grades of ONE get_nug search into a
// single served/mismatch verdict — the content signal the disagreement selector
// consumes as JudgeFit.
//
// It looks only at the nugs the search actually RETURNED (returnedIDs, from the
// kind:search event's returned[]), not the wider candidate pool the relevance
// judge also grades for recall metrics. The search "served" iff at least one
// returned nug is materially relevant (grade >= relevantThreshold): a search that
// surfaced even one on-target nug did its job; one whose every returned nug is
// marginal/irrelevant is a mismatch. Max-over-returned, not top-ranked, so the
// verdict needs no score/rank parsing and credits a relevant nug the agent could
// have used wherever it landed.
//
// ok is false when NONE of the returned nugs carries a grade — no basis to judge,
// so the caller treats the search as FitUnknown and never manufactures a
// disagreement from missing data.
func SearchFit(returnedIDs []string, grades map[string]int) (fit JudgeFit, ok bool) {
	best, graded := 0, false
	for _, id := range returnedIDs {
		g, has := grades[id]
		if !has {
			continue
		}
		graded = true
		if g > best {
			best = g
		}
	}
	if !graded {
		return FitUnknown, false
	}
	if best >= relevantThreshold {
		return FitServed, true
	}
	return FitMismatch, true
}
