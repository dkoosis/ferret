package score

import "github.com/dkoosis/ferret/internal/dialogue"

// Deterministic Hop2 (query→result) retrieval-quality scorer — ferret-bbp.4.
//
// This is the post-retrieval QPP (query-performance prediction) half of the
// two-hop rating model (internal/dialogue/attribute.go): a GRADED retrieval-
// quality verdict read off the served result-set, scored even when nothing broke
// and with NO ground-truth relevance label. Reference-free QPP is the only
// retrieval-quality family that fits ferret's no-ground-truth posture.
//
// Borrowed method + citation (repo convention ferret-cite-algos-in-code):
//   - Clarity Score — a focused query yields a peaked result distribution, a
//     broad one a diffuse (low-clarity) set. Cronen-Townsend, Zhou & Croft,
//     SIGIR 2002. The oversized-set demotion is the deterministic clarity analog.
//   - NQC / WIG — post-retrieval, score-based predictors. Shtok et al., TOIS
//     2012; Zhou & Croft, SIGIR 2007. We have no retrieval scores, so the trace's
//     own implicit-feedback tells (set size + downstream use) stand in for them.
//   - Downstream use as an implicit relevance label. Joachims, KDD 2002.
//
// The grade COMPOSES signals Episode already derives (EmptyResult, Oversized,
// Consumed*); it does not re-derive them. It is complementary to RU (the rollup's
// served-rate already credits consumption) — QPP adds the clarity/result-set
// prediction RU does not measure. Like the rest of internal/score it is a pure
// function of the event/dialogue stream: same Episode → same grade (doc.go).

// QPPGrade is the graded retrieval-quality verdict for one episode's Hop2 leg.
type QPPGrade string

const (
	// QPPLow — the served set could not answer the query: empty or error. The
	// clearest Hop2 fail, and where a human DATA-diagnosis ("it moved the file")
	// lands — the search came back empty because the data/index was stale.
	QPPLow QPPGrade = "low"
	// QPPMid — a middling set: oversized (low-clarity, broad query) or results
	// returned with no downstream-use signal. Nothing failed, nothing clearly won.
	QPPMid QPPGrade = "mid"
	// QPPHigh — a returned nug was actually used downstream: the retrieval
	// delivered the answer (implicit-feedback relevance).
	QPPHigh QPPGrade = "high"
)

// QPP is the deterministic Hop2 retrieval-quality verdict: a graded clarity/use
// score plus the hop a non-HIGH grade charges. The Hop field is the companion to
// dialogue.AttributeHop — where AttributeHop reads the human's repair MOVE, this
// reads the RESULT SET, so a retrieval-quality defect is attributed to the leg
// that produced it (HopRetrieval for an empty/oversized set; HopNone otherwise).
type QPP struct {
	Grade QPPGrade     `json:"grade"`
	Hop   dialogue.Hop `json:"hop"`
}

// QPP grades one episode's retrieval (Hop2) quality from its served result-set,
// reference-free. Precedence puts the two result-set DEFECTS (empty, oversized)
// ahead of downstream use, so a defective set is never masked by a later accept;
// a self-requery (Hop1/interp churn) is deliberately ignored — a query failure is
// not a retrieval-quality defect, and must not be charged to Hop2.
//
// CRITICAL (the "it moved the file" guard): a human diagnosing a DATA problem is
// a Hop2 retrieval fail, NOT a query fail. It surfaces here as an empty served
// set → QPPLow charged to HopRetrieval; the grade never charges HopInterp, so the
// query is never blamed for a data/index failure. Conversely a self-requery that
// ends in a used result grades QPPHigh — the retrieval was fine, the query churned.
func (e Episode) QPP() QPP {
	switch {
	case e.EmptyResult:
		// Nothing usable returned (R3a empty). Clarity floor: a query with no
		// results is maximally low-clarity (Cronen-Townsend 2002).
		return QPP{Grade: QPPLow, Hop: dialogue.HopRetrieval}
	case e.Oversized:
		// Broad / under-specified query → a diffuse, low-clarity set (R3a
		// oversized). A result-set defect that stands even if a result is later
		// used — surfaced, not masked. AttributeHop charges oversized to
		// HopRetrieval; we match it.
		return QPP{Grade: QPPMid, Hop: dialogue.HopRetrieval}
	case e.ConsumedLoose:
		// A returned nug was used downstream: implicit-feedback relevance
		// (Joachims 2002). A preceding self-requery is Hop1 churn, deliberately
		// not charged here.
		return QPP{Grade: QPPHigh, Hop: dialogue.HopNone}
	default:
		// Results returned, no downstream-use signal: reference-free QPP has no
		// positive evidence for HIGH, but nothing failed.
		return QPP{Grade: QPPMid, Hop: dialogue.HopNone}
	}
}
