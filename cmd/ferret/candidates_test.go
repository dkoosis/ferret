package main

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// TestScoreCandidate pins the composite-factor arithmetic: zero-cost tasks leak
// nothing (score 0), out-weight raises an output-heavy task toward 2×, and each
// within-task pivot bumps the thrash factor by thrashWeight.
func TestScoreCandidate(t *testing.T) {
	tests := []struct {
		name                  string
		in, out, pivots       int
		wantScore, wantWeight float64
	}{
		{"zero cost leaks nothing", 0, 0, 3, 0, 0},
		{"all input, no pivots", 100, 0, 0, 100 * (1 + 0.0) * 1, 0},    // ow=0 → 1× ; thrash 1×
		{"all output, no pivots", 0, 100, 0, 100 * (1 + 1.0) * 1, 1.0}, // ow=1 → 2×
		{"half output, no pivots", 100, 100, 0, 200 * (1 + 0.5) * 1, 0.5},
		{"half output, two pivots", 100, 100, 2, 200 * 1.5 * (1 + 0.5*2), 0.5}, // thrash 2×
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, weight := scoreCandidate(tt.in, tt.out, tt.pivots)
			if math.Abs(score-tt.wantScore) > 1e-9 {
				t.Errorf("score = %v, want %v", score, tt.wantScore)
			}
			if math.Abs(weight-tt.wantWeight) > 1e-9 {
				t.Errorf("outWeight = %v, want %v", weight, tt.wantWeight)
			}
		})
	}
}

// TestRankCandidatesExcludesNonLeaks asserts the preamble and zero-cost tasks are
// dropped (they have nothing to automate or de-context) and the survivors sort by
// score descending.
func TestRankCandidatesExcludesNonLeaks(t *testing.T) {
	res := segResult{
		Session: "s", Project: "p", OutOrphan: 7,
		Segments: []segment{
			{Index: 0, Prompt: "", FirstCall: 0, LastCall: 0, InBytes: 999},         // preamble — excluded
			{Index: 1, Prompt: "small", FirstCall: 1, LastCall: 1, InBytes: 100},    // cost 100
			{Index: 2, Prompt: "no calls", FirstCall: -1, LastCall: -1},             // zero cost — excluded
			{Index: 3, Prompt: "big out", FirstCall: 2, LastCall: 5, OutBytes: 800}, // heaviest leak
		},
	}
	got := rankCandidates(res)
	if got.Tasks != 2 {
		t.Fatalf("ranked tasks = %d, want 2 (preamble + zero-cost excluded): %+v", got.Tasks, got.Candidates)
	}
	if got.Candidates[0].Task != 3 || got.Candidates[1].Task != 1 {
		t.Errorf("order = [%d,%d], want [3,1] (score desc)", got.Candidates[0].Task, got.Candidates[1].Task)
	}
	if got.Candidates[0].Calls != 4 {
		t.Errorf("task 3 calls = %d, want 4", got.Candidates[0].Calls)
	}
	if got.TotalCost != 900 || got.TotalOut != 800 {
		t.Errorf("totals: cost=%d out=%d, want 900/800", got.TotalCost, got.TotalOut)
	}
	if got.OutOrphan != 7 {
		t.Errorf("outOrphan = %d, want 7 (carried through)", got.OutOrphan)
	}
}

func runCand(t *testing.T, lines []string, format string, top int) string {
	t.Helper()
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "s.jsonl", lines)
	var buf bytes.Buffer
	if err := candidates(&buf, root, "s", format, top); err != nil {
		t.Fatalf("candidates: %v", err)
	}
	return buf.String()
}

// TestCandidatesEndToEndJSON drives the command over a fixture and checks the
// bundle shape: ranked candidates, --top capping, and the truncated flag.
func TestCandidatesEndToEndJSON(t *testing.T) {
	out := runCand(t, segFixtureLines(), fmtJSON, 1)
	var res candResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out)
	}
	if res.Top != 1 {
		t.Errorf("top = %d, want 1", res.Top)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("shown = %d, want 1 (capped by --top)", len(res.Candidates))
	}
	if res.Tasks < 1 {
		t.Fatalf("ranked tasks = %d, want ≥1", res.Tasks)
	}
	if res.Tasks > 1 && !res.Truncated {
		t.Errorf("truncated = false but %d > top 1", res.Tasks)
	}
	// The shown candidate must be the highest-scoring one.
	top := res.Candidates[0]
	if top.Cost == 0 || top.Score == 0 {
		t.Errorf("top candidate has zero cost/score: %+v", top)
	}
}

// TestCandidatesText asserts the human rendering carries the legend and a per-task
// row with the score/cost/out columns.
func TestCandidatesText(t *testing.T) {
	out := runCand(t, segFixtureLines(), fmtText, 0)
	for _, want := range []string{"candidates session=s", "cost ×", "[task ", "score=", "ow=", "--- tasks="} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
}
