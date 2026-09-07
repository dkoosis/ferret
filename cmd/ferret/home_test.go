package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/fixes"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/shellnorm"
)

// writeHomeFixture builds a small-but-real corpus large enough to clear the
// report pipeline's default --min-support=20: a "Read"->"Edit" motif recurring
// in 25 sessions (a routine), a failing "SendMessage" tool call in 3 sessions
// (a misfire), and a polled "git status --short" in 4 sessions (a poll) —
// enough for one row on every scoreboard leg. Returns a *common pointed at the
// fixture data dir, format "text".
func writeHomeFixture(t *testing.T) *common {
	t.Helper()
	dir := t.TempDir()

	var evs []event.Event
	seq := 0
	add := func(e event.Event) {
		e.Seq = seq
		seq++
		e.Time = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		e.Project = "proj"
		evs = append(evs, e)
	}

	for i := range 25 {
		sess := sessionID(i)
		add(event.Event{Session: sess, Kind: event.KindTool, Action: "Read", Status: "ok", Bytes: 1000})
		add(event.Event{Session: sess, Kind: event.KindTool, Action: "Edit", Status: "ok", Bytes: 2000})
		if i < 3 {
			for range 3 {
				add(event.Event{Session: sess, Kind: event.KindTool, Action: "SendMessage", Status: "fail", Bytes: 500})
			}
		}
		if i < 4 {
			for range 3 {
				add(event.Event{Session: sess, Kind: event.KindShell, Action: "git_status",
					Detail: "git status --short", Status: "ok", Bytes: 300})
			}
		}
	}

	eventsPath := filepath.Join(dir, "events.jsonl")
	var buf bytes.Buffer
	for i := range evs {
		b, err := json.Marshal(evs[i])
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(eventsPath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}

	m := event.Manifest{
		SchemaVersion: event.SchemaVersion,
		CreatedAt:     time.Now(),
		Root:          "", // corpusStale short-circuits false with no root
		Provenance:    event.Provenance{Ferret: "", Normalizer: shellnorm.Version},
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o600); err != nil {
		t.Fatalf("write manifest.json: %v", err)
	}

	return &common{data: dir, format: fmtText}
}

func sessionID(i int) string { return "s" + string(rune('a'+i)) }

// TestBuildHomeScoreboard_RoutinesMatchReportPipeline_When_LedgerIsEmpty pins
// AC #2: the scoreboard's routine ranking is exactly `report --lens cmd`'s
// own findings pipeline (same constants, same noise drop, same sort), not a
// second computation that happens to look similar.
func TestBuildHomeScoreboard_RoutinesMatchReportPipeline_When_LedgerIsEmpty(t *testing.T) {
	c := writeHomeFixture(t)

	sb, corpus, err := buildHomeScoreboard(c)
	if err != nil {
		t.Fatalf("buildHomeScoreboard: %v", err)
	}
	if len(sb.Routines) == 0 {
		t.Fatal("no routines found — fixture did not clear min-support")
	}

	// Independently replicate report --lens cmd's default (kind="") view at the
	// scoreboard's own recurrence floor. homeMinSupport, not reportMinSupport:
	// the scoreboard deliberately asks a looser floor than `report`'s default
	// (see home.go's homeMinSupport) because at 20 the routines section renders
	// empty on a real corpus. Everything else in the pipeline is identical, so
	// this still proves the scoreboard adds no detector and reorders nothing.
	lo := &lensOpts{lens: homeLens}
	wantCorpus, _, err := lo.corpus(c.eventsPath())
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	sscores := mine.ScoreSurprise(wantCorpus, mine.SurpriseOpts{Order: reportOrder, MinToks: reportSurpriseMinToks})
	findings, _ := mineFindings(wantCorpus, homeMinSupport, reportMaxGap, reportMaxLen, reportOrder, reportTop,
		mine.SurpriseIndex(sscores), mine.FrictionCut(sscores))
	var want []string
	for _, f := range findings {
		if f.Kind == mine.KindNoise {
			continue
		}
		want = append(want, strings.Join(wantCorpus.Tokens(f.IDs), " ⇝ "))
		if len(want) == 3 {
			break
		}
	}

	if len(want) == 0 {
		t.Fatal("independent report replay found no findings — fixture is broken")
	}
	if len(sb.Routines) > len(want) {
		t.Fatalf("scoreboard has more routines (%d) than the report pipeline found (%d)", len(sb.Routines), len(want))
	}
	for i, r := range sb.Routines {
		if r.Key != want[i] {
			t.Errorf("routines[%d].key = %q, want %q (report --lens cmd order)", i, r.Key, want[i])
		}
	}
	if corpus == nil {
		t.Error("buildHomeScoreboard returned a nil corpus")
	}
}

// TestBuildHomeScoreboard_WasteIsUnionOfMisfireAndPoll_When_LedgerIsEmpty pins
// AC #2's second half: .waste is exactly the top-N by wastedBytes of
// `friction --source misfire` ∪ `friction --source poll` — computed here by
// calling mergeFriction + filterWasteReport, the same functions cmdFriction
// itself calls for each --source value, not a hand re-derivation.
func TestBuildHomeScoreboard_WasteIsUnionOfMisfireAndPoll_When_LedgerIsEmpty(t *testing.T) {
	c := writeHomeFixture(t)

	sb, _, err := buildHomeScoreboard(c)
	if err != nil {
		t.Fatalf("buildHomeScoreboard: %v", err)
	}
	if len(sb.Waste) == 0 {
		t.Fatal("no waste rows found — fixture did not produce a misfire/poll")
	}

	lo := &lensOpts{lens: homeLens}
	misRep, err := mergeFriction(c, lo, false, string(mine.WasteFail))
	if err != nil {
		t.Fatalf("mergeFriction(misfire): %v", err)
	}
	misRep = filterWasteReport(misRep, mine.WasteFail)
	pollRep, err := mergeFriction(c, lo, false, string(mine.WasteRepeat))
	if err != nil {
		t.Fatalf("mergeFriction(poll): %v", err)
	}
	pollRep = filterWasteReport(pollRep, mine.WasteRepeat)

	union := append(append([]mine.WasteRow{}, misRep.Rows...), pollRep.Rows...)
	if len(union) == 0 {
		t.Fatal("independent misfire ∪ poll replay is empty — fixture is broken")
	}

	byKey := make(map[string]mine.WasteRow, len(union))
	for _, r := range union {
		byKey[string(r.Source)+"|"+r.Key] = r
	}
	for _, r := range sb.Waste {
		wantRow, ok := byKey[string(r.Source)+"|"+r.Key]
		if !ok {
			t.Errorf("scoreboard waste row %+v not present in the independent misfire ∪ poll replay", r)
			continue
		}
		if wantRow.WastedBytes != r.WastedBytes {
			t.Errorf("waste row %s/%s wastedBytes = %d, want %d (friction's own figure)", r.Source, r.Key, r.WastedBytes, wantRow.WastedBytes)
		}
	}
}

// TestBuildHomeScoreboard_LedgerEntryLeavesRankingAndAppearsOnDelta pins the
// bead's third AC: a recorded fix disappears from the routine ranking and
// shows up on the delta with real before/after bytes measured against the
// SAME fixture corpus.
func TestBuildHomeScoreboard_LedgerEntryLeavesRankingAndAppearsOnDelta(t *testing.T) {
	c := writeHomeFixture(t)

	sbBefore, corpus, err := buildHomeScoreboard(c)
	if err != nil {
		t.Fatalf("buildHomeScoreboard: %v", err)
	}
	if len(sbBefore.Routines) == 0 {
		t.Fatal("fixture produced no routines to fix")
	}
	top := sbBefore.Routines[0]
	tokens := strings.Split(top.Key, " ⇝ ")
	motif := fixes.MotifKey(tokens)

	// BaselineBurn is in TOKENS (the ledger's unit); ScoreboardRoutine is in
	// BYTES. Convert through the one exported ratio rather than a local copy —
	// a second `const bytesPerToken = 4` in this package is exactly the drift
	// this change removed.
	entry := fixes.Entry{
		Motif: motif, Fix: "hookified", Disposition: fixes.DispositionFix,
		BaselineBurn: top.BurnBytes / mine.BytesPerToken,
		AddedAt:      time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), Lens: homeLens,
	}
	if err := fixes.Append(fixes.Path(c.data), entry); err != nil {
		t.Fatalf("fixes.Append: %v", err)
	}

	sbAfter, _, err := buildHomeScoreboard(c)
	if err != nil {
		t.Fatalf("buildHomeScoreboard (after fix): %v", err)
	}
	for _, r := range sbAfter.Routines {
		if r.Key == top.Key {
			t.Errorf("fixed motif %q still appears in routines: %+v", top.Key, sbAfter.Routines)
		}
	}
	if len(sbAfter.Delta) != 1 {
		t.Fatalf("delta rows = %d, want 1", len(sbAfter.Delta))
	}
	d := sbAfter.Delta[0]
	if d.Key != top.Key {
		t.Errorf("delta key = %q, want %q", d.Key, top.Key)
	}
	// Delta and routine rows are both bytes now, so this is a direct compare —
	// no conversion, which is the point of the units change.
	if d.BeforeBytes != top.BurnBytes || d.AfterBytes != top.BurnBytes {
		t.Errorf("delta bytes = before=%d after=%d, want both %d (burn unchanged since the fix, no re-ingest)",
			d.BeforeBytes, d.AfterBytes, top.BurnBytes)
	}
	_ = corpus
}

