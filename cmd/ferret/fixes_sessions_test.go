package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/fixes"
)

// writeSessionFixture writes a tool-lens corpus with three motifs of different
// footprints: Read→Edit in 25 sessions (clears report's min-support of 20, so
// it is a finding), Grep→Write in 5 sessions (6 occurrences; below min-support,
// not a finding) and Glob→Bash in 3 sessions.
func writeSessionFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeEraManifest(t, dir, event.SchemaVersion)
	w, err := event.NewWriter(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	seq := 0
	emit := func(sess string, actions ...string) {
		for _, a := range actions {
			seq++
			ev := event.Event{Seq: seq, Session: sess, Project: "proj", Kind: event.KindTool,
				Action: a, Status: event.StatusOK, Bytes: 1000}
			if err := w.Write(&ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := range 25 {
		emit(fmt.Sprintf("read%02d", i), "Read", "Edit")
	}
	for i := range 5 {
		if i == 0 {
			emit("grep00", "Grep", "Write", "Grep", "Write") // two occurrences in one session
			continue
		}
		emit(fmt.Sprintf("grep%02d", i), "Grep", "Write")
	}
	for i := range 3 {
		emit(fmt.Sprintf("glob%02d", i), "Glob", "Bash")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runFixesAdd(t *testing.T, data, motif string) {
	t.Helper()
	saved := CLI.Fixes.Add
	t.Cleanup(func() { CLI.Fixes.Add = saved })
	CLI.Fixes.Add.Data, CLI.Fixes.Add.Lens, CLI.Fixes.Add.Motif = data, "tool", motif
	CLI.Fixes.Add.Fix, CLI.Fixes.Add.Disposition = "sdlc-xxx", fixes.DispositionFix
	captureStdout(t, cmdFixesAdd)
}

func runReportSinceFixes(t *testing.T, data, format string) string {
	t.Helper()
	return runReportLimit(t, data, format, -1)
}

func runReportLimit(t *testing.T, data, format string, limit int) string {
	t.Helper()
	saved := CLI.Report
	t.Cleanup(func() { CLI.Report = saved })
	r := &CLI.Report
	r.Data, r.Format, r.Limit, r.Lens = data, format, limit, "tool"
	r.MinSupport, r.MaxGap, r.MaxLen, r.Order, r.Top = reportMinSupport, reportMaxGap, reportMaxLen, reportOrder, reportTop
	r.SinceFixes = true
	return captureStdout(t, cmdReport)
}

// lineWith returns the first output line containing needle.
func lineWith(t *testing.T, out, needle string) string {
	t.Helper()
	for ln := range strings.SplitSeq(out, "\n") {
		if strings.Contains(ln, needle) {
			return ln
		}
	}
	t.Fatalf("no line with %q in:\n%s", needle, out)
	return ""
}

// TestFixesAddRecordsSessionBaseline: fixes add writes baselineSessions and
// baselineCount into the ledger row, counted from the corpus — including for a
// motif below report's min-support, which is not a finding.
func TestFixesAddRecordsSessionBaseline(t *testing.T) {
	data := writeSessionFixture(t)
	runFixesAdd(t, data, "Grep,Write")

	got, err := fixes.Load(fixes.Path(data))
	if err != nil || len(got) != 1 {
		t.Fatalf("Load = %v, %v", got, err)
	}
	e := got[0]
	if e.BaselineSessions != 5 || e.BaselineCount != 6 {
		t.Errorf("baseline sessions/count = %d/%d, want 5/6", e.BaselineSessions, e.BaselineCount)
	}
	if e.BaselineBurn == 0 {
		t.Error("a below-min-support motif must still record its counted burn, not 0")
	}
}

// TestReportSinceFixesShowsSessionDelta: a fix recorded at 64 sessions reads
// "sess 64s→25s ↓" beside its burn delta; the burn arrow and the session arrow
// are independent (burn rose here, sessions fell).
func TestReportSinceFixesShowsSessionDelta(t *testing.T) {
	data := writeSessionFixture(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := fixes.Append(fixes.Path(data), fixes.Entry{Motif: "Read,Edit", Fix: "sdlc-xxx", AddedAt: at,
		BaselineBurn: 1, Lens: "tool", BaselineSessions: 64, BaselineCount: 80}); err != nil {
		t.Fatal(err)
	}
	row := lineWith(t, runReportSinceFixes(t, data, "text"), "Read ⇝ Edit")
	if !strings.Contains(row, "sess 64s→25s ↓") {
		t.Errorf("row lacks the session delta: %s", row)
	}
	if !strings.Contains(row, "burn 1→") || !strings.Contains(row, "↑ sess") {
		t.Errorf("burn must keep its own arrow (↑ here): %s", row)
	}
}

// TestReportJSONCapsFixedGoneAtLimit: --format json --limit N cuts the
// fixedGone rows to N, reports the full count, and marks the payload truncated;
// an unlimited run keeps every row and stays untruncated.
func TestReportJSONCapsFixedGoneAtLimit(t *testing.T) {
	data := writeSessionFixture(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, m := range []string{"Grep,Write", "Glob,Bash", "Write,Read"} {
		e := fixes.Entry{Motif: m, Fix: "f", AddedAt: at, BaselineBurn: 100, Lens: "tool", BaselineSessions: 9}
		if err := fixes.Append(fixes.Path(data), e); err != nil {
			t.Fatal(err)
		}
	}
	type payload struct {
		Truncated      bool             `json:"truncated"`
		FixedGone      []map[string]any `json:"fixedGone"`
		FixedGoneTotal int              `json:"fixedGoneTotal"`
	}
	decode := func(limit int) payload {
		var p payload
		if err := json.Unmarshal([]byte(runReportLimit(t, data, fmtJSON, limit)), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := decode(2); len(p.FixedGone) != 2 || p.FixedGoneTotal != 3 || !p.Truncated {
		t.Errorf("limit 2: rows=%d total=%d truncated=%v, want 2/3/true", len(p.FixedGone), p.FixedGoneTotal, p.Truncated)
	}
	if p := decode(-1); len(p.FixedGone) != 3 || p.FixedGoneTotal != 3 || p.Truncated {
		t.Errorf("unlimited: rows=%d total=%d truncated=%v, want 3/3/false", len(p.FixedGone), p.FixedGoneTotal, p.Truncated)
	}
}

// TestGoneRowWithUnknownTokensClaimsNoDelta: a row (lens-less, or lens-tagged
// like "Edit!,Read" checked with --no-mark-fail) whose tokens do not exist in
// this corpus must not read as a fix that took the motif to zero: it is listed
// with a note and no burn or session delta. One whose tokens exist but no longer
// co-occur is a real outcome and shows its delta.
func TestGoneRowWithUnknownTokensClaimsNoDelta(t *testing.T) {
	data := writeSessionFixture(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, e := range []fixes.Entry{
		{Motif: "sh:git_add,sh:git_commit -q -F", Fix: "foreign", AddedAt: at, BaselineBurn: 100},
		{Motif: "Edit!,Read", Fix: "markfail", AddedAt: at, BaselineBurn: 100, Lens: "tool", BaselineSessions: 64},
		{Motif: "Write,Read", Fix: "gone", AddedAt: at, BaselineBurn: 100, Lens: "tool", BaselineSessions: 9},
	} {
		if err := fixes.Append(fixes.Path(data), e); err != nil {
			t.Fatal(err)
		}
	}
	out := runReportSinceFixes(t, data, "text")
	for _, motif := range []string{"git_add", "Edit! ⇝ Read"} {
		row := lineWith(t, out, motif)
		if !strings.Contains(row, "tokens not in this corpus") || strings.Contains(row, "↓") || strings.Contains(row, "s→") {
			t.Errorf("unknown-token row must carry the note and no delta: %s", row)
		}
	}
	if row := lineWith(t, out, "Write ⇝ Read"); !strings.Contains(row, "n=0") || !strings.Contains(row, "sess 9s→0s ↓") {
		t.Errorf("co-occurrence gone but tokens known must list at n=0 with its delta: %s", row)
	}
}

// TestReportTextGoneRowsKeepTheirOwnLimit: the findings spend the sink's whole
// --limit budget, yet the gone rows still print under their heading, capped at
// the limit with an explicit "+K more" line.
func TestReportTextGoneRowsKeepTheirOwnLimit(t *testing.T) {
	data := writeSessionFixture(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, m := range []string{"Grep,Write", "Glob,Bash", "Write,Read"} {
		e := fixes.Entry{Motif: m, Fix: "f", AddedAt: at, BaselineBurn: 100, Lens: "tool", BaselineSessions: 9}
		if err := fixes.Append(fixes.Path(data), e); err != nil {
			t.Fatal(err)
		}
	}
	out := runReportLimit(t, data, "text", 1) // one finding row spends the whole sink budget
	_, under, found := strings.Cut(out, "no longer in findings=3")
	if !found || strings.Count(under, "✓ n=") != 1 || !strings.Contains(under, "+2 more fixed motifs") {
		t.Errorf("want 1 gone row and '+2 more' under the heading:\n%s", out)
	}
}

// TestReportListsFixedMotifsNoLongerFindings: a ledgered motif below min-support
// is not a finding, yet it is printed under its own heading with baseline→now
// sessions counted from the corpus. A row written before baselineSessions
// existed prints there with no session delta.
func TestReportListsFixedMotifsNoLongerFindings(t *testing.T) {
	data := writeSessionFixture(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, e := range []fixes.Entry{
		{Motif: "Grep,Write", Fix: "a", AddedAt: at, BaselineBurn: 100, Lens: "tool", BaselineSessions: 30, BaselineCount: 40},
		{Motif: "Glob,Bash", Fix: "b", AddedAt: at, BaselineBurn: 100, Lens: "tool"}, // pre-change row
	} {
		if err := fixes.Append(fixes.Path(data), e); err != nil {
			t.Fatal(err)
		}
	}
	out := runReportSinceFixes(t, data, "text")

	if !strings.Contains(out, "no longer in findings=2") {
		t.Errorf("missing the own heading with 2 rows:\n%s", out)
	}
	_, under, found := strings.Cut(out, "no longer in findings")
	if !found || !strings.Contains(under, "Grep ⇝ Write") {
		t.Errorf("Grep→Write must be listed under the heading:\n%s", out)
	}
	grep := lineWith(t, out, "Grep ⇝ Write")
	if !strings.Contains(grep, "sess 30s→5s ↓") || !strings.Contains(grep, "n=6") {
		t.Errorf("below-support row lacks baseline→now sessions: %s", grep)
	}
	glob := lineWith(t, out, "Glob ⇝ Bash")
	if !strings.Contains(glob, "[fixed 2026-10-01 burn") {
		t.Errorf("pre-change row must print its burn delta: %s", glob)
	}
	if strings.Contains(glob, "s→") {
		t.Errorf("pre-change row printed a session delta: %s", glob)
	}

	// json carries the same rows.
	js := runReportSinceFixes(t, data, "json")
	if !strings.Contains(js, `"fixedGone"`) || !strings.Contains(js, `"baselineSessions": 30`) {
		t.Errorf("json lacks fixedGone rows:\n%s", js)
	}
}
