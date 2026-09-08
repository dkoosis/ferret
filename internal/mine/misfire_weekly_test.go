package mine

import (
	"fmt"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
)

// weeklyFailEv builds one fail|cfail candidate event with a timestamp,
// mirroring failEv (misfire_reasons_test.go) plus the Time this bucketing
// needs.
func weeklyFailEv(session, action, errText string, t time.Time) event.Event {
	return event.Event{
		Session: session, Kind: event.KindTool, Action: action,
		Status: event.StatusFail, Err: errText, Time: t,
	}
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

// TestMineReasonsByWeek_DecaysToExplicitZero_Not_Absent is the bead's named
// AC test: a reason that decays to zero in the final week must print 0 in
// that column, not be missing from it — the SendMessage 168/129/43/0 shape
// the bead's Story names.
func TestMineReasonsByWeek_DecaysToExplicitZero_Not_Absent(t *testing.T) {
	w1 := mustParse(t, "2026-08-03T10:00:00Z") // 2026-W32
	w2 := mustParse(t, "2026-08-10T10:00:00Z") // 2026-W33
	w3 := mustParse(t, "2026-08-24T10:00:00Z") // 2026-W35 — w4 (W34) has zero events

	events := make([]event.Event, 0, 6)
	for range 3 {
		events = append(events, weeklyFailEv("s1", "SendMessage", "json: unexpected end of JSON input", w1))
	}
	for range 2 {
		events = append(events, weeklyFailEv("s2", "SendMessage", "json: unexpected end of JSON input", w2))
	}
	// w3 (W35) has a SendMessage failure, but a DIFFERENT reason — it extends
	// the SendMessage week axis without adding to the json-parse reason's
	// count, so that reason's W35 bucket must read as an explicit 0.
	events = append(events, weeklyFailEv("s3", "SendMessage", "connection reset by peer", w3))

	rep := MineReasonsByWeek(events, "SendMessage")
	if len(rep.Keys) != 1 {
		t.Fatalf("keys = %+v, want 1 (SendMessage)", rep.Keys)
	}
	kr := rep.Keys[0]
	// Two distinct reasons under SendMessage: the json-parse one (5 total,
	// ranks first) and the W35 connection-reset one (1 total) that exists
	// only to extend the week axis past the decay.
	if len(kr.Reasons) != 2 {
		t.Fatalf("reasons = %+v, want 2", kr.Reasons)
	}
	jsonReason := kr.Reasons[0]
	if jsonReason.Total != 5 {
		t.Fatalf("top reason = %+v, want the json-parse reason (total=5) ranked first", jsonReason)
	}
	weeks := jsonReason.Weeks
	if len(weeks) != 4 {
		t.Fatalf("weeks = %+v, want 4 (W32..W35, no gaps)", weeks)
	}
	last := weeks[len(weeks)-1]
	if last.Week != "2026-W35" {
		t.Fatalf("last week label = %q, want 2026-W35", last.Week)
	}
	if last.Count != 0 || last.Uncaptured {
		t.Errorf("last bucket = %+v, want Count=0 Uncaptured=false — an explicit zero, not absent or unknown", last)
	}
	if weeks[0].Count != 3 || weeks[1].Count != 2 {
		t.Errorf("weeks[0..1] = %+v, want counts 3,2", weeks[:2])
	}
	// The gap week (W34) has no SendMessage failures of any reason — still an
	// explicit 0, not a missing entry.
	if weeks[2].Week != "2026-W34" || weeks[2].Count != 0 || weeks[2].Uncaptured {
		t.Errorf("weeks[2] = %+v, want week=2026-W34 count=0 uncaptured=false", weeks[2])
	}
}

// TestMineReasonsByWeek_MarksPreCaptureWeek_As_Uncaptured pins the bead's
// Rule: a week whose failed events for a key carry no captured Err text at
// all must be marked uncaptured, never printed as a genuine 0 — a
// pre-ferret-54q ingest window must not read as "no failures".
func TestMineReasonsByWeek_MarksPreCaptureWeek_As_Uncaptured(t *testing.T) {
	precapture := mustParse(t, "2026-08-03T10:00:00Z") // 2026-W32 — no Err captured
	captured := mustParse(t, "2026-08-10T10:00:00Z")   // 2026-W33 — Err captured

	events := []event.Event{
		{Session: "s1", Kind: event.KindTool, Action: "Edit", Status: event.StatusFail, Time: precapture}, // no Err
		weeklyFailEv("s2", "Edit", "File has not been read yet.", captured),
	}
	rep := MineReasonsByWeek(events, "Edit")
	if len(rep.Keys) != 1 {
		t.Fatalf("keys = %+v, want 1", rep.Keys)
	}
	weeks := rep.Keys[0].Reasons[0].Weeks
	if len(weeks) != 2 {
		t.Fatalf("weeks = %+v, want 2", weeks)
	}
	if weeks[0].Week != "2026-W32" || !weeks[0].Uncaptured {
		t.Errorf("weeks[0] = %+v, want week=2026-W32 uncaptured=true", weeks[0])
	}
	if weeks[0].Count != 0 {
		t.Errorf("weeks[0].Count = %d, want 0 alongside Uncaptured=true (no fabricated count)", weeks[0].Count)
	}
	if weeks[1].Week != "2026-W33" || weeks[1].Uncaptured || weeks[1].Count != 1 {
		t.Errorf("weeks[1] = %+v, want week=2026-W33 uncaptured=false count=1", weeks[1])
	}
	if rep.Failed != 2 || rep.Uncaptured != 1 {
		t.Errorf("Failed/Uncaptured = %d/%d, want 2/1", rep.Failed, rep.Uncaptured)
	}
}

// TestMineReasonsByWeek_KeyFilterScopesWeeksToOneKey checks --key scoping
// carries through to the weekly report the same way it does for MineReasons.
func TestMineReasonsByWeek_KeyFilterScopesWeeksToOneKey(t *testing.T) {
	ts := mustParse(t, "2026-08-03T10:00:00Z")
	events := []event.Event{
		weeklyFailEv("s1", "Edit", "boom edit", ts),
		weeklyFailEv("s1", "Read", "boom read", ts),
	}
	rep := MineReasonsByWeek(events, "Edit")
	if len(rep.Keys) != 1 || rep.Keys[0].Key != "Edit" {
		t.Fatalf("keys = %+v, want only Edit", rep.Keys)
	}
	if rep.Failed != 1 {
		t.Errorf("failed = %d, want 1 (Read excluded by --key)", rep.Failed)
	}
}

// TestIsoWeek_MatchesGoStdlibISOWeek pins the label format against Go's own
// time.ISOWeek, so a format typo can't silently drift from the definition.
func TestIsoWeek_MatchesGoStdlibISOWeek(t *testing.T) {
	ts := mustParse(t, "2026-08-24T00:00:00Z")
	year, week := ts.ISOWeek()
	want := fmt.Sprintf("%04d-W%02d", year, week)
	if got := isoWeek(ts); got != want {
		t.Errorf("isoWeek = %q, want %q", got, want)
	}
}
