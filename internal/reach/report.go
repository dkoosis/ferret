package reach

import "time"

// Report is the reach-rate rollup over a window: the headline store-first rate
// plus the channel breakdown and the opportunity list. Every rate is carried
// beside its n — a rate over tens of opportunities/week is noise without it.
type Report struct {
	Since         time.Time     `json:"since"`
	Until         time.Time     `json:"until"`
	N             int           `json:"n"`         // total opportunities
	Recall        int           `json:"recall"`    // recall-question opportunities
	Reorient      int           `json:"reorient"`  // re-orientation asides (tx-vtea)
	Reached       int           `json:"reached"`   // store-first reaches (numerator)
	ReachRate     float64       `json:"reachRate"` // Reached / N — the headline
	Beads         int           `json:"beads"`     // answered by bd read
	Grep          int           `json:"grep"`      // answered by rg/grep/Read/…
	Gh            int           `json:"gh"`        // answered by gh/git forensics
	None          int           `json:"none"`      // no retrieval fired
	KgRate        float64       `json:"kgRate"`    // (store+beads) / N — the whole kg, not just nugs
	Failures      int           `json:"failures"`  // opportunities not store-reached
	Sessions      int           `json:"sessions"`  // distinct sessions with ≥1 opportunity
	DecodeErrs    int           `json:"decodeErrs,omitempty"`
	Opportunities []Opportunity `json:"opportunities"`
}

// Rollup folds a flat opportunity list into the Report over the given window.
func Rollup(opps []Opportunity, win Window, decodeErrs int) Report {
	r := Report{Since: win.Since, Until: win.Until, N: len(opps), DecodeErrs: decodeErrs}
	sessions := map[string]bool{}
	for i := range opps {
		o := &opps[i]
		sessions[o.Project+"/"+o.Session] = true
		switch o.Class {
		case ClassRecall:
			r.Recall++
		case ClassReorient:
			r.Reorient++
		}
		switch o.Reach {
		case ReachStore:
			r.Reached++
		case ReachBeads:
			r.Beads++
		case ReachGrep:
			r.Grep++
		case ReachGh:
			r.Gh++
		default:
			r.None++
		}
	}
	r.Sessions = len(sessions)
	r.Failures = r.N - r.Reached
	if r.N > 0 {
		r.ReachRate = float64(r.Reached) / float64(r.N)
		r.KgRate = float64(r.Reached+r.Beads) / float64(r.N)
	}
	r.Opportunities = opps
	return r
}
