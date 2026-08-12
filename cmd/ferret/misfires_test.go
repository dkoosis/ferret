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
	if err := writeMisfiresJSON(&buf, rep, 0); err != nil {
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

// TestWriteMisfiresJSON_CapsRowsAndRepairs_When_LimitSet guards the
// pre-capping writeBurnJSON mirrors — the out.JSON contract itself ignores
// row limits, so the cap must happen in writeMisfiresJSON.
func TestWriteMisfiresJSON_CapsRowsAndRepairs_When_LimitSet(t *testing.T) {
	events := []event.Event{
		misfireEv("s1", "jq", event.StatusFail, "jq '.[0]'"),
		misfireEv("s1", "jq", event.StatusOK, "jq '.result[0]'"),
		misfireEv("s2", "grep", event.StatusFail, "grep -r x"),
		misfireEv("s2", "grep", event.StatusOK, "grep -rn x"),
	}
	events[1].Retry = true
	events[3].Retry = true

	rep := mine.MineMisfires(events)
	if len(rep.Rows) < 2 || len(rep.Repairs) < 2 {
		t.Fatalf("fixture too small: rows=%d repairs=%d", len(rep.Rows), len(rep.Repairs))
	}

	var buf bytes.Buffer
	if err := writeMisfiresJSON(&buf, rep, 1); err != nil {
		t.Fatalf("writeMisfiresJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if rows, ok := got["rows"].([]any); !ok || len(rows) != 1 {
		t.Errorf("rows len=%v; want 1 (capped)", got["rows"])
	}
	if repairs, ok := got["repairs"].([]any); !ok || len(repairs) != 1 {
		t.Errorf("repairs len=%v; want 1 (capped)", got["repairs"])
	}
	if total, ok := got["total"].(float64); !ok || int(total) != len(rep.Rows) {
		t.Errorf("total=%v; want uncapped %d", got["total"], len(rep.Rows))
	}
}

// swallowMisfireEv is a shell event carrying the ingest-set swallow flag, the
// shape internal/event/build.go writes for a `cmd 2>/dev/null || fallback`
// left arm. Status is ok on purpose — that is the whole point of the tell.
func swallowMisfireEv(session, action, detail string) event.Event {
	e := misfireEv(session, action, event.StatusOK, detail)
	e.Swallow = true
	return e
}

// TestWriteMisfiresText_RendersSwallowedSection_When_CommandsHideTheirErrors
// checks the section header, the columns, and the exemplar command that makes
// a row actionable.
func TestWriteMisfiresText_RendersSwallowedSection_When_CommandsHideTheirErrors(t *testing.T) {
	rep := mine.MineMisfires([]event.Event{
		swallowMisfireEv("s1", "bd_show", "bd show x 2>/dev/null"),
		swallowMisfireEv("s2", "bd_show", "bd show y 2>/dev/null"),
		misfireEv("s3", "bd_show", event.StatusOK, "bd show z --short"),
	})
	var buf bytes.Buffer
	if err := writeMisfiresText(&buf, rep, 0, 0); err != nil {
		t.Fatalf("writeMisfiresText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"swallowed errors", "bd_show", "swallowed=2", "sessions=2", "calls=3", "rate=0.67",
		`"bd show x 2>/dev/null"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
}

// TestWriteMisfiresText_OmitsSwallowedSection_When_NothingIsHidden keeps the
// dense-output contract: no header for an empty section.
func TestWriteMisfiresText_OmitsSwallowedSection_When_NothingIsHidden(t *testing.T) {
	rep := mine.MineMisfires([]event.Event{
		misfireEv("s1", "jq", event.StatusFail, "jq '.[0]'"),
	})
	var buf bytes.Buffer
	if err := writeMisfiresText(&buf, rep, 0, 0); err != nil {
		t.Fatalf("writeMisfiresText: %v", err)
	}
	if strings.Contains(buf.String(), "swallowed errors (") {
		t.Errorf("empty swallowed section still rendered a header\n---\n%s", buf.String())
	}
}

// TestWriteMisfiresText_CapsSwallowedRows_When_LimitSet guards the shared row
// budget: the sink's limit covers every section, so an over-budget swallow row
// is counted into the truncation tail rather than printed.
func TestWriteMisfiresText_CapsSwallowedRows_When_LimitSet(t *testing.T) {
	rep := mine.MineMisfires([]event.Event{
		swallowMisfireEv("s1", "bd_show", "bd show x 2>/dev/null"),
		swallowMisfireEv("s2", "rg", "rg pat 2>/dev/null"),
	})
	if len(rep.Swallowed) != 2 {
		t.Fatalf("fixture too small: swallowed=%d", len(rep.Swallowed))
	}
	var buf bytes.Buffer
	if err := writeMisfiresText(&buf, rep, 1, 0); err != nil {
		t.Fatalf("writeMisfiresText: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "rg ") {
		t.Errorf("second swallow row rendered past the row limit\n---\n%s", out)
	}
	if !strings.Contains(out, "more (raise -limit") {
		t.Errorf("truncation was silent — no tail line\n---\n%s", out)
	}
}

// TestWriteMisfiresJSON_EmitsSwallowedAndCapsIt_When_LimitSet guards the
// analyst-ingestable shape: the section, its uncapped total, and the same
// pre-capping writeBurnJSON mirrors.
func TestWriteMisfiresJSON_EmitsSwallowedAndCapsIt_When_LimitSet(t *testing.T) {
	rep := mine.MineMisfires([]event.Event{
		swallowMisfireEv("s1", "bd_show", "bd show x 2>/dev/null"),
		swallowMisfireEv("s2", "rg", "rg pat 2>/dev/null"),
	})
	if len(rep.Swallowed) != 2 {
		t.Fatalf("fixture too small: swallowed=%d", len(rep.Swallowed))
	}
	var buf bytes.Buffer
	if err := writeMisfiresJSON(&buf, rep, 1); err != nil {
		t.Fatalf("writeMisfiresJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	sw, ok := got["swallowed"].([]any)
	if !ok || len(sw) != 1 {
		t.Fatalf("swallowed=%v; want 1 row (capped)", got["swallowed"])
	}
	if total, ok := got["swallowedTotal"].(float64); !ok || int(total) != 2 {
		t.Errorf("swallowedTotal=%v; want uncapped 2", got["swallowedTotal"])
	}
	row, ok := sw[0].(map[string]any)
	if !ok {
		t.Fatalf("swallowed row is not an object: %#v", sw[0])
	}
	for _, key := range []string{"key", "swallows", "swallowSess", "calls", "swallowRate", "score", "exemplar"} {
		if _, ok := row[key]; !ok {
			t.Errorf("swallowed row missing key %q; got %#v", key, row)
		}
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
	if len(rep.Rows) != 1 || rep.Rows[0].Key != "sh:jq" || rep.Rows[0].Fails != 2 {
		t.Errorf("mine over loaded events = %+v; want one sh:jq row with 2 fails", rep.Rows)
	}
}
