package score

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/retrievalevent"
)

// The `helped` adjudicator — ferret-bbp.14, decision nug f7977bfc1afa.
//
// Hop1 (internal/analyst/hop1.go, "was the query a faithful read of intent?")
// and Hop2 (qpp.go, "was the served result set good?") both answer whether
// the SEARCH was good. Neither answers whether the search HELPED THE TASK — a
// search can grade high on both hops and never help (surfaced but never
// opened, task abandoned anyway); it can grade low and still help (a lucky
// in-band hit). `helped` is the outcome leg: a deterministic, zero-spend (no
// LLM in v1 — the C-slot below is reserved, not built) precedence lattice
// composing three signals ferret already computes or receives:
//
//  1. read-linkage    — did a kind:read row resolve back to this kind:search
//     row via search_ref (internal/retrievalevent)?
//  2. episode tell     — the owning segment's dialogue.Outcome (the human's
//     own words: accept / repair-heavy / abandoned / unknown). PARADISE
//     task-success leg, Walker et al. 1997 — same borrowed method
//     internal/dialogue/episode.go already cites.
//  3. segment outcome  — the weak terminal-VCS-action label (score.Outcome,
//     outcome.go): nil is silence, never a negative signal.
//
// D2 (the ratified table) is encoded below as DATA (the `lattice` slice), not
// scattered branch logic, so `rulesHash` — hashed once over the table's own
// serialization — changes if and only if the table changes (AC3). Adjudicate
// implements the table's precedence order directly; see the comment above it
// for the row-by-row cross-reference.

// Verdict is the 5-value adjudication enum (D1).
type Verdict string

const (
	VerdictHelped   Verdict = "helped"
	VerdictIgnored  Verdict = "ignored"
	VerdictMisled   Verdict = "misled"
	VerdictConflict Verdict = "conflict"
	VerdictNoSignal Verdict = "no_signal"
)

// judgeAdjudicator and judgeScheme name this adjudicator + scheme in every
// fingerprint it stamps (D4).
const (
	judgeAdjudicator = "helped"
	judgeScheme      = "lattice-v1"
)

// JudgeFingerprint is the structured provenance record every helped verdict
// carries (D4): which adjudicator, which scheme, a hash of the rules that
// produced it, and which model, if any, was consulted (nil = purely
// deterministic verdict). Defined here (not in internal/analyst) so Hop1 can
// reuse the same shape without a package cycle (analyst already imports
// score); Hop1 stamps its own fingerprint (ferret-bbp.15).
type JudgeFingerprint struct {
	Adjudicator string          `json:"adjudicator"`
	Scheme      string          `json:"scheme"`
	RulesHash   string          `json:"rules_hash"`
	LLM         *LLMFingerprint `json:"llm"`
}

// LLMFingerprint identifies the paid judge behind a fingerprint's LLM leg:
// the model consulted and a hash of the exact system prompt it ran under, so
// a replay can tell two prompt revisions apart even on the same model.
type LLMFingerprint struct {
	Model      string `json:"model"`
	PromptHash string `json:"prompt_hash"`
}

// helpedFingerprint is the one JudgeFingerprint every helped verdict stamps
// (LLM always nil — v1 has no staged-LLM path).
var helpedFingerprint = JudgeFingerprint{
	Adjudicator: judgeAdjudicator,
	Scheme:      judgeScheme,
	RulesHash:   rulesHash,
	LLM:         nil,
}

// latticeRow is one row of the D2 table, as data. ReadLinkage/Tell/SegOutcome
// use the open-vocabulary tokens below rather than the leg types Adjudicate
// consumes, because "any"/"none" appear as wildcards in the ratified table
// and a real leg type has no such value.
type latticeRow struct {
	ReadLinkage string
	Tell        string
	SegOutcome  string
	Verdict     Verdict
}

// lattice IS the D2 table (decision nug f7977bfc1afa), verbatim:
//
//	read-linkage | tell                      | segment outcome | verdict
//	read         | accept/clean              | clean           | helped
//	read         | repair/abandon (adjacent) | any             | misled
//	read         | none                      | clean           | helped
//	no read      | accept-tell               | any             | conflict  (⚑)
//	no read      | none                      | clean           | ignored
//	no read      | repair/abandon            | any             | ignored
//	any          | conflicting tells         | any             | conflict
//	none         | none                      | none            | no_signal
//
// rulesHash is a sha256 over this table's canonical serialization —
// modifying any row (adding, removing, reordering, or changing a value)
// moves the hash, which is exactly AC3's "changes iff the table changes".
// tokenNone is the "no signal" wildcard token shared by all three lattice
// columns (goconst: one constant, not five inline literals).
const tokenNone = "none"

