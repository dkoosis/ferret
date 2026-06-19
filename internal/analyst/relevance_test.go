package analyst

import (
	"strings"
	"testing"
)

func TestBuildRelevancePromptIncludesIntentQueryAndCandidates(t *testing.T) {
	system, user := BuildRelevancePrompt(
		"where did we decide to put scorers?",
		"scorer package home decision",
		[]NugCandidate{
			{ID: "n1", Text: "scorers live in internal/score"},
			{ID: "n2", Text: "unrelated note about CI"},
		},
	)
	if !strings.Contains(system, "grade") {
		t.Errorf("system prompt should frame grading, got: %q", system)
	}
	for _, want := range []string{
		"where did we decide to put scorers?", // PROMPT
		"scorer package home decision",        // QUERY
		"n1", "scorers live in internal/score",
		"n2", "unrelated note about CI",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q\n--- user ---\n%s", want, user)
		}
	}
}

func TestParseRelevanceDecodesJudgments(t *testing.T) {
	resp := `{"judgments":[
		{"nugId":"n1","grade":3,"why":"directly names the scorer home"},
		{"nugId":"n2","grade":0,"why":"about CI, not the decision"}
	]}`
	js, err := ParseRelevance(resp)
	if err != nil {
		t.Fatalf("ParseRelevance: %v", err)
	}
	if len(js) != 2 {
		t.Fatalf("want 2 judgments, got %d", len(js))
	}
	if js[0].NugID != "n1" || js[0].Grade != GradeExact {
		t.Errorf("judgment[0] = %+v, want n1/Exact", js[0])
	}
	if js[1].Grade != GradeIrrelevant {
		t.Errorf("judgment[1].Grade = %d, want Irrelevant", js[1].Grade)
	}
}

func TestParseRelevanceToleratesFencesAndProse(t *testing.T) {
	// decodeFirstObject guardrail (ferret-001): fenced + trailing prose with a stray '}'.
	resp := "```json\n{\"judgments\":[{\"nugId\":\"x\",\"grade\":2,\"why\":\"ok\"}]}\n```\nUse `fmt {}` next."
	js, err := ParseRelevance(resp)
	if err != nil {
		t.Fatalf("ParseRelevance: %v", err)
	}
	if len(js) != 1 || js[0].NugID != "x" || js[0].Grade != GradeRelevant {
		t.Errorf("got %+v, want one x/Relevant", js)
	}
}

func TestBuildCoveragePromptIncludesPromptAndQuery(t *testing.T) {
	system, user := BuildCoveragePrompt(
		"find the loto territory rules",
		"loto lock territory",
	)
	if !strings.Contains(system, "QUERY") || !strings.Contains(system, "intent") {
		t.Errorf("coverage system prompt should frame query/intent, got: %q", system)
	}
	if !strings.Contains(user, "find the loto territory rules") || !strings.Contains(user, "loto lock territory") {
		t.Errorf("coverage user prompt missing prompt or query:\n%s", user)
	}
	// The coverage judge must NOT be shown any results — it is a pure prompt→query check.
	if strings.Contains(user, "CANDIDATES") || strings.Contains(user, "RESULTS") {
		t.Errorf("coverage prompt leaked results to a results-blind judge:\n%s", user)
	}
}

func TestParseCoverageDecodesGrade(t *testing.T) {
	cg, err := ParseCoverage(`{"grade":1,"why":"captures loto but drops territory"}`)
	if err != nil {
		t.Fatalf("ParseCoverage: %v", err)
	}
	if cg.Grade != CoveragePartial {
		t.Errorf("Grade = %d, want CoveragePartial", cg.Grade)
	}
	if cg.Why == "" {
		t.Errorf("Why should be populated")
	}
}
