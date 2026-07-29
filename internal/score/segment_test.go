package score

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// TestSegmenterResultDoesNotExposeInternalBackingArray is the ferret-00n
// regression: result() must hand out a copy of s.segs, not the segmenter's
// own backing array. Calls result() twice on the same segmenter (mirroring
// any future caller that re-derives a Result mid-stream) and mutates the
// first Result's Segments in place — a caller-visible, entirely ordinary
// thing to do (sort, filter, edit a field). Pre-fix, that mutation is
// visible through the second Result too, because both share one backing
// array; post-fix each Result owns its own copy.
func TestSegmenterResultDoesNotExposeInternalBackingArray(t *testing.T) {
	src := writeSession(t, segFixtureLines())
	sm := segmenter{}
	if err := transcript.ReadLines(src.Path, func(line []byte) error {
		sm.feed(line)
		return nil
	}); err != nil {
		t.Fatalf("ReadLines: %v", err)
	}

	res1 := sm.result(src)
	if len(res1.Segments) == 0 {
		t.Fatal("fixture produced no segments — test setup is broken")
	}
	origPrompt := res1.Segments[0].Prompt

	// Caller mutates the slice it was handed.
	res1.Segments[0].Prompt = "MUTATED"

	res2 := sm.result(src)
	if res2.Segments[0].Prompt != origPrompt {
		t.Errorf("mutating res1.Segments leaked into segmenter internal state: "+
			"res2.Segments[0].Prompt = %q, want unmutated %q", res2.Segments[0].Prompt, origPrompt)
	}
	if res1.Segments[0].Prompt == res2.Segments[0].Prompt {
		t.Errorf("res1 and res2 Segments still alias the same backing array")
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

// probeFixtureLines is a small session shaped like the ferret-j33 feedback-tap
// hazard: a normal task, an armed ask (assistant reply — not scanned for
// boundaries either way), then a probe ANSWER turn carrying the recognized
// "y - now fix the tests" prefix at timestamp probeTS. Plain SegmentSource
// (no adjustment) segments this exactly like any other user prompt — that IS
// the contamination bug §6 documents: a spurious segment opens keyed on the
// raw, prefixed probe text instead of attributing to the ongoing task.
const probeTS = "2026-07-26T12:00:03Z"

func probeFixtureLines(answerContent string) []string {
	return []string{
		`{"type":"user","timestamp":"2026-07-26T12:00:00Z","sessionId":"s","message":{"role":"user","content":"orient and assess the bead"}}`,
		`{"type":"assistant","timestamp":"2026-07-26T12:00:01Z","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"bead.md"}}]}}`,
		`{"type":"user","timestamp":"` + probeTS + `","sessionId":"s","message":{"role":"user","content":"` + answerContent + `"}}`,
		`{"type":"assistant","timestamp":"2026-07-26T12:00:04Z","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"a.go"}}]}}`,
	}
}

// TestSegmentSourceProbeContaminationBaseline documents the bug plain
// SegmentSource has (ferret-j33 §6): a probe answer's raw, token-prefixed text
// opens its own spurious segment.
func TestSegmentSourceProbeContaminationBaseline(t *testing.T) {
	res, err := SegmentSource(writeSession(t, probeFixtureLines("y - now fix the tests")))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("want 2 segments (contaminated baseline), got %d: %+v", len(res.Segments), res.Segments)
	}
	if res.Segments[1].Prompt != "y - now fix the tests" {
		t.Errorf("baseline seg2.Prompt = %q, want the raw contaminated probe text", res.Segments[1].Prompt)
	}
}

// TestSegmentSourceExcludingCleansProbeAnswer is the fix: with the ledger-
// derived adjustment (probeTS → the answer's already-stripped remainder), the
// probe turn's segment carries the CLEAN remainder text, never the raw
// "y - now fix the tests" prefix.
func TestSegmentSourceExcludingCleansProbeAnswer(t *testing.T) {
	src := writeSession(t, probeFixtureLines("y - now fix the tests"))
	res, err := SegmentSourceExcluding(src, map[string]string{probeTS: "now fix the tests"})
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range res.Segments {
		if strings.Contains(seg.Prompt, "y - now fix the tests") {
			t.Errorf("segment %+v still carries the raw probe prefix, want it cleaned", seg)
		}
	}
	if res.Segments[1].Prompt != "now fix the tests" {
		t.Errorf("excluded seg2.Prompt = %q, want the clean remainder", res.Segments[1].Prompt)
	}
}

// TestSegmentSourceExcludingIgnoredLabelUnaffected is the over-exclusion guard
// (test plan §4 companion): a label the caller never adjusted (as buildProbeAdjustments
// does for an Ignored valence) must segment exactly like the plain baseline —
// proving the fix doesn't eat legitimate work turns it wasn't told to touch.
func TestSegmentSourceExcludingIgnoredLabelUnaffected(t *testing.T) {
	src := writeSession(t, probeFixtureLines("please also check the tests"))
	plain, err := SegmentSource(src)
	if err != nil {
		t.Fatal(err)
	}
	excluded, err := SegmentSourceExcluding(src, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Segments) != len(excluded.Segments) || plain.Segments[1].Prompt != excluded.Segments[1].Prompt {
		t.Errorf("an empty/non-matching adjustment map must not alter segmentation: plain=%+v excluded=%+v",
			plain.Segments, excluded.Segments)
	}
}

// TestSegmentSourceExcludingBareTokenNoEmptySegment is the AC's simplest,
// most common case (plan-review P1-2): a bare-token answer ("n") folds to an
// EMPTY replacement. That must route to the same no-boundary carrier path a
// tool_result envelope takes — NOT open a spurious empty-Prompt segment.
func TestSegmentSourceExcludingBareTokenNoEmptySegment(t *testing.T) {
	src := writeSession(t, probeFixtureLines("n"))

	baseline, err := SegmentSource(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Segments) != 2 || baseline.Segments[1].Prompt != "n" {
		t.Fatalf("contaminated baseline: want a spurious 'n' segment, got %+v", baseline.Segments)
	}

	res, err := SegmentSourceExcluding(src, map[string]string{probeTS: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 1 {
		t.Fatalf("bare-token fold-to-empty must NOT open a new segment, got %d: %+v", len(res.Segments), res.Segments)
	}
	for _, seg := range res.Segments {
		if seg.Prompt == "" && seg.Index != 0 {
			t.Errorf("no non-preamble segment may carry an empty Prompt, got %+v", seg)
		}
	}
	// t2 (the call after the folded probe turn) attributes to the ongoing task,
	// not a new segment — the carrier path's normal behavior.
	if res.Segments[0].FirstCall != 0 || res.Segments[0].LastCall != 1 {
		t.Errorf("both calls must attribute to the single live task, got range %d..%d",
			res.Segments[0].FirstCall, res.Segments[0].LastCall)
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