var lattice = []latticeRow{
	{"read", "accept", "clean", VerdictHelped},
	{"read", "repair-abandon-adjacent", "any", VerdictMisled},
	{"read", tokenNone, "clean", VerdictHelped},
	{"no-read", "accept", "any", VerdictConflict},
	{"no-read", tokenNone, "clean", VerdictIgnored},
	{"no-read", "repair-abandon", "any", VerdictIgnored},
	{"any", "conflicting", "any", VerdictConflict},
	{tokenNone, tokenNone, tokenNone, VerdictNoSignal},
}

// rulesHash is computed once at package init from the lattice literal above.
var rulesHash = computeRulesHash()

func computeRulesHash() string {
	var sb strings.Builder
	for _, r := range lattice {
		fmt.Fprintf(&sb, "%s|%s|%s|%s\n", r.ReadLinkage, r.Tell, r.SegOutcome, r.Verdict)
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:16])
}

// Leg is the caller-supplied episode-side evidence for one retrieval event,
// keyed by the search row's EventID. Adjudicate takes it as an input rather than
// reaching for the transcript itself, so it stays a pure function testable
// against the vendored fixture. The live join that derives Legs from a real
// session — ts→segment interval containment for Tell/SegOutcome (bbp.16), read-
// adjacency for RepairAdjacent (bbp.17) — lives in JoinLegs (join.go) + the
// cmd `helped` path, feeding this exact shape.
type Leg struct {
	// SegmentID is the owning task segment's identifier (AC4: every emitted
	// record must carry one).
	SegmentID string
	// Tell is the owning segment's dialogue.Outcome — the human's own words,
	// not the tool sequence (PARADISE task-success leg, Walker et al. 1997).
	Tell dialogue.Outcome
	// SegOutcome is the segment's weak terminal-action label (nil = silence,
	// never a negative signal — see outcome.go).
	SegOutcome *Outcome
	// RepairAdjacent reports whether a repair/abandon move sits immediately
	// adjacent to the read that resolved this search. Only a repair the
	// human made *right after* opening the result should indict the search
	// itself (misled); a repair elsewhere in the segment is unrelated churn.
	// In the live join (bbp.17) an adjacent repair also SUPPLIES this leg's
	// Tell — the human's direct reaction to this retrieval — so misled fires on
	// a clean search→pushback, not only inside an already-repair-heavy segment.
	RepairAdjacent bool
}

// isRepairAbandon reports whether a dialogue.Outcome reads as repair/abandon
// friction for lattice purposes (v2's repair-heavy/abandoned pair).
func isRepairAbandon(o dialogue.Outcome) bool {
	return o == dialogue.OutcomeRepairHeavy || o == dialogue.OutcomeAbandoned
}

// tellIsNone reports a tell with no signal — dialogue.OutcomeUnknown, or the
// Go zero value "" a caller-supplied Leg map defaults to when a search event
// has no entry (AdjudicateEvents never fabricates an OutcomeUnknown for a
// missing leg, so both must read as "no tell" here).
func tellIsNone(o dialogue.Outcome) bool {
	return o == dialogue.OutcomeUnknown || o == ""
}

// isConflictingTells reports the cross-leg disagreement half of "conflicting
// tells" (open question 3): the segment SHIPPED a terminal VCS artifact
// (SegOutcome.Positive) while the human's own words say the task was
// abandoned or repair-heavy. Two ferret-derived signals about the SAME task
// disagreeing is exactly the case the conflict bucket exists to flag for a
// future human/LLM adjudication pass (the C-slot, reserved not built) rather
// than silently pick a side.
func isConflictingTells(leg Leg) bool {
	return isRepairAbandon(leg.Tell) && leg.SegOutcome != nil && leg.SegOutcome.Positive
}

