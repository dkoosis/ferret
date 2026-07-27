package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/mine"
)

// burnResult builds a minimal ranked BurnResult for the render tests — the
// aggregation itself (grouping/ranking/calls/bytes-per-call/sessions) is
// pinned in internal/mine/burn_test.go; these tests pin the CLI render layer.
func burnResult() *mine.BurnResult {
	return &mine.BurnResult{
		Events:   5,
		Sessions: 3,
		Rows: []mine.BurnRow{
			{Key: "Read", OutBytes: 150, Calls: 2, BytesPerCall: 75, Sessions: 1},
			{Key: "sh:git_commit", OutBytes: 35, Calls: 3, BytesPerCall: 35.0 / 3.0, Sessions: 2},
		},
	}
}

// TestWriteBurnText_RendersRankedRows_When_ResultHasRows pins the text
// render: header totals, both keys present, in rank order (Read before
// sh:git_commit — it has more out-bytes).
func TestWriteBurnText_RendersRankedRows_When_ResultHasRows(t *testing.T) {
	var buf bytes.Buffer
	if err := writeBurnText(&buf, burnResult(), 0, 0); err != nil {
		t.Fatalf("writeBurnText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"events=5", "sessions=3", "rows=2", "Read", "sh:git_commit"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
	// The about() preamble mentions "sh:git_commit" as an example, so scope the
	// order check to the rows printed after the "burn events=" header line.
	rowsOut := out[strings.Index(out, "burn events="):]
	readIdx := strings.Index(rowsOut, "Read")
	gitIdx := strings.Index(rowsOut, "sh:git_commit")
	if readIdx < 0 || gitIdx < 0 || readIdx > gitIdx {
		t.Errorf("expected Read row before sh:git_commit row (rank order)\n---\n%s", rowsOut)
	}
}

// TestWriteBurnText_RespectsLimit_When_LimitBelowRowCount pins that --limit
// caps rendered rows and the sink reports the truncation.
func TestWriteBurnText_RespectsLimit_When_LimitBelowRowCount(t *testing.T) {
	var buf bytes.Buffer
	if err := writeBurnText(&buf, burnResult(), 1, 0); err != nil {
		t.Fatalf("writeBurnText: %v", err)
	}
	out := buf.String()
	// The about() preamble mentions "sh:git_commit" as an example; scope the
	// suppression check to the rows printed after the "burn events=" header.
	rowsOut := out[strings.Index(out, "burn events="):]
	if strings.Contains(rowsOut, "sh:git_commit") {
		t.Errorf("expected sh:git_commit row suppressed by --limit=1\n---\n%s", rowsOut)
	}
	if !strings.Contains(out, "more") {
		t.Errorf("expected truncation notice from out.Sink\n---\n%s", out)
	}
}

// TestWriteBurnJSON_RoundTrips_When_ResultEncoded pins the JSON contract:
// valid JSON, totals present, rows carry all four AC columns, and --limit
// truncation is reflected in the total/truncated bookkeeping keys.
func TestWriteBurnJSON_RoundTrips_When_ResultEncoded(t *testing.T) {
	var buf bytes.Buffer
	if err := writeBurnJSON(&buf, burnResult(), 1); err != nil {
		t.Fatalf("writeBurnJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	for _, key := range []string{"events", "sessions", "rows", "total", "truncated"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing key %q\n%s", key, buf.String())
		}
	}
	if total, ok := got["total"].(float64); !ok || total != 2 {
		t.Errorf(`JSON "total" = %v, want 2`, got["total"])
	}
	if truncated, ok := got["truncated"].(bool); !ok || !truncated {
		t.Errorf(`JSON "truncated" = %v, want true (limit=1 < 2 rows)`, got["truncated"])
	}
	rows, ok := got["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf(`JSON "rows" = %v, want 1-element array (capped by limit)`, got["rows"])
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("rows[0] is not an object: %v", rows[0])
	}
	for _, col := range []string{"key", "outBytes", "calls", "bytesPerCall", "sessions"} {
		if _, ok := row[col]; !ok {
			t.Errorf("row missing column %q: %v", col, row)
		}
	}
}

// TestWriteBurnJSON_NoTruncation_When_LimitZero pins limit=0 (unlimited) as
// the no-op case: every row survives and truncated reads false.
func TestWriteBurnJSON_NoTruncation_When_LimitZero(t *testing.T) {
	var buf bytes.Buffer
	if err := writeBurnJSON(&buf, burnResult(), 0); err != nil {
		t.Fatalf("writeBurnJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	rows, _ := got["rows"].([]any)
	if len(rows) != 2 {
		t.Errorf("rows len = %d, want 2 (no truncation at limit=0)", len(rows))
	}
	if truncated, _ := got["truncated"].(bool); truncated {
		t.Errorf("truncated = true, want false at limit=0")
	}
}
