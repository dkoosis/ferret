// Package feedback is the ask-side of the in-session feedback tap (epic
// ferret-wf9): it decides WHICH moment, if any, is worth interrupting dk for a
// one-char label.
//
// The trigger is active-learning, not confidence: ask where two independent
// signals about "did this get_nug search help?" DISAGREE, because that is where a
// human label carries the most information and where the helped adjudicator (the
// thing v1 validates) is most likely wrong.
//
//   - The deterministic helped lattice (internal/score) reads BEHAVIOR — did dk
//     read the result, did a repair/accept tell follow, was the segment clean —
//     and emits helped/misled/ignored/conflict/no_signal per kind:search event.
//   - The relevance judge (internal/analyst, an LLM) reads CONTENT — were the
//     nugs the search RETURNED materially relevant to the intent — grading each
//     returned nug 0..3; SearchFit aggregates those to served/mismatch.
//
// They share no inputs (behavior vs returned-content), so a conflict is genuine
// signal, not agreement-by-construction. The pairing (decision 2026-07-25, rev:
// relevance-of-returned-nugs) supersedes the originally-ratified recalltrace-fit,
// which judged a DISJOINT retrieval path (start-of-run auto-recall emits no
// kind:search event — HyperWren/trixi confirmed), and the applicability auditor,
// which judges code-nav tool CHOICE (rg-vs-snipe), not get_nug retrieval quality.
// Relevance is the only judge on the search's own returned items, joined EXACTLY
// by nug_id (the event's returned[]{nug_id,score}).
//
// This package owns the pure decision; the live orchestration (running relevance
// on a search's returned nugs, the Stop/UserPromptSubmit hooks, budget) layers on
// top. `score` stays LLM-free — this selector sits above it and analyst, so it
// lives here, not in `internal/score`.
package feedback

import (
	"fmt"
	"strings"

	"github.com/dkoosis/ferret/internal/score"
)

// JudgeFit is the content judge's verdict on a search, restated at the feedback
// boundary so the pure selector needs no dependency on the analyst SDK: the live
// layer runs relevance and aggregates via SearchFit. FitUnknown marks "no judge
// ran / no returned nug could be graded" — nothing to disagree with, so it never
// triggers an ask.
type JudgeFit string

const (
	FitServed   JudgeFit = "served"
	FitMismatch JudgeFit = "mismatch"
	FitUnknown  JudgeFit = ""
)

// Disagree reports whether the deterministic lattice verdict and the independent
// content-relevance fit conflict about whether the get_nug search helped — the
// ask trigger — and returns a one-line reason for the audit trail.
//
// Only the two DECISIVE lattice verdicts participate: helped and misled make a
// falsifiable helpfulness claim the content judge can contradict. The
// non-committal verdicts (ignored/conflict/no_signal) assert nothing to
// disagree with, so they never trigger — asking there would spend the budget on
// a moment neither signal has an opinion about.
func Disagree(v score.Verdict, f JudgeFit) (bool, string) {
	switch {
	case v == score.VerdictHelped && f == FitMismatch:
		return true, "lattice=helped but returned nugs graded irrelevant: behavior looked helped, the search surfaced nothing materially relevant"
	case v == score.VerdictMisled && f == FitServed:
		return true, "lattice=misled but returned nugs graded relevant: behavior looked misled, the search did surface relevant content"
	default:
		return false, ""
	}
}

// Moment is the human-legible identity of the retrieval being rated — enough for
// dk to know WHICH moment from the one-liner alone (legibility is the question's
// job, not the join's). The caller sources it from the underlying retrieval
// event; the opaque SearchRef on a HelpedRecord is not something dk can place.
type Moment struct {
	Tool      string // the retrieval tool, e.g. "get_nug", "recall"
	Query     string // the query/target text, truncated for the one-liner
	TurnsBack int    // how many user turns ago the retrieval happened
}

// queryCap bounds the query text in the one-liner so a long query can't blow up
// the ask into an unreadable wall.
const queryCap = 48

// phrase renders the moment as a human clause: `get_nug "backend design" 3 turns
// back`. A missing tool or query degrades gracefully rather than emitting empty
// quotes — the ask must still point somewhere.
func (m Moment) phrase() string {
	tool := m.Tool
	if tool == "" {
		tool = "that retrieval"
	}
	var b strings.Builder
	b.WriteString(tool)
	if q := strings.TrimSpace(m.Query); q != "" {
		if len(q) > queryCap {
			q = q[:queryCap] + "…"
		}
		fmt.Fprintf(&b, " %q", q)
	}
	switch {
	case m.TurnsBack == 1:
		b.WriteString(" 1 turn back")
	case m.TurnsBack > 1:
		fmt.Fprintf(&b, " %d turns back", m.TurnsBack)
	}
	return b.String()
}

// AskCandidate is a moment the tap should ask dk about: a lattice-vs-recalltrace
// disagreement, carrying the target identity for the later join and the legible
// question dk will see. TargetRef is the search event both verdicts refer to —
// the stable key the pending-ask state carries so the answer joins back to the
// right moment.
type AskCandidate struct {
	TargetRef string `json:"target_ref"`
	SegmentID string `json:"segment_id"`
	TS        string `json:"ts"`
	Question  string `json:"question"`
	Reason    string `json:"reason"`
}

// Select decides whether a single (helped record, recalltrace fit) pair is worth
// an ask, and if so builds the candidate — target identity from the record, a
// legible target-naming question from the moment. It reports false (and the zero
// candidate) when the signals agree, so callers filter with the bool, never by
// inspecting fields.
func Select(rec score.HelpedRecord, f JudgeFit, m Moment) (AskCandidate, bool) {
	ok, reason := Disagree(rec.Verdict, f)
	if !ok {
		return AskCandidate{}, false
	}
	return AskCandidate{
		TargetRef: rec.SearchRef,
		SegmentID: rec.SegmentID,
		TS:        rec.TS,
		Question:  "That " + m.phrase() + " — did it help? [y/n]",
		Reason:    reason,
	}, true
}