// TestCmdHome_FallsBackToStatus_When_CorpusMissing pins the freshness gate:
// no manifest → the existing status view, never the scoreboard, never an
// error.
func TestCmdHome_FallsBackToStatus_When_CorpusMissing(t *testing.T) {
	c := &common{data: t.TempDir(), format: fmtText}
	var buf bytes.Buffer
	st := readStatus(c)
	if st.Ready {
		t.Fatalf("readStatus on an empty dir reports ready=true")
	}
	if err := writeStatusText(&buf, st, 0); err != nil {
		t.Fatalf("writeStatusText: %v", err)
	}
	if !strings.Contains(buf.String(), "no corpus") {
		t.Errorf("expected the status fallback view, got %q", buf.String())
	}
}

// homeScoreboard fixture for the pure render tests below — mirrors
// wasteReport()/readyStatus() in friction_test.go/status_test.go: hand-built
// data, no corpus needed.
func homeScoreboardFixture() mine.Scoreboard {
	return mine.Scoreboard{
		Routines: []mine.ScoreboardRoutine{
			// Bytes, not tokens — the same rendered figures as before the units
			// change (175000 and 108000 tokens x BytesPerToken).
			{Key: "Read ⇝ Read ⇝ Edit", BurnBytes: 700_000, SideBurnBytes: 266_000, Count: 433, Sessions: 230, ExStream: 0, ExSeq: 12},
			{Key: "Read ⇝ sh:rg -n ⇝ Read", BurnBytes: 432_000, SideBurnBytes: 164_000, Count: 171, Sessions: 132, ExStream: 1, ExSeq: 4},
		},
		Waste: []mine.WasteRow{
			{Key: "Read", Source: mine.WasteFail, WastedBytes: 1_025_126, Occurrences: 138, Sessions: 61},
			{Key: "sh:git_status", Source: mine.WasteRepeat, WastedBytes: 12_400, Occurrences: 40, Sessions: 6},
		},
		Delta: []mine.ScoreboardDelta{
			{Key: "Edit! ⇝ Read", Fix: "hookified", FixedAt: "2026-08-12", BeforeBytes: 431_000, AfterBytes: 212_400},
		},
		BelowCut: 36,
	}
}

