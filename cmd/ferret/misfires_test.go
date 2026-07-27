package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/mine"
)

func misfireEv(session, action, status, detail string) event.Event {
	return event.Event{Session: session, Kind: event.KindShell, Action: action, Status: status, Detail: detail}
}

// TestWriteMisfiresText_ShowsRankAndColumns_When_KeyHasFailures renders one
// misfire row and checks the bead's named columns (key, failure count,
// sessions, fail rate) all surface.
func TestWriteMisfiresText_ShowsRankAndColumns_When_KeyHasFailures(t *testing.T) {
	rep := mine.MineMisfires([]event.Event{
		misfireEv("s1", "jq", event.StatusFail, "jq '.[0]'"),
		misfireEv("s2", "jq", event.StatusFail, "jq '.[0]'"),
		misfireEv("s3", "jq", event.StatusOK, "jq '.[0]'"),
	})
	var buf bytes.Buffer
	if err := writeMisfiresText(&buf, rep, 0, 0); err != nil {
		t.Fatalf("writeMisfiresText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"jq", "fails=2", "sessions=2", "calls=3", "fail-rate=0.67"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
}

// TestWriteMisfiresText_RendersRepairPair_When_RetryResolvedAFailure checks
// the failed→fixed rendering, including the fallback line for a key-level
// pair with no captured raw text.
func TestWriteMisfiresText_RendersRepairPair_When_RetryResolvedAFailure(t *testing.T) {
	failedEv := misfireEv("s1", "jq", event.StatusFail, "jq '.[0]'")
	fixedEv := misfireEv("s1", "jq", event.StatusOK, "jq '.result[0]'")
	fixedEv.Retry = true

	rep := mine.MineMisfires([]event.Event{failedEv, fixedEv})
	var buf bytes.Buffer
	if err := writeMisfiresText(&buf, rep, 0, 0); err != nil {
		t.Fatalf("writeMisfiresText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"repair pairs", "jq", `"jq '.[0]'"`, `"jq '.result[0]'"`} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
}

// TestWriteMisfiresJSON_RoundTrips_When_ReportHasRowsAndRepairs guards the
// analyst-ingestable JSON shape: rows, repairs, and their totals.
func TestWriteMisfiresJSON_RoundTrips_When_ReportHasRowsAndRepairs(t *testing.T) {
	failedEv := misfireEv("s1", "jq", event.StatusFail, "jq '.[0]'")
	fixedEv := misfireEv("s1", "jq", event.StatusOK, "jq '.result[0]'")
	fixedEv.Retry = true

	rep := mine.MineMisfires([]event.Event{failedEv, fixedEv})
	var buf bytes.Buffer
	if err := writeMisfiresJSON(&buf, rep); err != nil {
		t.Fatalf("writeMisfiresJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	for _, key := range []string{"rows", "repairs", "total", "repairsTotal"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing key %q", key)
		}
	}
	if total, ok := got["total"].(float64); !ok || total != 1 {
		t.Errorf("total=%v; want 1", got["total"])
	}
	if rt, ok := got["repairsTotal"].(float64); !ok || rt != 1 {
		t.Errorf("repairsTotal=%v; want 1", got["repairsTotal"])
	}
}

// TestCmdMisfires_LoadsRealArtifact_When_EventsFileExists is the round-trip
// check (mirrors TestLoadEventsRoundTrip in gates_test.go): write an
// events.jsonl, run the full cmdMisfires load-then-mine path against it via
// loadEvents + mine.MineMisfires directly (cmdMisfires itself writes to
// os.Stdout, so the plumbing below exercises the same load path it uses).
func TestCmdMisfires_LoadsRealArtifact_When_EventsFileExists(t *testing.T) {
	path := t.TempDir() + "/events.jsonl"
	w, err := event.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	evs := []event.Event{
		misfireEv("s1", "jq", event.StatusFail, "jq '.[0]'"),
		misfireEv("s2", "jq", event.StatusFail, "jq '.[0]'"),
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
	rep := mine.MineMisfires(got)
	if len(rep.Rows) != 1 || rep.Rows[0].Key != "jq" || rep.Rows[0].Fails != 2 {
		t.Errorf("mine over loaded events = %+v; want one jq row with 2 fails", rep.Rows)
	}
}
