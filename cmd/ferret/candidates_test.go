package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/conform"
	"github.com/dkoosis/ferret/internal/score"
)

// TestScoreCandidate pins the composite-factor arithmetic: zero-cost tasks leak
// nothing (score 0), out-weight raises an output-heavy task toward 2×, each
// within-task pivot bumps the thrash factor by thrashWeight, and a supplied
// conformance verdict folds in 1 + (1 − fitness×precision) — nil leaves it at 1×.
func TestScoreCandidate(t *testing.T) {
	tests := []struct {
		name                              string
		in, out, pivots                   int
		conf                              *conform.Result
		wantScore, wantWeight, wantFactor float64
	}{
		{"zero cost leaks nothing", 0, 0, 3, nil, 0, 0, 1},
		{"all input, no pivots", 100, 0, 0, nil, 100 * (1 + 0.0) * 1, 0, 1},    // ow=0 → 1× ; thrash 1×
		{"all output, no pivots", 0, 100, 0, nil, 100 * (1 + 1.0) * 1, 1.0, 1}, // ow=1 → 2×
		{"half output, no pivots", 100, 100, 0, nil, 200 * (1 + 0.5) * 1, 0.5, 1},
		{"half output, two pivots", 100, 100, 2, nil, 200 * 1.5 * (1 + 0.5*2), 0.5, 1}, // thrash 2×
		// conformance folds in as the 4th factor: perfect → 1× (no change); a
		// half-conformant task (q = 0.5×0.5 = 0.25 → factor 1.75) raises the leak.
		{"perfect conformance is neutral", 100, 0, 0, &conform.Result{Fitness: 1, Precision: 1}, 100 * 1 * 1 * 1, 0, 1},
		{"off-plan conformance raises leak", 100, 100, 0, &conform.Result{Fitness: 0.5, Precision: 0.5}, 200 * 1.5 * 1 * 1.75, 0.5, 1.75},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, weight, factor := scoreCandidate(tt.in, tt.out, tt.pivots, tt.conf)
			if math.Abs(score-tt.wantScore) > 1e-9 {
				t.Errorf("score = %v, want %v", score, tt.wantScore)
			}
			if math.Abs(weight-tt.wantWeight) > 1e-9 {
				t.Errorf("outWeight = %v, want %v", weight, tt.wantWeight)
			}
			if math.Abs(factor-tt.wantFactor) > 1e-9 {
				t.Errorf("confFactor = %v, want %v", factor, tt.wantFactor)
			}
		})
	}
}

// TestRankCandidatesExcludesNonLeaks asserts the preamble and zero-cost tasks are
// dropped (they have nothing to automate or de-context) and the survivors sort by
// score descending.
func TestRankCandidatesExcludesNonLeaks(t *testing.T) {
	res := score.Result{
		Session: "s", Project: "p", OutOrphan: 7,
		Segments: []score.Segment{
			{Index: 0, Prompt: "", FirstCall: 0, LastCall: 0, InBytes: 999},         // preamble — excluded
			{Index: 1, Prompt: "small", FirstCall: 1, LastCall: 1, InBytes: 100},    // cost 100
			{Index: 2, Prompt: "no calls", FirstCall: -1, LastCall: -1},             // zero cost — excluded
			{Index: 3, Prompt: "big out", FirstCall: 2, LastCall: 5, OutBytes: 800}, // heaviest leak
		},
	}
	got := rankCandidates(res, nil)
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

// TestRankCandidatesConformance pins the kuv.4 join (ferret-567.2): a task with a
// supplied off-plan reference plan folds conformance in as a 4th factor > 1 (score =
// reference-free base × factor), the verdict is surfaced on the row, and a task with
// no spec keeps a nil conformance and the reference-free rank.
func TestRankCandidatesConformance(t *testing.T) {
	res := score.Result{
		Session: "s", Project: "p",
		Segments: []score.Segment{
			{Index: 1, Prompt: "planned", FirstCall: 0, LastCall: 1, InBytes: 100, OutBytes: 100},
			{Index: 2, Prompt: "no plan", FirstCall: 2, LastCall: 2, InBytes: 100, OutBytes: 100},
		},
	}
	specs := map[int]candConformSpec{
		// task 1: one planned step served, one off-plan call → precision < 1.
		1: {Task: 1, Reference: []string{"read", "edit"}, Observed: []conform.ObsCall{
			{Call: 0, Tool: "Read", Step: "read"},
			{Call: 1, Tool: "Bash", Step: ""},
		}},
		// task 3 doesn't exist and the reference is empty: both must be no-ops.
		3: {Task: 3, Reference: nil},
	}
	got := rankCandidates(res, specs)

	var t1, t2 *candidate
	for i := range got.Candidates {
		switch got.Candidates[i].Task {
		case 1:
			t1 = &got.Candidates[i]
		case 2:
			t2 = &got.Candidates[i]
		}
	}
	if t1 == nil || t2 == nil {
		t.Fatalf("want tasks 1 and 2 ranked, got %+v", got.Candidates)
	}
	if t2.Conformance != nil {
		t.Errorf("task 2 has no spec → conformance must be nil, got %+v", t2.Conformance)
	}
	if t1.Conformance == nil {
		t.Fatal("task 1 has a spec → conformance must be populated")
	}
	if t1.Conformance.Factor <= 1 {
		t.Errorf("off-plan factor = %v, want > 1 (a leak penalty)", t1.Conformance.Factor)
	}
	wantFactor := 1 + (1 - t1.Conformance.Fitness*t1.Conformance.Precision)
	if math.Abs(t1.Conformance.Factor-wantFactor) > 1e-9 {
		t.Errorf("factor = %v, want 1+(1-fit×prec) = %v", t1.Conformance.Factor, wantFactor)
	}
	base, _, _ := scoreCandidate(t1.InBytes, t1.OutBytes, t1.Pivots, nil)
	if wantScore := base * t1.Conformance.Factor; math.Abs(t1.Score-wantScore) > 1e-9 {
		t.Errorf("task 1 score = %v, want reference-free base × factor = %v", t1.Score, wantScore)
	}
}

// TestReadCandConformSpecs covers the side-input loader: an empty path is the
// reference-free default (nil map), and a JSON array is keyed by task index.
func TestReadCandConformSpecs(t *testing.T) {
	if got, err := readCandConformSpecs(""); err != nil || len(got) != 0 {
		t.Fatalf("empty path: got (%v, %v), want (empty map, nil)", got, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "conf.json")
	body := `[{"task":2,"reference":["plan"],"observed":[{"call":0,"tool":"Read","step":"plan"}]}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	specs, err := readCandConformSpecs(path)
	if err != nil {
		t.Fatalf("readCandConformSpecs: %v", err)
	}
	if len(specs) != 1 || len(specs[2].Reference) != 1 || specs[2].Reference[0] != "plan" {
		t.Errorf("specs = %+v, want a single task-2 spec keyed by index", specs)
	}
}

func runCand(t *testing.T, lines []string, format string, top int) string {
	t.Helper()
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "s.jsonl", lines)
	var buf bytes.Buffer
	if err := candidates(&buf, root, "s", format, top, ""); err != nil {
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