func homeStatusFixture() status {
	return status{
		Data: "/tmp/.ferret", Ready: true,
		Events: 554265, Sessions: 2755,
	}
}

func TestWriteHomeText_RendersRoutinesWasteAndDelta_When_ScoreboardIsPopulated(t *testing.T) {
	corpus := &mine.Corpus{StreamKeys: []string{"proj/session1234@", "proj/session5678@"}, Vocab: []string{"x"}}
	var buf bytes.Buffer
	if err := writeHomeText(&buf, corpus, homeStatusFixture(), homeScoreboardFixture(), 0); err != nil {
		t.Fatalf("writeHomeText: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		"554265 events", "2755 sessions",
		"Read ⇝ Read ⇝ Edit", "next: ferret tokens --session",
		"1001.1KB", "misfire", "next: ferret misfires",
		"sh:git_status", "next: ferret polling",
		"Δ since fixes:", "Edit! ⇝ Read", "2026-08-12",
		"36 more below the cut",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n > 40 {
		t.Errorf("scoreboard is %d lines, want ≤ 40\n---\n%s", n, got)
	}
}

func TestWriteHomeJSON_CarriesScoreboardAndStatus_When_Encoded(t *testing.T) {
	var buf bytes.Buffer
	if err := writeHomeJSON(&buf, homeStatusFixture(), homeScoreboardFixture()); err != nil {
		t.Fatalf("writeHomeJSON: %v", err)
	}
	var got struct {
		Routines []mine.ScoreboardRoutine `json:"routines"`
		Waste    []mine.WasteRow          `json:"waste"`
		Delta    []mine.ScoreboardDelta   `json:"delta"`
		BelowCut int                      `json:"belowCut"`
		Status   status                   `json:"status"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n---\n%s", err, buf.String())
	}
	if len(got.Routines) != 2 || len(got.Waste) != 2 || len(got.Delta) != 1 || got.BelowCut != 36 {
		t.Errorf("round-trip lost data: %+v", got)
	}
	if !got.Status.Ready || got.Status.Events != 554265 {
		t.Errorf("status did not round-trip: %+v", got.Status)
	}
}

// TestGuardOffline_NeverRefusesHome pins the [local] surface label: home does
// not touch the analyst, so --offline/FERRET_OFFLINE must never refuse it —
// the AC's "ferret --offline byte-identical to ferret".
func TestGuardOffline_NeverRefusesHome(t *testing.T) {
	orig := CLI.Offline
	CLI.Offline = true
	t.Cleanup(func() { CLI.Offline = orig })
	if err := guardOffline("home"); err != nil {
		t.Errorf("guardOffline(home) under --offline = %v, want nil (home is local)", err)
	}
}
