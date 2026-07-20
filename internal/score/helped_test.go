package score

import (
	"encoding/json"
	"testing"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/retrievalevent"
)

// TestAdjudicateLattice is the unit table for the D2 lattice: one case per
// row of the ratified table (helped.go's `lattice` var), asserted directly
// against Adjudicate so a change to either drifts the other into a failing
// test. Includes the ⚑ regression (no-read + accept-tell → conflict, NOT
// ignored) and the misled-adjacency pair (a repair immediately after the
// read misleads; a repair elsewhere in the segment does not).
func TestAdjudicateLattice(t *testing.T) {
	positive := &Outcome{Positive: true, Signal: "sh:git_commit"}

	cases := []struct {
		name   string
		linked bool
		leg    Leg
		want   Verdict
	}{
		{
			name:   "row1: read + accept + clean -> helped",
			linked: true,
			leg:    Leg{Tell: dialogue.OutcomeSuccess, SegOutcome: positive},
			want:   VerdictHelped,
		},
		{
			name:   "row2: read + repair adjacent -> misled",
			linked: true,
			leg:    Leg{Tell: dialogue.OutcomeRepairHeavy, RepairAdjacent: true},
			want:   VerdictMisled,
		},
		{
			name:   "row2b: read + abandoned adjacent -> misled",
			linked: true,
			leg:    Leg{Tell: dialogue.OutcomeAbandoned, RepairAdjacent: true},
			want:   VerdictMisled,
		},
		{
			name:   "row2 companion: read + repair NOT adjacent -> helped, not misled",
			linked: true,
			leg:    Leg{Tell: dialogue.OutcomeRepairHeavy, RepairAdjacent: false},
			want:   VerdictHelped,
		},
		{
			name:   "row3: read + no tell + clean -> helped",
			linked: true,
			leg:    Leg{Tell: dialogue.OutcomeUnknown, SegOutcome: positive},
			want:   VerdictHelped,
		},
		{
			name:   "row4 (flag): no read + accept-tell -> conflict, NOT ignored",
			linked: false,
			leg:    Leg{Tell: dialogue.OutcomeSuccess},
			want:   VerdictConflict,
		},
		{
			name:   "row5: no read + none + clean -> ignored",
			linked: false,
			leg:    Leg{Tell: dialogue.OutcomeUnknown, SegOutcome: positive},
			want:   VerdictIgnored,
		},
		{
			name:   "row6: no read + repair/abandon -> ignored",
			linked: false,
			leg:    Leg{Tell: dialogue.OutcomeAbandoned},
			want:   VerdictIgnored,
		},
		{
			name:   "row7: conflicting tells (segOutcome positive vs tell abandoned) -> conflict, even when linked",
			linked: true,
			leg:    Leg{Tell: dialogue.OutcomeAbandoned, SegOutcome: positive, RepairAdjacent: true},
			want:   VerdictConflict,
		},
		{
			name:   "row7b: conflicting tells, no read -> conflict",
			linked: false,
			leg:    Leg{Tell: dialogue.OutcomeRepairHeavy, SegOutcome: positive},
			want:   VerdictConflict,
		},
		{
			name:   "row8: no legs at all -> no_signal",
			linked: false,
			leg:    Leg{},
			want:   VerdictNoSignal,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Adjudicate(c.linked, c.leg)
			if got != c.want {
				t.Errorf("Adjudicate(%v, %+v) = %s, want %s", c.linked, c.leg, got, c.want)
			}
		})
	}
}

// TestRulesHashPin fails if the lattice table literal changes without the
// implementer noticing — proof of AC3's "rules_hash changes iff the table
// changes". Recompute this constant (see helped.go's computeRulesHash) and
// update it deliberately whenever the lattice is intentionally revised.
func TestRulesHashPin(t *testing.T) {
	const wantHash = "a779a920fcb95d8ce2153849e02a0288"
	if rulesHash != wantHash {
		t.Errorf("rulesHash = %s, want %s (lattice table changed — if intentional, update wantHash)", rulesHash, wantHash)
	}
	if helpedFingerprint.RulesHash != rulesHash {
		t.Errorf("helpedFingerprint.RulesHash = %s, want %s", helpedFingerprint.RulesHash, rulesHash)
	}
}

// --- Step 5: composed golden replay -----------------------------------

// syntheticLegs attaches ferret-authored episode-side evidence to the
// vendored producer fixture's search rows, keyed by event_id. The fixture
// itself carries only the read-linkage leg (10 search + 2 read rows, no
// tells or segment outcomes); dialed to hit every lattice verdict:
//
//   - 1a2b3c4d5e6f7081 (read, "loto lock semantics") — accept tell, clean
//     outcome -> helped
//   - 3c4d5e6f70819203 (no read, "worktree gc") — accept tell -> conflict ⚑
//   - 4d5e6f7081920314 (no read, "asdfqwer nonsense") — no tell, no outcome
//     -> no_signal
//   - 5e6f708192031425 (read, "kong root migration") — repair-heavy tell,
//     adjacent -> misled
//   - 708192031425364a (no read, "pipe_buf atomic append") — repair-heavy
//     tell -> ignored
//   - 92031425364a5b6c (no read, "embedding coverage") — abandoned tell +
//     positive segment outcome -> conflict (cross-leg disagreement)
//   - 031425364a5b6c7d (no read, "database is locked") — no tell, no
//     outcome -> no_signal
//   - 1425364a5b6c7d8e (no read, direct-id "a55d63b61ddb") — no tell,
//     clean outcome -> ignored
//   - 25364a5b6c7d8e9f (no read, direct-name "dk-goals") — repair tell, NOT
//     adjacent (it's a no-read row so adjacency is moot) -> ignored
//
// 8192031425364a5b (session-fallback) intentionally has no entry — it's
// filtered before legs are consulted.
func syntheticLegs() map[string]Leg {
	positive := &Outcome{Positive: true, Signal: "sh:git_commit"}
	return map[string]Leg{
		"1a2b3c4d5e6f7081": {SegmentID: "seg-1", Tell: dialogue.OutcomeSuccess, SegOutcome: positive},
		"3c4d5e6f70819203": {SegmentID: "seg-2", Tell: dialogue.OutcomeSuccess},
		"4d5e6f7081920314": {SegmentID: "seg-3"},
		"5e6f708192031425": {SegmentID: "seg-4", Tell: dialogue.OutcomeRepairHeavy, RepairAdjacent: true},
		"708192031425364a": {SegmentID: "seg-5", Tell: dialogue.OutcomeRepairHeavy},
		"92031425364a5b6c": {SegmentID: "seg-6", Tell: dialogue.OutcomeAbandoned, SegOutcome: positive},
		"031425364a5b6c7d": {SegmentID: "seg-7"},
		"1425364a5b6c7d8e": {SegmentID: "seg-8", SegOutcome: positive},
		"25364a5b6c7d8e9f": {SegmentID: "seg-9", Tell: dialogue.OutcomeRepairHeavy},
	}
}

