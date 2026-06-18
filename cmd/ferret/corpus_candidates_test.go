package main

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/transcript"
)

// TestCallShapeTokens pins the shape-token vocabulary: a built-in tool is its
// name, an MCP call compresses to mcp:server.tool, and a compound Bash call
// expands to one sh:<verb> token per shell statement (an empty/unparseable
// command degrades to a single "sh").
func TestCallShapeTokens(t *testing.T) {
	tests := []struct {
		name string
		blk  transcript.Block
		want []string
	}{
		{"builtin", transcript.Block{Name: "Read", Input: json.RawMessage(`{"file_path":"a.go"}`)}, []string{"Read"}},
		{"mcp", transcript.Block{Name: "mcp__trixi__set_nug"}, []string{"mcp:trixi.set_nug"}},
		{"bash compound", transcript.Block{Name: "Bash", Input: json.RawMessage(`{"command":"git add -A && git commit -m x"}`)}, []string{"sh:git_add", "sh:git_commit"}},
		{"bash empty", transcript.Block{Name: "Bash", Input: json.RawMessage(`{}`)}, []string{"sh"}},
		{"no name", transcript.Block{Name: ""}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := callShapeTokens(&tt.blk)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("callShapeTokens = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestShapeSignature asserts the canonical key: distinct tokens, sorted, with
// duplicates collapsed; an all-empty shape yields ok=false.
func TestShapeSignature(t *testing.T) {
	sig, toks, ok := shapeSignature([]string{"Read", "sh:rg", "Read", "Edit"})
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if sig != "Edit + Read + sh:rg" {
		t.Errorf("sig = %q, want %q", sig, "Edit + Read + sh:rg")
	}
	if strings.Join(toks, ",") != "Edit,Read,sh:rg" {
		t.Errorf("tokens = %v, want sorted+deduped", toks)
	}
	if _, _, ok := shapeSignature([]string{"", ""}); ok {
		t.Error("empty shape: ok = true, want false")
	}
	if _, _, ok := shapeSignature(nil); ok {
		t.Error("nil shape: ok = true, want false")
	}
}

// TestScoreCorpusShape pins the aggregate-leak arithmetic: zero cost leaks
// nothing, and out-weight scales the summed cost toward 2×.
func TestScoreCorpusShape(t *testing.T) {
	tests := []struct {
		name              string
		cost, out         int
		wantScore, wantOW float64
	}{
		{"zero cost", 0, 0, 0, 0},
		{"all input", 100, 0, 100, 0},
		{"all output", 100, 100, 200, 1.0},
		{"quarter output", 100, 25, 125, 0.25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, ow := scoreCorpusShape(tt.cost, tt.out)
			if math.Abs(score-tt.wantScore) > 1e-9 {
				t.Errorf("score = %v, want %v", score, tt.wantScore)
			}
			if math.Abs(ow-tt.wantOW) > 1e-9 {
				t.Errorf("outWeight = %v, want %v", ow, tt.wantOW)
			}
		})
	}
}

// TestRankCorpusMinSessions asserts the recurrence gate (a shape seen in fewer
// than minSessions sessions is dropped), the score-descending sort, and the
// per-instance mean.
func TestRankCorpusMinSessions(t *testing.T) {
	aggs := map[string]*shapeAgg{
		"recurring": {
			tokens: []string{"Read", "sh:rg"}, occ: 4, cost: 800, outBytes: 800,
			sessions: map[string]struct{}{"a": {}, "b": {}, "c": {}},
		},
		"cheap-recurring": {
			tokens: []string{"Edit"}, occ: 2, cost: 100, outBytes: 0,
			sessions: map[string]struct{}{"a": {}, "b": {}},
		},
		"one-session": {
			tokens: []string{"Write"}, occ: 9, cost: 9000, outBytes: 9000,
			sessions: map[string]struct{}{"a": {}},
		},
	}
	got := rankCorpus(aggs, 2)
	if len(got) != 2 {
		t.Fatalf("ranked = %d, want 2 (one-session shape gated out): %+v", len(got), got)
	}
	if got[0].Sig != "recurring" {
		t.Errorf("rank[0] = %q, want recurring (highest score)", got[0].Sig)
	}
	if got[0].MeanCost != 200 {
		t.Errorf("recurring meanCost = %d, want 200 (800/4)", got[0].MeanCost)
	}
	if got[0].Sessions != 3 {
		t.Errorf("recurring sessions = %d, want 3", got[0].Sessions)
	}
	// minSessions=1 admits the one-session shape, which then tops the rank by cost.
	all := rankCorpus(aggs, 1)
	if len(all) != 3 || all[0].Sig != "one-session" {
		t.Errorf("min-sessions=1: got %d shapes, top=%q; want 3, one-session", len(all), all[0].Sig)
	}
}

// corpusFixture writes two sessions in different projects that share a Read+sh:rg
// task shape, plus a single-session Write task — the recurrence gate's fixture.
func corpusFixture(t *testing.T, root string) {
	t.Helper()
	writeSpineFixture(t, root, "-Users-dev-projA", "aaa.jsonl", []string{
		`{"type":"user","sessionId":"aaa","message":{"role":"user","content":"find the callers"}}`,
		`{"type":"assistant","sessionId":"aaa","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"a1","name":"Read","input":{"file_path":"x.go"}},` +
			`{"type":"tool_use","id":"a2","name":"Bash","input":{"command":"rg foo"}}]}}`,
		`{"type":"user","sessionId":"aaa","message":{"role":"user","content":"now write the patch"}}`,
		`{"type":"assistant","sessionId":"aaa","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"a3","name":"Write","input":{"file_path":"y.go","content":"big payload here"}}]}}`,
	})
	writeSpineFixture(t, root, "-Users-dev-projB", "bbb.jsonl", []string{
		`{"type":"user","sessionId":"bbb","message":{"role":"user","content":"locate the callers"}}`,
		`{"type":"assistant","sessionId":"bbb","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"rg bar"}},` +
			`{"type":"tool_use","id":"b2","name":"Read","input":{"file_path":"z.go"}}]}}`,
	})
}

// TestCorpusCandidatesEndToEnd drives the corpus mode over the fixture: the shared
// Read+sh:rg shape recurs across both sessions and survives the gate; the
// single-session Write shape is dropped at min-sessions=2.
func TestCorpusCandidatesEndToEnd(t *testing.T) {
	root := t.TempDir()
	corpusFixture(t, root)

	var buf bytes.Buffer
	if err := corpusCandidates(&buf, root, fmtJSON, 0, 2); err != nil {
		t.Fatalf("corpusCandidates: %v", err)
	}
	var res corpusResult
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("decode json: %v\n%s", err, buf.String())
	}
	if res.Sessions != 2 {
		t.Errorf("sessions walked = %d, want 2", res.Sessions)
	}
	if res.Tasks != 3 {
		t.Errorf("costed tasks = %d, want 3 (2 rg + 1 write)", res.Tasks)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1 (Write gated out at min-sessions=2): %+v", len(res.Candidates), res.Candidates)
	}
	c := res.Candidates[0]
	if c.Sig != "Read + sh:rg" {
		t.Errorf("shape sig = %q, want %q", c.Sig, "Read + sh:rg")
	}
	if c.Sessions != 2 || c.Occurrences != 2 {
		t.Errorf("recurrence: sessions=%d occ=%d, want 2/2", c.Sessions, c.Occurrences)
	}

	// min-sessions=1 admits the single-session Write shape too.
	buf.Reset()
	if err := corpusCandidates(&buf, root, fmtJSON, 0, 1); err != nil {
		t.Fatalf("corpusCandidates min1: %v", err)
	}
	var all corpusResult
	if err := json.Unmarshal(buf.Bytes(), &all); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if len(all.Candidates) != 2 {
		t.Errorf("min-sessions=1 candidates = %d, want 2", len(all.Candidates))
	}
}

// TestCorpusCandidatesText asserts the human rendering carries the header, legend,
// a shape row, and the footer tally.
func TestCorpusCandidatesText(t *testing.T) {
	root := t.TempDir()
	corpusFixture(t, root)
	var buf bytes.Buffer
	if err := corpusCandidates(&buf, root, fmtText, 0, 2); err != nil {
		t.Fatalf("corpusCandidates: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"corpus candidates root=", "task-SHAPES", "[shape] sessions=", "tokens: Read + sh:rg", "--- shapes="} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
}
