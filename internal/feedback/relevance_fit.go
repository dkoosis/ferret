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
// surfaced even one on-target nug did its job. Max-over-returned, not top-ranked,
// so the verdict needs no score/rank parsing and credits a relevant nug the agent
// could have used wherever it landed.
//
// The two verdicts are asymmetric in what they need from the grade set, because a
// returned nug can be UNGRADED — the Stop hook loops `trixi get` over returnedIDs
// and skips any that 404 (pruned/consolidated between the search and judge), so
// `grades` may cover only a subset:
//
//   - FitServed needs only ONE graded-relevant nug: a relevant hit is a relevant
//     hit no matter what the ungraded rest would have scored, so a partial fetch
//     never hides a served verdict.
//   - FitMismatch is the claim "NOTHING relevant was returned" — unsound if any
//     returned nug is ungraded, since the missing one may have been the relevant
//     hit. Concluding mismatch from a partial subset manufactures a false
//     helped-vs-mismatch disagreement and spends the scarce ask budget on it.
//     So mismatch requires EVERY returned nug to carry a grade.
//
// ok is false (FitUnknown) whenever the grades can't decide: no returned nug
// graded at all, OR a below-threshold subset with some nug still ungraded. The
// caller treats FitUnknown as no-signal and never manufactures a disagreement
// from missing data.
func SearchFit(returnedIDs []string, grades map[string]int) (fit JudgeFit, ok bool) {
	best, graded, allGraded := 0, false, true
	for _, id := range returnedIDs {
		g, has := grades[id]
		if !has {
			allGraded = false
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
	if !allGraded {
		// Every graded nug is below threshold, but an ungraded returned nug could
		// have been the relevant one — can't soundly claim mismatch. No-op.
		return FitUnknown, false
	}
	return FitMismatch, true
}
