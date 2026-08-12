package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/mine"
)

// pollingEv is the writer tests' fixture: a shell event carrying the raw
// (truncated) command text MinePolling dedups on.
func pollingEv(session, action, command string) event.Event {
	return event.Event{Session: session, Kind: event.KindShell, Action: action, Detail: command}
}

// pollingFixture builds a two-command corpus: `loto inbox` polled in two
// sessions, `git status --short` polled in one.
func pollingFixture() mine.PollingReport {
	events := []event.Event{
		pollingEv("s1", "loto", "loto inbox"), pollingEv("s1", "loto", "loto inbox"),
		pollingEv("s2", "loto", "loto inbox"), pollingEv("s2", "loto", "loto inbox"),
		pollingEv("s3", "git_status", "git status --short"),
		pollingEv("s3", "git_status", "git status --short"),
	}
	return mine.MinePolling(events)
}

// TestWritePollingText_ShowsRankAndColumns_When_CommandsPoll renders the table
// and checks every column a reader ranks on surfaces.
func TestWritePollingText_ShowsRankAndColumns_When_CommandsPoll(t *testing.T) {
	var buf bytes.Buffer
	if err := writePollingText(&buf, pollingFixture(), 0, 0); err != nil {
		t.Fatalf("writePollingText: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		"polling rows=2", "sessions=3",
		"max=2", "repeats=4", "score=8", "sh:loto", `"loto inbox"`,
		"sh:git_status", `"git status --short"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text output missing %q\n---\n%s", want, got)
		}
	}
}

// TestWritePollingText_CapsRows_When_LimitSet checks the sink's row limit is
// wired — the text writer's cap comes from out.NewSink, not a manual slice.
func TestWritePollingText_CapsRows_When_LimitSet(t *testing.T) {
	rep := pollingFixture()
	if len(rep.Rows) != 2 {
		t.Fatalf("fixture rows = %d, want 2", len(rep.Rows))
	}
	var buf bytes.Buffer
	if err := writePollingText(&buf, rep, 1, 0); err != nil {
		t.Fatalf("writePollingText: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, `"loto inbox"`) {
		t.Errorf("top row missing under limit=1\n---\n%s", got)
	}
	if strings.Contains(got, `"git status --short"`) {
		t.Errorf("second row rendered despite limit=1\n---\n%s", got)
	}
}

// TestWritePollingJSON_RoundTrips_When_ReportHasRows guards the
// analyst-ingestable shape: rows, the corpus session denominator, and the
// AX truncation contract keys.
func TestWritePollingJSON_RoundTrips_When_ReportHasRows(t *testing.T) {
	var buf bytes.Buffer
	if err := writePollingJSON(&buf, pollingFixture(), 0); err != nil {
		t.Fatalf("writePollingJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	for _, key := range []string{"rows", "sessions", keyTotal} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing key %q", key)
		}
	}
	if total, ok := got[keyTotal].(float64); !ok || total != 2 {
		t.Errorf("total = %v, want 2", got[keyTotal])
	}
	if sessions, ok := got["sessions"].(float64); !ok || sessions != 3 {
		t.Errorf("sessions = %v, want 3", got["sessions"])
	}
	rows, ok := got["rows"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("rows = %v, want 2", got["rows"])
	}
	top, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("row 0 = %v, want an object", rows[0])
	}
	for _, key := range []string{"command", "key", "totalRepeats", "sessions", "maxPerSession", "score"} {
		if _, ok := top[key]; !ok {
			t.Errorf("row JSON missing key %q; got %v", key, top)
		}
	}
}

// TestWritePollingJSON_CapsRows_When_LimitSet guards the pre-capping
// writeMisfiresJSON mirrors: the cap trims rows but total stays uncapped, and
// the truncated flag flips.
func TestWritePollingJSON_CapsRows_When_LimitSet(t *testing.T) {
	rep := pollingFixture()
	var buf bytes.Buffer
	if err := writePollingJSON(&buf, rep, 1); err != nil {
		t.Fatalf("writePollingJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if rows, ok := got["rows"].([]any); !ok || len(rows) != 1 {
		t.Errorf("rows len = %v, want 1 (capped)", got["rows"])
	}
	if total, ok := got[keyTotal].(float64); !ok || int(total) != len(rep.Rows) {
		t.Errorf("total = %v, want uncapped %d", got[keyTotal], len(rep.Rows))
	}
	if truncated, ok := got[keyTruncated].(bool); !ok || !truncated {
		t.Errorf("truncated = %v, want true", got[keyTruncated])
	}
}

// TestWritePollingJSON_ReportsUntruncated_When_LimitUnset is the negative twin:
// with no limit, the truncation flag must not claim rows were dropped.
func TestWritePollingJSON_ReportsUntruncated_When_LimitUnset(t *testing.T) {
	var buf bytes.Buffer
	if err := writePollingJSON(&buf, pollingFixture(), 0); err != nil {
		t.Fatalf("writePollingJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if truncated, ok := got[keyTruncated].(bool); !ok || truncated {
		t.Errorf("truncated = %v, want false", got[keyTruncated])
	}
}

// TestCmdPolling_LoadsRealArtifact_When_EventsFileExists is the round-trip
// check (mirrors TestCmdMisfires_LoadsRealArtifact_When_EventsFileExists):
// write an events.jsonl and run the same load-then-mine path cmdPolling uses
// (cmdPolling itself writes to os.Stdout).
func TestCmdPolling_LoadsRealArtifact_When_EventsFileExists(t *testing.T) {
	path := t.TempDir() + "/events.jsonl"
	w, err := event.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	evs := []event.Event{
		pollingEv("s1", "loto", "loto inbox"),
		pollingEv("s1", "loto", "loto inbox"),
		pollingEv("s1", "loto", "loto inbox"),
	}
	for i := range evs {
		if err := w.Write(&evs[i]); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := loadEvents(path)
	if err != nil {
		t.Fatalf("loadEvents: %v", err)
	}
	rep := mine.MinePolling(got)
	if len(rep.Rows) != 1 || rep.Rows[0].Command != "loto inbox" || rep.Rows[0].TotalRepeats != 3 {
		t.Errorf("mine over loaded events = %+v; want one `loto inbox` row with 3 repeats", rep.Rows)
	}
}
