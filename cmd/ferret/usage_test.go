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
	var opus, sonnet apiusage.Totals
	opus.Add(&apiusage.Row{Model: "claude-opus-5", CacheWrite: 150000, Write1h: 150000, CacheRead: 1500000, Output: 15000})
	opus.Add(&apiusage.Row{Model: "claude-opus-5", CacheWrite: 50000, Write1h: 50000, CacheRead: 500000, Output: 5000, Thinking: 5000})
	sonnet.Add(&apiusage.Row{Model: "claude-sonnet-5", Input: 1000, CacheRead: 100000})

	var all apiusage.Totals
	all.Add(&apiusage.Row{Model: "claude-opus-5", CacheWrite: 150000, Write1h: 150000, CacheRead: 1500000, Output: 15000})
	all.Add(&apiusage.Row{Model: "claude-opus-5", CacheWrite: 50000, Write1h: 50000, CacheRead: 500000, Output: 5000, Thinking: 5000})
	all.Add(&apiusage.Row{Model: "claude-sonnet-5", Input: 1000, CacheRead: 100000})
	all.Add(&apiusage.Row{Model: "<synthetic>", Output: 40})

	return &apiusage.Report{
		Totals:   all,
		Sessions: 2,
		Models: []apiusage.ModelRow{
			{Model: "claude-opus-5", Totals: opus},
			{Model: "claude-sonnet-5", Totals: sonnet},
		},
		Rows: []apiusage.SessionRow{
			{Session: "aaaaaaaa-1111", Totals: opus, Agents: 3},
			{Session: "bbbbbbbb-2222", Totals: sonnet, Agents: 1},
		},
	}
}

// TestWriteUsageText_ShowsTokenAndCostViews_When_TheyDisagree pins the whole
// point of the report: cache-read dominates the token count while cache-write
// and output take a far larger share once priced, so both columns must render.
// It also pins the two review findings from PR #134 — the per-model split and
// the named unpriced bucket.
func TestWriteUsageText_ShowsTokenAndCostViews_When_TheyDisagree(t *testing.T) {
	var buf bytes.Buffer
	if err := writeUsageText(&buf, usageReport(), 0, 0); err != nil {
		t.Fatalf("writeUsageText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"calls=4", "sessions=2", "%tokens", "%spend",
		"input", "cache-write", "cache-read", "output",
		"reads per written token", "thinking=25.0% of output",
		"claude-opus-5", "claude-sonnet-5", // per-model split makes the pooled dollar figure auditable
		"unpriced: 1 calls", // an unpriced model is named, never absorbed
		apiusage.PricedAt,   // a spend figure with undated prices invites silent staleness
		"/usage",            // the reconciliation instruction must be on the page
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

// TestHumanUSD_KeepsCentsOnlyWhereTheyMatter pins the money renderer: cents
// under $100, whole dollars above, so a corpus total does not print six
// meaningless digits.
func TestHumanUSD_KeepsCentsOnlyWhereTheyMatter(t *testing.T) {
	cases := map[float64]string{0: "$0.00", 6.25: "$6.25", 99.994: "$99.99", 1234.56: "$1235"}
	for in, want := range cases {
		if got := humanUSD(in); got != want {
			t.Errorf("humanUSD(%v) = %q, want %q", in, got, want)
		}
	}
}
