package main

import (
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
	saved := CLI.Report
	t.Cleanup(func() { CLI.Report = saved })
	r := &CLI.Report
	r.Data, r.Format, r.Limit, r.Lens = data, format, -1, "tool"
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

// TestLegacyLenslessRowWithForeignTokensIsNotListedAsFixed: a row with no lens
// whose tokens do not exist in this corpus (likely another lens) must not read
// as a fix that took the motif to zero. One whose tokens exist but no longer
// co-occur is a real outcome and is listed.
func TestLegacyLenslessRowWithForeignTokensIsNotListedAsFixed(t *testing.T) {
	data := writeSessionFixture(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, e := range []fixes.Entry{
		{Motif: "sh:git_add,sh:git_commit -q -F", Fix: "foreign", AddedAt: at, BaselineBurn: 100},
		{Motif: "Write,Read", Fix: "gone", AddedAt: at, BaselineBurn: 100},
	} {
		if err := fixes.Append(fixes.Path(data), e); err != nil {
			t.Fatal(err)
		}
	}
	out := runReportSinceFixes(t, data, "text")
	if strings.Contains(out, "git_add") {
		t.Errorf("foreign-lens legacy row listed as fixed:\n%s", out)
	}
	if row := lineWith(t, out, "Write ⇝ Read"); !strings.Contains(row, "n=0") {
		t.Errorf("co-occurrence gone but tokens known must list at n=0: %s", row)
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