// TestAdjudicateEventsGoldenReplay is the end-to-end contract test: it loads
// the vendored producer fixture, joins read->search, attaches the synthetic
// legs above, and asserts the full AC surface — one keyed record per
// non-fallback retrieval event, every record carries segment_id + agent_type
// and the pinned judge_fingerprint, the fallback row is filtered (not
// mis-keyed), and two runs are byte-identical (determinism).
func TestAdjudicateEventsGoldenReplay(t *testing.T) {
	events, err := retrievalevent.ReadEvents("../retrievalevent/testdata/retrieval-golden.jsonl")
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	legs := syntheticLegs()

	records, filtered := AdjudicateEvents(events, legs)

	if filtered != 1 {
		t.Errorf("filtered = %d, want 1 (the session-fallback row)", filtered)
	}
	if len(records) != 9 {
		t.Fatalf("len(records) = %d, want 9 (10 search rows - 1 fallback)", len(records))
	}

	seenKeys := make(map[string]bool)
	for _, r := range records {
		if r.SegmentID == "" {
			t.Errorf("record %s has empty segment_id", r.SearchRef)
		}
		if r.AgentType == "" {
			t.Errorf("record %s has empty agent_type", r.SearchRef)
		}
		if r.JudgeFingerprint != helpedFingerprint {
			t.Errorf("record %s judge_fingerprint = %+v, want %+v", r.SearchRef, r.JudgeFingerprint, helpedFingerprint)
		}
		key := r.SessionID + "|" + r.AgentID + "|" + r.TS + "|" + r.SearchRef
		if seenKeys[key] {
			t.Errorf("duplicate key %s", key)
		}
		seenKeys[key] = true
	}

	want := map[string]Verdict{
		"1a2b3c4d5e6f7081": VerdictHelped,
		"3c4d5e6f70819203": VerdictConflict,
		"4d5e6f7081920314": VerdictNoSignal,
		"5e6f708192031425": VerdictMisled,
		"708192031425364a": VerdictIgnored,
		"92031425364a5b6c": VerdictConflict,
		"031425364a5b6c7d": VerdictNoSignal,
		"1425364a5b6c7d8e": VerdictIgnored,
		"25364a5b6c7d8e9f": VerdictIgnored,
	}
	got := make(map[string]Verdict, len(records))
	for _, r := range records {
		got[r.SearchRef] = r.Verdict
		if r.SearchRef == "5e6f708192031425" && !r.Correlational {
			t.Errorf("misled record %s should carry the correlational caveat", r.SearchRef)
		}
		if r.Verdict != VerdictMisled && r.Correlational {
			t.Errorf("record %s verdict=%s should not carry the correlational caveat", r.SearchRef, r.Verdict)
		}
	}
	for eventID, wantVerdict := range want {
		if got[eventID] != wantVerdict {
			t.Errorf("event %s verdict = %s, want %s", eventID, got[eventID], wantVerdict)
		}
	}

	// determinism: two full runs produce byte-identical output.
	first := mustMarshal(t, records)
	records2, filtered2 := AdjudicateEvents(events, legs)
	if filtered2 != filtered {
		t.Errorf("second run filtered = %d, want %d", filtered2, filtered)
	}
	second := mustMarshal(t, records2)
	if first != second {
		t.Fatalf("AdjudicateEvents not byte-stable:\nfirst  %s\nsecond %s", first, second)
	}
}

// TestJudgeFingerprintJSON pins the on-wire shape of the fingerprint (D4):
// adjudicator, scheme, rules_hash, and an explicit null llm (not an omitted
// field — a consumer must be able to tell "no LLM was consulted" from "this
// producer doesn't know the field").
func TestJudgeFingerprintJSON(t *testing.T) {
	b, err := json.Marshal(helpedFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["adjudicator"] != "helped" {
		t.Errorf("adjudicator = %v, want helped", got["adjudicator"])
	}
	if got["scheme"] != "lattice-v1" {
		t.Errorf("scheme = %v, want lattice-v1", got["scheme"])
	}
	if _, ok := got["llm"]; !ok {
		t.Error(`"llm" key missing — want explicit null, not omitted`)
	} else if got["llm"] != nil {
		t.Errorf("llm = %v, want null", got["llm"])
	}
	if got["rules_hash"] == "" {
		t.Error("rules_hash is empty")
	}
}
