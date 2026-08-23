package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/mine"
)

// burnResult builds a minimal ranked BurnResult for the render tests — the
// aggregation itself (grouping/ranking/calls/bytes-per-call/sessions/render
// cost) is pinned in internal/mine/burn_test.go; these tests pin the CLI
// render layer.
//
// Rows are in miner order, which since ferret-noj is context-bytes descending:
// the 2-call tool key leads on 150 bytes and the 3-call shell key trails on 35.
// The writer must print that order verbatim — no re-sorting in the render
// layer, which is what makes the miner the single home of the ranking.
func burnResult() *mine.BurnResult {
	return &mine.BurnResult{
		Events:   5,
		Sessions: 3,
		Rows: []mine.BurnRow{
			{Key: "Read", Bytes: 150, Calls: 2, BytesPerCall: 75, Sessions: 1},
			{Key: "sh:git_commit", Bytes: 35, Calls: 3, BytesPerCall: 35.0 / 3.0, Sessions: 2},
		},
	}
}

// burnRowsOut returns the rendered output from the "burn events=" header
// onward — the about() preamble names "sh:git_commit" as an example, so every
// row-order and row-suppression assertion must be scoped past it.
func burnRowsOut(t *testing.T, out string) string {
	t.Helper()
	hdrIdx := strings.Index(out, "burn events=")
	if hdrIdx < 0 {
		t.Fatalf("missing burn events= header\n---\n%s", out)
	}
	return out[hdrIdx:]
}

// TestWriteBurnText_RendersRankedRows_When_ResultHasRows pins the text
// render: header totals, both keys present, in the miner's render-cost rank
// order (sh:git_commit before Read — fewer bytes, more rendered chrome).
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
	rowsOut := burnRowsOut(t, out)
	gitIdx := strings.Index(rowsOut, "sh:git_commit")
	readIdx := strings.Index(rowsOut, "Read")
	if gitIdx < 0 || readIdx < 0 || readIdx > gitIdx {
		t.Errorf("expected Read row before sh:git_commit row (context-byte rank order)\n---\n%s", rowsOut)
	}
}

// TestWriteBurnText_ShowsByteAndPerCallColumns_When_ResultHasRows pins the
// columns a tune-up decision reads: the gross byte total that ranks, and the
// per-call toll that says whether the fix is "call it less" or "make each call
// cheaper". Those two disagree whenever a cheap command is called constantly.
func TestWriteBurnText_ShowsByteAndPerCallColumns_When_ResultHasRows(t *testing.T) {
	var buf bytes.Buffer
	if err := writeBurnText(&buf, burnResult(), 0, 0); err != nil {
		t.Fatalf("writeBurnText: %v", err)
	}
	rowsOut := burnRowsOut(t, buf.String())
	for _, want := range []string{"bytes", "/call", "calls", "sess"} {
		if !strings.Contains(rowsOut, want) {
			t.Errorf("rendered rows missing %q column label\n---\n%s", want, rowsOut)
		}
	}
	// The leading row's gross total (150B) and the trailing row's much smaller
	// one (35B) must both be legible in one table.
	for _, want := range []string{"150B", "35B"} {
		if !strings.Contains(rowsOut, want) {
			t.Errorf("rendered rows missing value %q\n---\n%s", want, rowsOut)
		}
	}
}

// TestWriteBurnText_RespectsLimit_When_LimitBelowRowCount pins that --limit
// caps rendered rows and the sink reports the truncation. The surviving row is
// the top-ranked one (Read), so the cap drops sh:git_commit — the limit-cap
// discipline rides on the miner's order, which it never re-derives.
func TestWriteBurnText_RespectsLimit_When_LimitBelowRowCount(t *testing.T) {
	var buf bytes.Buffer
	if err := writeBurnText(&buf, burnResult(), 1, 0); err != nil {
		t.Fatalf("writeBurnText: %v", err)
	}
	out := buf.String()
	rowsOut := burnRowsOut(t, out)
	if strings.Contains(rowsOut, "sh:git_commit") {
		t.Errorf("expected sh:git_commit row suppressed by --limit=1\n---\n%s", rowsOut)
	}
	if !strings.Contains(rowsOut, "Read") {
		t.Errorf("expected top-ranked Read row to survive --limit=1\n---\n%s", rowsOut)
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
	for _, col := range []string{"key", "bytes", "calls", "bytesPerCall", "sessions"} {
		if _, ok := row[col]; !ok {
			t.Errorf("row missing column %q: %v", col, row)
		}
	}
	// The one surviving row under limit=1 is the miner's rank leader — the JSON
	// cap consumes the miner's order as-is rather than re-deriving it.
	if key, _ := row["key"].(string); key != "Read" {
		t.Errorf(`rows[0]["key"] = %v, want "Read" (context-byte rank leader)`, row["key"])
	}
	if b, ok := row["bytes"].(float64); !ok || b != 150 {
		t.Errorf(`rows[0]["bytes"] = %v, want 150`, row["bytes"])
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
