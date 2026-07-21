package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/analyst"
)

func TestWriteAdjudicateTextRendersMismatches(t *testing.T) {
	res := analyst.Result{
		Session: "abc123",
		Model:   "claude-sonnet-4-6",
		Findings: []analyst.Finding{
			{Task: "find NewServer def", Call: "rg func NewServer", ToolUsed: "rg", Fit: analyst.FitMismatch, Better: "snipe", Why: "symbol definition lookup", Confidence: "high"},
			{Task: "count tests", Call: "rg -c func Test", ToolUsed: "rg", Fit: analyst.FitServed, Why: "a count, legit", Confidence: "high"},
		},
	}
	var buf bytes.Buffer
	if err := writeAdjudicateText(&buf, res); err != nil {
		t.Fatalf("writeAdjudicateText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "mismatches=1") {
		t.Errorf("header should report 1 mismatch:\n%s", out)
	}
	if !strings.Contains(out, "snipe") || !strings.Contains(out, "symbol definition lookup") {
		t.Errorf("mismatch detail missing:\n%s", out)
	}
	// The served (legit) call must NOT surface as an actionable row.
	if strings.Contains(out, "count tests") {
		t.Errorf("served call leaked into actionable output:\n%s", out)
	}
}

func TestWriteAdjudicateTextNoMismatches(t *testing.T) {
	res := analyst.Result{
		Session:  "abc123",
		Model:    "claude-sonnet-4-6",
		Findings: []analyst.Finding{{Fit: analyst.FitServed, ToolUsed: "rg"}},
	}
	var buf bytes.Buffer
	if err := writeAdjudicateText(&buf, res); err != nil {
		t.Fatalf("writeAdjudicateText: %v", err)
	}
	if !strings.Contains(buf.String(), "no tool-for-intent mismatches") {
		t.Errorf("expected no-mismatch line:\n%s", buf.String())
	}
}

const recallTraceFixture = `{"run_id":"r1","chat_id":"c1","ts":"2026-07-21T10:00:00Z","recalled":[{"source":"chat: Planning","text":"flat-file memory behind a seam","when":"2026-07-20T09:00:00Z"},{"source":"decision: dbd72","text":"5-6 modules + glue","when":"2026-07-19T09:00:00Z"}],"final_text":"The memory backend is a flat file.","outcome":"completed"}

{"run_id":"r2","chat_id":"c1","ts":"2026-07-21T11:00:00Z","recalled":[],"final_text":"No memory needed here.","outcome":"max_iterations"}
`

// TestReadRecallTrace_ParsesJSONL: the reader decodes each non-blank line into a
// run, skips blanks, and maps fragments onto the analyst input shape. Blank-line
// tolerance matters because the emitter appends rows and an editor may leave a
// trailing newline.
func TestReadRecallTrace_ParsesJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recall-trace.jsonl")
	if err := os.WriteFile(path, []byte(recallTraceFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	runs, err := readRecallTrace(path)
	if err != nil {
		t.Fatalf("readRecallTrace: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("want 2 runs, got %d", len(runs))
	}
	if runs[0].RunID != "r1" || runs[0].FinalText != "The memory backend is a flat file." || runs[0].Outcome != "completed" {
		t.Errorf("run[0] fields wrong: %+v", runs[0])
	}
	items := runs[0].items()
	if len(items) != 2 || items[0].Source != "chat: Planning" || items[0].Text != "flat-file memory behind a seam" {
		t.Errorf("run[0].items() wrong: %+v", items)
	}
	if len(runs[1].Recalled) != 0 {
		t.Errorf("run[1] should have zero recalled fragments, got %d", len(runs[1].Recalled))
	}
}

// TestReadRecallTrace_RejectsMalformedLine: a corrupt line must fail loud with
// its line number, not be silently dropped — a half-scored batch would produce a
// false baseline downstream (mirrors the consumer's trailing-JSON rejection).
func TestReadRecallTrace_RejectsMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	body := `{"run_id":"r1","recalled":[],"final_text":"ok","outcome":"completed"}
this is not json
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, err := readRecallTrace(path)
	if err == nil {
		t.Fatal("expected error on malformed line, got nil")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error should name the offending line, got: %v", err)
	}
}

// TestRunRecallTrace_EmptyIsHonest: a trace with no runs is a hard error, never a
// silent zero-finding green — the downstream metric (gg-8rq.16.3) must not read a
// false baseline off an empty batch.
func TestRunRecallTrace_EmptyIsHonest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, []byte("\n\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	saved := CLI.Adjudicate
	defer func() { CLI.Adjudicate = saved }()
	CLI.Adjudicate.RecallTrace = path
	CLI.Adjudicate.Format = fmtJSON

	err := runRecallTrace()
	if err == nil {
		t.Fatal("expected error on empty trace, got nil")
	}
	if !strings.Contains(err.Error(), "no runs") {
		t.Errorf("error should report an empty batch, got: %v", err)
	}
}

// TestEmitRecallPrompts_SkipsEmptyRecall: --emit-prompt assembles a prompt per
// judged run without a network call, and a run that recalled nothing is skipped
// (no fragment to judge, no paid call).
func TestEmitRecallPrompts_SkipsEmptyRecall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recall-trace.jsonl")
	if err := os.WriteFile(path, []byte(recallTraceFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	runs, err := readRecallTrace(path)
	if err != nil {
		t.Fatalf("readRecallTrace: %v", err)
	}
	var buf bytes.Buffer
	emitRecallPrompts(&buf, runs)
	out := buf.String()
	if !strings.Contains(out, "RUN 1 (r1) SYSTEM") || !strings.Contains(out, "FINAL ANSWER:") {
		t.Errorf("expected run 1's assembled prompt:\n%s", out)
	}
	if strings.Contains(out, "r2") {
		t.Errorf("run 2 recalled nothing and must be skipped:\n%s", out)
	}
}

// TestRecallTraceLabel names the batch by the trace file's basename.
func TestRecallTraceLabel(t *testing.T) {
	if got := recallTraceLabel("/tmp/eval/recall-trace.jsonl"); got != "recall-trace:recall-trace.jsonl" {
		t.Errorf("recallTraceLabel = %q", got)
	}
}
