package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/apiusage"
)

// usageReport builds a report whose token ranking and cost ranking disagree —
// the condition the render must make visible.
func usageReport() *apiusage.Report {
	return &apiusage.Report{
		Totals:   apiusage.Totals{Calls: 3, Input: 1000, CacheWrite: 200000, CacheRead: 2000000, Output: 20000, Thinking: 5000, Write1h: 150000, Write5m: 50000},
		Sessions: 2,
		Models:   []apiusage.ModelRow{{Model: "claude-opus-5", Totals: apiusage.Totals{Calls: 3}, Weighted: 100}},
		Rows: []apiusage.SessionRow{
			{Session: "aaaaaaaa-1111", Totals: apiusage.Totals{Calls: 2, CacheWrite: 150000, CacheRead: 1500000, Output: 15000}, Weighted: 412500, Agents: 3},
			{Session: "bbbbbbbb-2222", Totals: apiusage.Totals{Calls: 1, CacheWrite: 50000, CacheRead: 500000, Output: 5000}, Weighted: 137500, Agents: 1},
		},
	}
}

// TestWriteUsageText_ShowsTokenAndCostViews_When_TheyDisagree pins the whole
// point of the report: cache-read dominates the token count while cache-write
// and output take a far larger share once priced, so both columns must render.
func TestWriteUsageText_ShowsTokenAndCostViews_When_TheyDisagree(t *testing.T) {
	var buf bytes.Buffer
	if err := writeUsageText(&buf, usageReport(), 0, 0); err != nil {
		t.Fatalf("writeUsageText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"calls=3", "sessions=2", "%tokens", "%weighted",
		"input", "cache-write", "cache-read", "output",
		"reads per written token", "thinking=25.0% of output",
		"/usage", // the reconciliation instruction must be on the page
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n---\n%s", want, out)
		}
	}
}

// TestWriteUsageText_SpendsLimitOnSessions_When_LimitIsSmall pins the output
// budget split: --limit governs the ranked session list, not the fixed bucket
// table, so a small limit still shows the accounting.
func TestWriteUsageText_SpendsLimitOnSessions_When_LimitIsSmall(t *testing.T) {
	var buf bytes.Buffer
	if err := writeUsageText(&buf, usageReport(), 1, 0); err != nil {
		t.Fatalf("writeUsageText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "cache-write") {
		t.Errorf("--limit 1 swallowed the bucket table\n---\n%s", out)
	}
	if !strings.Contains(out, "aaaaaaaa") {
		t.Errorf("--limit 1 must still show the top session\n---\n%s", out)
	}
	if strings.Contains(out, "bbbbbbbb") {
		t.Errorf("--limit 1 rendered a second session row\n---\n%s", out)
	}
}

// TestWriteUsageText_ReportsEmptyState_When_NoCalls pins the DK-AXI 0-results
// rule for a corpus whose ledger is empty.
func TestWriteUsageText_ReportsEmptyState_When_NoCalls(t *testing.T) {
	var buf bytes.Buffer
	if err := writeUsageText(&buf, &apiusage.Report{}, 0, 0); err != nil {
		t.Fatalf("writeUsageText: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "0 API calls") {
		t.Errorf("missing explicit empty state\n---\n%s", out)
	}
}

// TestHumanCount_ScalesToTokenMagnitudes pins the renderer at the scales this
// ledger actually reaches — billions of cached-read tokens.
func TestHumanCount_ScalesToTokenMagnitudes(t *testing.T) {
	cases := map[int64]string{999: "999", 1500: "1.5k", 2_500_000: "2.5m", 14_131_077_349: "14.13b"}
	for in, want := range cases {
		if got := humanCount(in); got != want {
			t.Errorf("humanCount(%d) = %q, want %q", in, got, want)
		}
	}
}
