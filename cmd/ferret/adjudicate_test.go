package main

import (
	"bytes"
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
