package analyst

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// maxTokensDoer stubs the Anthropic transport with a well-formed Messages
// response whose stop_reason is max_tokens and whose usage carries real token
// counts — the truncation case complete() turns into ErrTruncatedResponse.
type maxTokensDoer struct{}

func (maxTokensDoer) Do(req *http.Request) (*http.Response, error) {
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"claude",` +
		`"content":[{"type":"text","text":"{\"grade\""}],"stop_reason":"max_tokens",` +
		`"usage":{"input_tokens":420,"output_tokens":8000}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}, nil
}

// TestRunCoveragePreservesUsageOnTruncation pins the PR #64 fix: when a coverage
// judge call truncates at max_tokens, RunCoverage must return the paid call's
// real token usage (not Usage{}), so analyst.Hop1's burn accounting stays honest.
func TestRunCoveragePreservesUsageOnTruncation(t *testing.T) {
	cfg := Config{APIKey: "sk-test", HTTPClient: maxTokensDoer{}}
	_, usage, err := RunCoverage(context.Background(), cfg, "ep-1", "prompt text", "query text")
	if !errors.Is(err, ErrTruncatedResponse) {
		t.Fatalf("err = %v; want ErrTruncatedResponse", err)
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		t.Errorf("usage dropped on truncation: %+v; want the call's real token counts", usage)
	}
}

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
