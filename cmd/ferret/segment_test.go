package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/score"
)

// segFixtureLines is a small synthetic session with TWO user prompts, interleaved
// thinking (one with a goal-shift cue, one without), and tool calls before and
// after the second prompt — enough to exercise boundaries, call ownership, pivots,
// the preamble path is exercised separately.
func segFixtureLines() []string {
	return []string{
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"orient and assess the bead"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"I should read the bead first."},` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"bead.md"}},` +
			`{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"bd show x"}}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"now implement the marker"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"Let me now switch to writing the code."},` +
			`{"type":"tool_use","id":"t3","name":"Edit","input":{"file_path":"a.go"}}]}}`,
		`{"type":"user","isMeta":true,"sessionId":"s","message":{"role":"user","content":"SYSTEM noise"}}`,
	}
}

func runSeg(t *testing.T, lines []string, format string) string {
	t.Helper()
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "s.jsonl", lines)
	var buf bytes.Buffer
	if err := segments(&buf, root, "s", format); err != nil {
		t.Fatalf("segments: %v", err)
	}
	return buf.String()
}

// TestSegmentsBoundaryPerPrompt asserts the load-bearing contract: every new user
// prompt yields exactly one boundary candidate, meta noise is dropped, and tool
// calls are owned by the segment whose prompt precedes them.
func TestSegmentsBoundaryPerPrompt(t *testing.T) {
	out := runSeg(t, segFixtureLines(), fmtJSON)
	var res score.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("want 2 boundary candidates (1 per prompt), got %d: %+v", len(res.Segments), res.Segments)
	}
	if res.Segments[0].Prompt != "orient and assess the bead" {
		t.Errorf("seg1 prompt = %q", res.Segments[0].Prompt)
	}
	if res.Segments[1].Prompt != "now implement the marker" {
		t.Errorf("seg2 prompt = %q", res.Segments[1].Prompt)
	}
	// seg1 owns calls 0..1 (the Read + Bash); seg2 owns call 2 (the Edit).
	if res.Segments[0].FirstCall != 0 || res.Segments[0].LastCall != 1 {
		t.Errorf("seg1 call range = %d..%d, want 0..1", res.Segments[0].FirstCall, res.Segments[0].LastCall)
	}
	if res.Segments[1].FirstCall != 2 || res.Segments[1].LastCall != 2 {
		t.Errorf("seg2 call range = %d..%d, want 2..2", res.Segments[1].FirstCall, res.Segments[1].LastCall)
	}
	if res.TotalCalls != 3 {
		t.Errorf("totalCalls = %d, want 3", res.TotalCalls)
	}
}

// TestSegmentsPivotHints asserts the cheap thinking-pivot heuristic flags a
// goal-shift cue ("let me now…") and leaves plain reasoning unflagged.
func TestSegmentsPivotHints(t *testing.T) {
	out := runSeg(t, segFixtureLines(), fmtJSON)
	var res score.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if len(res.Segments[0].Pivots) != 0 {
		t.Errorf("seg1 should have no pivot hint (plain reasoning), got %+v", res.Segments[0].Pivots)
	}
	if len(res.Segments[1].Pivots) != 1 {
		t.Fatalf("seg2 should have 1 pivot hint, got %+v", res.Segments[1].Pivots)
	}
	if got := res.Segments[1].Pivots[0].Cue; got != "let me now" {
		t.Errorf("pivot cue = %q, want %q", got, "let me now")
	}
	if res.Pivots != 1 {
		t.Errorf("total pivots = %d, want 1", res.Pivots)
	}
}

// TestSegmentsDeterministic is the acceptance gate the bead names: boundary
// candidates must be byte-stable across repeated runs on identical input.
func TestSegmentsDeterministic(t *testing.T) {
	lines := segFixtureLines()
	for _, format := range []string{"text", fmtJSON} {
		first := runSeg(t, lines, format)
		for i := range 5 {
			if got := runSeg(t, lines, format); got != first {
				t.Fatalf("format %s not byte-stable on run %d:\n--- first ---\n%s\n--- got ---\n%s",
					format, i, first, got)
			}
		}
	}
}