// Adjudicate applies the D2 lattice's precedence order to one retrieval
// event's linkage + leg, row references below to the `lattice` table:
//
//  1. no legs at all (row 8: none/none/none)                → no_signal
//  2. conflicting tells, any linkage (row 7 + open-q3(b))    → conflict
//  3. linked + repair/abandon adjacent to the read (row 2)   → misled
//  4. linked + anything else (rows 1, 3 collapse to one arm) → helped
//  5. not linked + accept-tell (row 4, the ⚑ regression)     → conflict
//  6. not linked + anything else (rows 5, 6 collapse)        → ignored
//
// Rows 1/3 and rows 5/6 collapse into single arms deliberately: the ratified
// table's "clean" segment-outcome qualifier only ever widens which linked/
// non-adjacent or non-read/non-accept cases fire, never narrows the verdict
// they land on — so the precedence order below is behavior-identical to a
// literal 8-way match while staying readable. helped_test.go's table-driven
// case enumerates all 8 rows directly against this function.
func Adjudicate(linked bool, leg Leg) Verdict {
	noLegs := !linked && tellIsNone(leg.Tell) && leg.SegOutcome == nil
	switch {
	case noLegs:
		return VerdictNoSignal
	case isConflictingTells(leg):
		return VerdictConflict
	case linked && isRepairAbandon(leg.Tell) && leg.RepairAdjacent:
		return VerdictMisled
	case linked:
		return VerdictHelped
	case leg.Tell == dialogue.OutcomeSuccess:
		// ⚑ no read + accept-tell: the human says it helped but nothing was
		// ever opened. Trust the human's words over the plumbing's silence —
		// don't downgrade to `ignored` and don't invent a `helped` the
		// linkage can't support. Surface it for adjudication (bbp thesis:
		// plumbing must not override the human's words).
		return VerdictConflict
	default:
		return VerdictIgnored
	}
}

// HelpedRecord is one adjudicated verdict for one retrieval event (D3: one
// verdict per event, carrying segment_id + agent_type). SearchRef is the
// search event's own EventID — the value a linked read row's search_ref
// would point at — folded into the key alongside session_id/agent_id/ts so a
// same-clock-tick collision (the reason the producer's own EventID exists)
// can't merge two distinct events.
type HelpedRecord struct {
	Verdict   Verdict `json:"verdict"`
	SessionID string  `json:"session_id"`
	AgentID   string  `json:"agent_id"`
	AgentType string  `json:"agent_type"`
	TS        string  `json:"ts"`
	SearchRef string  `json:"search_ref"`
	SegmentID string  `json:"segment_id"`
	// Correlational flags that this record's verdict is an inference from
	// adjacency, not direct evidence the search caused the repair (only ever
	// true for misled — the lattice's one verdict built on a temporal
	// coincidence rather than a direct tell/linkage read).
	Correlational    bool             `json:"correlational"`
	JudgeFingerprint JudgeFingerprint `json:"judge_fingerprint"`
}

// AdjudicateEvents drives Adjudicate over every kind:search row in events,
// resolving each one's read-linkage via retrievalevent.LinkReads and its
// episode-side evidence via legs (keyed by the search row's EventID; a
// search with no entry gets the zero Leg, i.e. no tell/segment-outcome
// signal). Session-fallback rows (contract Trap 2: agent_id degraded to
// session_id) are excluded from the returned records — filtered, not
// mis-keyed — and counted separately so a caller can report the drop instead
// of losing it silently.
//
// Output is sorted by (ts, agent_id, event_id) for determinism: no map
// iteration reaches the output order (mirrors doc.go's byte-stability
// contract).
func AdjudicateEvents(events []retrievalevent.Event, legs map[string]Leg) (records []HelpedRecord, filtered int) {
	links := retrievalevent.LinkReads(events)

	var searches []retrievalevent.Event
	for i := range events {
		if events[i].Kind != retrievalevent.KindSearch {
			continue
		}
		if events[i].Attribution == retrievalevent.AttributionSessionFallback {
			filtered++
			continue
		}
		searches = append(searches, events[i])
	}

	sort.Slice(searches, func(i, j int) bool {
		a, b := searches[i], searches[j]
		if a.TS != b.TS {
			return a.TS < b.TS
		}
		if a.AgentID != b.AgentID {
			return a.AgentID < b.AgentID
		}
		return a.EventID < b.EventID
	})

	records = make([]HelpedRecord, 0, len(searches))
	for i := range searches {
		e := &searches[i]
		linked := len(links[e.EventID]) > 0
		leg := legs[e.EventID] // zero value if absent: no tell/outcome signal
		verdict := Adjudicate(linked, leg)
		records = append(records, HelpedRecord{
			Verdict:          verdict,
			SessionID:        e.SessionID,
			AgentID:          e.AgentID,
			AgentType:        e.AgentType,
			TS:               e.TS,
			SearchRef:        e.EventID,
			SegmentID:        leg.SegmentID,
			Correlational:    verdict == VerdictMisled,
			JudgeFingerprint: helpedFingerprint,
		})
	}
	return records, filtered
}
