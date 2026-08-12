package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/mine"
)

// substEv is the writer tests' fixture: a shell event carrying the raw
// (truncated) command text MineSubstitutions reads.
func substEv(session, action, detail string) event.Event {
	return event.Event{Session: session, Kind: event.KindShell, Action: action, Detail: detail}
}

// substFixture builds a small corpus: `rg foo` substitutable in two sessions,
// one `ls -la` excluded as an unsupported flag.
func substFixture() mine.SubstReport {
	events := []event.Event{
		substEv("s1", "rg", "rg foo"),
		substEv("s2", "rg", "rg bar"),
		substEv("s3", "ls", "ls -la"),
	}
	return mine.MineSubstitutions(events)
}

// TestWriteSubstText_ShowsRankAndColumns_When_CallsSubstitutable renders the
// table and checks every column a reader ranks on surfaces, plus the
// excluded-reason tally.
func TestWriteSubstText_ShowsRankAndColumns_When_CallsSubstitutable(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSubstText(&buf, substFixture(), 0, 0); err != nil {
		t.Fatalf("writeSubstText: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		"substitutable rows=1", "sessions=3",
		"calls=2", "sessions=2", "score=4", "sh:rg", "Grep", `"rg foo"`,
		"unsupported_flag=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text output missing %q\n---\n%s", want, got)
		}
	}
}

// TestWriteSubstText_CapsRows_When_LimitSet checks the sink's row limit is
// wired.
func TestWriteSubstText_CapsRows_When_LimitSet(t *testing.T) {
	events := []event.Event{
		substEv("s1", "rg", "rg foo"), substEv("s1", "rg", "rg bar"),
		substEv("s2", "cat", "cat f.go"),
	}
	rep := mine.MineSubstitutions(events)
	if len(rep.Rows) != 2 {
		t.Fatalf("fixture rows = %d, want 2", len(rep.Rows))
	}
	var buf bytes.Buffer
	if err := writeSubstText(&buf, rep, 1, 0); err != nil {
		t.Fatalf("writeSubstText: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "sh:rg") {
		t.Errorf("top row missing under limit=1\n---\n%s", got)
	}
	if strings.Contains(got, "sh:cat") {
		t.Errorf("second row rendered despite limit=1\n---\n%s", got)
	}
}

// TestWriteSubstJSON_RoundTrips_When_ReportHasRows guards the
// analyst-ingestable shape: rows, the corpus session denominator, the
// excluded tally, and the AX truncation contract keys.
func TestWriteSubstJSON_RoundTrips_When_ReportHasRows(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSubstJSON(&buf, substFixture(), 0); err != nil {
		t.Fatalf("writeSubstJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	for _, key := range []string{"rows", "sessions", "excluded", keyTotal} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing key %q", key)
		}
	}
	if total, ok := got[keyTotal].(float64); !ok || total != 1 {
		t.Errorf("total = %v, want 1", got[keyTotal])
	}
	rows, ok := got["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("rows = %v, want 1", got["rows"])
	}
	top, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("row 0 = %v, want an object", rows[0])
	}
	for _, key := range []string{"key", "tool", "calls", "sessions", "score"} {
		if _, ok := top[key]; !ok {
			t.Errorf("row JSON missing key %q; got %v", key, top)
		}
	}
	excluded, ok := got["excluded"].(map[string]any)
	if !ok || excluded["unsupported_flag"] != float64(1) {
		t.Errorf("excluded = %v, want unsupported_flag=1", got["excluded"])
	}
}

// TestWriteSubstJSON_CapsRows_When_LimitSet guards the pre-capping shape
// (writePollingJSON/writeMisfiresJSON precedent): the cap trims rows but
// total stays uncapped, and the truncated flag flips.
func TestWriteSubstJSON_CapsRows_When_LimitSet(t *testing.T) {
	events := []event.Event{
		substEv("s1", "rg", "rg foo"),
		substEv("s2", "cat", "cat f.go"),
	}
	rep := mine.MineSubstitutions(events)
	var buf bytes.Buffer
	if err := writeSubstJSON(&buf, rep, 1); err != nil {
		t.Fatalf("writeSubstJSON: %v", err)
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

// TestWriteSubstJSON_ReportsUntruncated_When_LimitUnset is the negative twin:
// with no limit, the truncation flag must not claim rows were dropped.
func TestWriteSubstJSON_ReportsUntruncated_When_LimitUnset(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSubstJSON(&buf, substFixture(), 0); err != nil {
		t.Fatalf("writeSubstJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if truncated, ok := got[keyTruncated].(bool); !ok || truncated {
		t.Errorf("truncated = %v, want false", got[keyTruncated])
	}
}

// TestCmdSubstitutable_LoadsRealArtifact_When_EventsFileExists is the
// round-trip check (mirrors TestCmdPolling_LoadsRealArtifact_When_EventsFileExists):
// write an events.jsonl and run the same load-then-mine path cmdSubstitutable
// uses (cmdSubstitutable itself writes to os.Stdout).
func TestCmdSubstitutable_LoadsRealArtifact_When_EventsFileExists(t *testing.T) {
	path := t.TempDir() + "/events.jsonl"
	w, err := event.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	evs := []event.Event{
		substEv("s1", "rg", "rg foo"),
		substEv("s2", "rg", "rg bar"),
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
	rep := mine.MineSubstitutions(got)
	if len(rep.Rows) != 1 || rep.Rows[0].Key != "sh:rg" || rep.Rows[0].Calls != 2 {
		t.Errorf("mine over loaded events = %+v; want one sh:rg row with 2 calls", rep.Rows)
	}
}
