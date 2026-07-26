package feedback

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/score"
)

// TestAskCandidateJSONTags: the bank persists AskCandidate as JSON (bank.go) —
// a marshal/unmarshal roundtrip must preserve every field, which requires each
// one to carry a json tag (they were untagged before this bead's first
// serializing caller).
func TestAskCandidateJSONTags(t *testing.T) {
	want := AskCandidate{
		TargetRef: "evt-1",
		SegmentID: "seg-1",
		TS:        "2026-07-25T15:00:00Z",
		Question:  "did it help? [y/n]",
		Reason:    "lattice=helped but returned nugs graded irrelevant",
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got AskCandidate
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("roundtrip = %+v, want %+v", got, want)
	}
	for _, field := range []string{"target_ref", "segment_id", "ts", "question", "reason"} {
		if !strings.Contains(string(b), `"`+field+`"`) {
			t.Errorf("marshaled JSON missing tagged field %q: %s", field, b)
		}
	}
}

// TestDisagree pins the ratified trigger table: an ask fires ONLY when the
// behavioral lattice and the content judge make opposite helpfulness claims.
// Agreement, and any non-committal lattice verdict, must stay silent — the
// budget is scarce and only a genuine conflict earns an interruption.
func TestDisagree(t *testing.T) {
	cases := []struct {
		name string
		v    score.Verdict
		f    JudgeFit
		want bool
	}{
		{"helped vs mismatch → ask", score.VerdictHelped, FitMismatch, true},
		{"misled vs served → ask", score.VerdictMisled, FitServed, true},
		{"helped vs served → agree, silent", score.VerdictHelped, FitServed, false},
		{"misled vs mismatch → agree, silent", score.VerdictMisled, FitMismatch, false},
		{"helped, no judge → silent", score.VerdictHelped, FitUnknown, false},
		{"ignored vs mismatch → non-committal, silent", score.VerdictIgnored, FitMismatch, false},
		{"conflict vs served → non-committal, silent", score.VerdictConflict, FitServed, false},
		{"no_signal vs mismatch → non-committal, silent", score.VerdictNoSignal, FitMismatch, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := Disagree(c.v, c.f)
			if got != c.want {
				t.Errorf("Disagree(%v,%v) = %v, want %v", c.v, c.f, got, c.want)
			}
			if got && reason == "" {
				t.Error("a triggering disagreement must carry a non-empty reason for the audit trail")
			}
			if !got && reason != "" {
				t.Errorf("a non-trigger must carry no reason, got %q", reason)
			}
		})
	}
}

// TestSelectBuildsCandidateOnDisagreement: a disagreement yields a candidate that
// carries the target identity from the record (so the answer joins back) and a
// question that NAMES the moment (so dk knows what he's rating from the one-liner
// alone — legibility is the question's job).
func TestSelectBuildsCandidateOnDisagreement(t *testing.T) {
	rec := score.HelpedRecord{
		Verdict:   score.VerdictHelped,
		SessionID: "s1",
		SearchRef: "evt-42",
		SegmentID: "seg-7",
		TS:        "2026-07-25T15:00:00Z",
	}
	m := Moment{Tool: "get_nug", Query: "memory backend design"}

	c, ok := Select(rec, FitMismatch, m)
	if !ok {
		t.Fatal("Select must fire on a helped-vs-mismatch disagreement")
	}
	if c.TargetRef != "evt-42" || c.SegmentID != "seg-7" || c.TS != rec.TS {
		t.Errorf("candidate lost target identity: %+v", c)
	}
	for _, want := range []string{"get_nug", "memory backend design", "earlier", "[y/n]"} {
		if !strings.Contains(c.Question, want) {
			t.Errorf("question %q must name the moment (missing %q)", c.Question, want)
		}
	}
	if c.Reason == "" {
		t.Error("candidate must carry the disagreement reason")
	}
}

// TestSelectSilentOnAgreement: when the signals agree, Select reports false and
// the zero candidate — callers filter on the bool, never on fields.
func TestSelectSilentOnAgreement(t *testing.T) {
	rec := score.HelpedRecord{Verdict: score.VerdictHelped, SearchRef: "evt-1"}
	if c, ok := Select(rec, FitServed, Moment{Tool: "recall"}); ok {
		t.Errorf("Select must stay silent when signals agree, got %+v", c)
	}
}

// TestMomentPhraseDegradesGracefully: a missing tool or query must still point
// somewhere — an empty tool becomes "that retrieval", a missing query drops the
// quotes rather than emitting `""`. The temporal cue stays vague ("earlier") and
// never carries a numeric turn count — an exact count would be stale by inject
// time (ferret-162), so the phrase must never emit "turn(s) back".
func TestMomentPhraseDegradesGracefully(t *testing.T) {
	cases := []struct {
		m    Moment
		want []string // substrings that must appear
		not  []string // substrings that must not
	}{
		{Moment{Tool: "", Query: ""}, []string{"that retrieval", "earlier"}, []string{`""`, "turns back", "turn back"}},
		{Moment{Tool: "recall", Query: ""}, []string{"recall", "earlier"}, []string{`""`, "turns back", "turn back"}},
		{Moment{Tool: "get_nug", Query: "x"}, []string{"get_nug", `"x"`, "earlier"}, []string{"turns back", "turn back"}},
	}
	for _, c := range cases {
		got := c.m.phrase()
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("phrase(%+v) = %q, must contain %q", c.m, got, w)
			}
		}
		for _, n := range c.not {
			if strings.Contains(got, n) {
				t.Errorf("phrase(%+v) = %q, must NOT contain %q", c.m, got, n)
			}
		}
	}
}

// TestMomentPhraseTruncatesLongQuery: a long query is capped so the one-liner
// stays readable rather than ballooning into a wall.
func TestMomentPhraseTruncatesLongQuery(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := Moment{Tool: "recall", Query: long}.phrase()
	if len(got) > queryCap+40 { // tool + quotes + ellipsis overhead
		t.Errorf("phrase did not cap a long query: len=%d, %q", len(got), got)
	}
	if !strings.Contains(got, "…") {
		t.Error("a truncated query must show an ellipsis")
	}
}
