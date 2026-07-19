package score

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dkoosis/ferret/internal/transcript"
)

// segFixtureLines is a small synthetic session with TWO user prompts, interleaved
// thinking (one with a goal-shift cue, one without), tool calls before and after
// the second prompt, a folded affirmation, and a tool_result that lands after a
// later boundary — enough to exercise boundaries, call ownership, pivots,
// continuations, and cross-boundary cost attribution in one pass.
func segFixtureLines() []string {
	return []string{
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"orient and assess the bead"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"I should read the bead first."},` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"bead.md"}},` +
			`{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"bd show x"}}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"yes"}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"now implement the marker"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"Let me now switch to writing the code."},` +
			`{"type":"tool_use","id":"t3","name":"Edit","input":{"file_path":"a.go"}}]}}`,
		// t1's result lands here, after the second boundary — must still charge seg 1.
		`{"type":"user","sessionId":"s","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","content":"0123"}]}}`,
		`{"type":"user","isMeta":true,"sessionId":"s","message":{"role":"user","content":"SYSTEM noise"}}`,
	}
}

// writeSession writes lines as a session jsonl under a temp root and returns its
// transcript.Source — the input to SegmentSource.
func writeSession(t *testing.T, lines []string) transcript.Source {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	var buf []byte
	for _, ln := range lines {
		buf = append(buf, ln...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	return transcript.Source{Path: path, Project: "p", Session: "s"}
}

// TestSegmentSource is the engine contract at the score boundary: one boundary per
// real prompt (meta noise + the bare "yes" fold in, not opening segments), calls
// owned by the segment whose prompt precedes them, the pivot heuristic firing only
// on the goal-shift cue, and a tool_result attributed to its owning task even when
// it arrives after a later boundary.
func TestSegmentSource(t *testing.T) {
	res, err := SegmentSource(writeSession(t, segFixtureLines()))
	if err != nil {
		t.Fatalf("SegmentSource: %v", err)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("want 2 boundaries (yes + meta fold/drop), got %d: %+v", len(res.Segments), res.Segments)
	}
	if res.Segments[0].Prompt != "orient and assess the bead" || res.Segments[1].Prompt != "now implement the marker" {
		t.Errorf("prompts = %q / %q", res.Segments[0].Prompt, res.Segments[1].Prompt)
	}
	// seg 1 owns calls 0..1 (Read+Bash); seg 2 owns call 2 (Edit).
	if res.Segments[0].FirstCall != 0 || res.Segments[0].LastCall != 1 {
		t.Errorf("seg1 range = %d..%d, want 0..1", res.Segments[0].FirstCall, res.Segments[0].LastCall)
	}
	if res.Segments[1].FirstCall != 2 || res.Segments[1].LastCall != 2 {
		t.Errorf("seg2 range = %d..%d, want 2..2", res.Segments[1].FirstCall, res.Segments[1].LastCall)
	}
	if res.TotalCalls != 3 {
		t.Errorf("totalCalls = %d, want 3", res.TotalCalls)
	}
	// pivot fires only on seg 2's "let me now" cue.
	if len(res.Segments[0].Pivots) != 0 || len(res.Segments[1].Pivots) != 1 || res.Pivots != 1 {
		t.Errorf("pivots = seg1:%d seg2:%d total:%d, want 0/1/1",
			len(res.Segments[0].Pivots), len(res.Segments[1].Pivots), res.Pivots)
	}
	if res.Segments[1].Pivots[0].Cue != "let me now" {
		t.Errorf("pivot cue = %q, want %q", res.Segments[1].Pivots[0].Cue, "let me now")
	}
	// the bare "yes" folded into seg 1 as an affirmation continuation.
	if res.Conts != 1 || len(res.Segments[0].Conts) != 1 || res.Segments[0].Conts[0].Kind != "affirmation" {
		t.Errorf("conts = total:%d seg1:%+v, want one affirmation on seg1", res.Conts, res.Segments[0].Conts)
	}
	// t1's result (`"0123"` = 6 bytes) charges seg 1 across the boundary.
	if res.Segments[0].OutBytes != 6 {
		t.Errorf("seg1 outBytes = %d, want 6 (result attributed across boundary)", res.Segments[0].OutBytes)
	}
}

// TestSegmentSourceDeterministic is the acceptance gate: the engine is byte-stable
// across repeated runs on identical input (no time, map-order, or randomness).
func TestSegmentSourceDeterministic(t *testing.T) {
	src := writeSession(t, segFixtureLines())
	marshal := func(r Result) string {
		t.Helper()
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	first, err := SegmentSource(src)
	if err != nil {
		t.Fatal(err)
	}
	want := marshal(first)
	for i := range 5 {
		got, gerr := SegmentSource(src)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if j := marshal(got); j != want {
			t.Fatalf("run %d not byte-stable:\nwant %s\ngot  %s", i, want, j)
		}
	}
}

// TestSegmentSourcePreamble covers calls that precede any user prompt: they land in
// a synthetic preamble segment (index 0) so no call is dropped, and a pivot before
// the first prompt carries no task meaning (ignored).
func TestSegmentSourcePreamble(t *testing.T) {
	lines := []string{
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"Let me now do something."},` +
			`{"type":"tool_use","id":"t0","name":"Bash","input":{"command":"setup"}}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"the real prompt"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"x"}}]}}`,
	}
	res, err := SegmentSource(writeSession(t, lines))
	if err != nil {
		t.Fatal(err)
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

// TestSegmentSourceCompactionSummary guards the smoke FP at turn 15: the
// post-compaction carrier turn (a user message the harness injects, opening with
// "This session is being continued from a previous conversation…") reads as prompt
// text but is a system envelope, not user intent — it must fold into the live
// segment as a carrier, NOT open a spurious boundary.
func TestSegmentSourceCompactionSummary(t *testing.T) {
	lines := []string{
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"do the real task"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"x"}}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"This session is being continued from a previous conversation that ran out of context. The conversation is summarized below:"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"y"}}]}}`,
	}
	res, err := SegmentSource(writeSession(t, lines))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 1 {
		t.Fatalf("compaction summary must NOT open a boundary: want 1 segment, got %d: %+v", len(res.Segments), res.Segments)
	}
	// Both calls attribute to the single live task; the summary folded as a carrier.
	if res.Segments[0].FirstCall != 0 || res.Segments[0].LastCall != 1 {
		t.Errorf("seg range = %d..%d, want 0..1 (both calls owned by the live task)", res.Segments[0].FirstCall, res.Segments[0].LastCall)
	}
	if res.Conts != 1 || len(res.Segments[0].Conts) != 1 || res.Segments[0].Conts[0].Kind != "carrier" {
		t.Errorf("want one carrier continuation, got conts=%d seg0.Conts=%+v", res.Conts, res.Segments[0].Conts)
	}
}
