package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/transcript"
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
	var res segResult
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
	var res segResult
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
		"[seg 1] calls=0..1  prompt: orient and assess the bead",
		"[seg 2] calls=2  prompt: now implement the marker",
		"[pivot] think#",
		`cue="let me now"`,
		"--- segments=2 calls=3 pivots=1",
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
	var res segResult
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

// TestPromptTextIgnoresToolResults guards the boundary signal: a user line that
// only carries a tool_result is NOT a prompt and must not open a segment.
func TestPromptTextIgnoresToolResults(t *testing.T) {
	blocks := mustBlocks(t, `[{"type":"tool_result","tool_use_id":"t1","content":"x"}]`)
	if got := promptText(blocks); got != "" {
		t.Errorf("tool_result-only line yielded prompt %q, want empty", got)
	}
	blocks = mustBlocks(t, `[{"type":"text","text":"  do  the   thing  "}]`)
	if got := promptText(blocks); got != "do the thing" {
		t.Errorf("promptText = %q, want collapsed %q", got, "do the thing")
	}
}

func mustBlocks(t *testing.T, raw string) transcript.Blocks {
	t.Helper()
	var b transcript.Blocks
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	return b
}