// TestSegmentsTextShape checks the human rendering carries the boundary rows, the
// pivot hint, the analyst-scope disclaimer, and the trailer counts.
func TestSegmentsTextShape(t *testing.T) {
	out := runSeg(t, segFixtureLines(), "text")
	for _, want := range []string{
		"segments session=s project=-Users-dev-proj",
		"analyst-side, NOT ferret",
		"[seg 1] calls=0..1 cost=50B out=4B  prompt: orient and assess the bead",
		"[seg 2] calls=2 cost=20B out=0B  prompt: now implement the marker",
		"[pivot] think#",
		`cue="let me now"`,
		"--- segments=2 calls=3 cost=70B out=4B pivots=1 conts=0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
}

// TestSegmentsPreamble covers calls that precede any user prompt: they must land
// in a synthetic preamble segment (index 0) so no tool call is silently dropped,
// and pivots before the first prompt carry no task meaning (ignored).
func TestSegmentsPreamble(t *testing.T) {
	lines := []string{
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"Let me now do something."},` +
			`{"type":"tool_use","id":"t0","name":"Bash","input":{"command":"setup"}}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"the real prompt"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"x"}}]}}`,
	}
	out := runSeg(t, lines, fmtJSON)
	var res score.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("want preamble + 1 prompt segment, got %d", len(res.Segments))
	}
	if res.Segments[0].Index != 0 || res.Segments[0].FirstCall != 0 {
		t.Errorf("preamble = %+v, want index 0 owning call 0", res.Segments[0])
	}
	if res.Pivots != 0 {
		t.Errorf("pivot before first prompt must be ignored, got %d", res.Pivots)
	}
	if res.Segments[1].Index != 1 || res.Segments[1].FirstCall != 1 {
		t.Errorf("seg1 = %+v, want index 1 owning call 1", res.Segments[1])
	}
}

// TestSegmentsFoldsNonBoundaries is the integration contract: a bare "yes" between
// two work prompts does NOT open a segment — it folds into the live task as a
// continuation, and the calls that follow it attribute to that same task. A control
// built-in folds in too; an unknown command opens a boundary.
func TestSegmentsFoldsNonBoundaries(t *testing.T) {
	lines := []string{
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"design the signal"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t0","name":"Read","input":{"file_path":"a"}}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"yes"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"b"}}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"push it"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"git push"}}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":[` +
			`{"type":"text","text":"<command-name>/clear</command-name>"}]}}`,
	}
	out := runSeg(t, lines, fmtJSON)
	var res score.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("want 2 boundaries (yes + /clear folded), got %d: %+v", len(res.Segments), res.Segments)
	}
	// seg1 "design the signal" owns the Read AND the post-"yes" Write (calls 0..1).
	if res.Segments[0].FirstCall != 0 || res.Segments[0].LastCall != 1 {
		t.Errorf("seg1 range = %d..%d, want 0..1 (yes-continuation calls attribute here)",
			res.Segments[0].FirstCall, res.Segments[0].LastCall)
	}
	if len(res.Segments[0].Conts) != 1 || res.Segments[0].Conts[0].Kind != "affirmation" {
		t.Errorf("seg1 conts = %+v, want one affirmation", res.Segments[0].Conts)
	}
	// /clear folds into seg2 "push it"; seg2 owns the git-push call.
	if res.Segments[1].Prompt != "push it" || res.Segments[1].FirstCall != 2 {
		t.Errorf("seg2 = %+v, want prompt 'push it' owning call 2", res.Segments[1])
	}
	if len(res.Segments[1].Conts) != 1 || res.Segments[1].Conts[0].Kind != "control" {
		t.Errorf("seg2 conts = %+v, want one control", res.Segments[1].Conts)
	}
	if res.Conts != 2 {
		t.Errorf("total conts = %d, want 2", res.Conts)
	}
}

// TestSegmentsDropsLeadingNonBoundary covers a non-boundary turn before any real
// segment: it is counted but attaches nowhere (no task to continue), and the first
// real prompt still becomes seg 1.
func TestSegmentsDropsLeadingNonBoundary(t *testing.T) {
	lines := []string{
		`{"type":"user","sessionId":"s","message":{"role":"user","content":[` +
			`{"type":"text","text":"<command-name>/clear</command-name>"}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"the real prompt"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t0","name":"Read","input":{"file_path":"x"}}]}}`,
	}
	out := runSeg(t, lines, fmtJSON)
	var res score.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(res.Segments) != 1 || res.Segments[0].Index != 1 {
		t.Fatalf("want a single seg 1 (leading /clear dropped), got %+v", res.Segments)
	}
	if len(res.Segments[0].Conts) != 0 {
		t.Errorf("leading non-boundary must not attach, got %+v", res.Segments[0].Conts)
	}
	if res.Conts != 1 {
		t.Errorf("conts tally = %d, want 1 (counted though dropped)", res.Conts)
	}
}

// TestSegmentsPerTaskCost is the step-3 contract (ferret-567): each task carries
// its input-byte spend and the output bytes its calls pulled in, with results
// attributed to the OWNING task by tool_use id even when the result lands on a
// later line — and a result for an unowned id counts as orphan output.
func TestSegmentsPerTaskCost(t *testing.T) {
	lines := []string{
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"task one"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"u1","name":"Bash","input":{"command":"x"}}]}}`,
		// Result for u1 arrives AFTER the next prompt opens task two — must still
		// charge task one.
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"task two"}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"u1","content":"0123456789"}]}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"u2","name":"Read","input":{"file_path":"y"}}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"u2","content":"ABCDE"},` +
			`{"type":"tool_result","tool_use_id":"ghost","content":"orphaned"}]}}`,
	}
	out := runSeg(t, lines, fmtJSON)
	var res score.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(res.Segments))
	}
	// task one: input = len(`{"command":"x"}`)=15; output = len(`"0123456789"`)=12.
	if res.Segments[0].InBytes != 15 || res.Segments[0].OutBytes != 12 {
		t.Errorf("task one cost = in %d/out %d, want 15/12 (result attributed across the boundary)",
			res.Segments[0].InBytes, res.Segments[0].OutBytes)
	}
	// task two: input = len(`{"file_path":"y"}`)=17; output = len(`"ABCDE"`)=7.
	if res.Segments[1].InBytes != 17 || res.Segments[1].OutBytes != 7 {
		t.Errorf("task two cost = in %d/out %d, want 17/7", res.Segments[1].InBytes, res.Segments[1].OutBytes)
	}
	if res.TotalIn != 32 || res.TotalOut != 19 {
		t.Errorf("totals = in %d/out %d, want 32/19", res.TotalIn, res.TotalOut)
	}
	// the "ghost" result matched no owned call → orphan output = len(`"orphaned"`)=10.
	if res.OutOrphan != 10 {
		t.Errorf("outOrphan = %d, want 10", res.OutOrphan)
	}
}
