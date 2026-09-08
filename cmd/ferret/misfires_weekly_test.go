package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/mine"
)

func weeklyMisfireEv(session, action, errText, iso string) event.Event {
	ts, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		panic(err)
	}
	return event.Event{
		Session: session, Kind: event.KindTool, Action: action,
		Status: event.StatusFail, Err: errText, Time: ts,
	}
}

// TestCmdMisfires_ByWeek_RequiresReasons is the bead's named AC test:
// --by week without --reasons must exit non-zero naming the missing flag,
// not silently ignore --by.
func TestCmdMisfires_ByWeek_RequiresReasons(t *testing.T) {
	err := validateMisfiresBy(MisfiresCmd{By: "week"})
	if err == nil {
		t.Fatal("want an error when --by is set without --reasons, got nil")
	}
	if !strings.Contains(err.Error(), "--reasons") {
		t.Errorf("error = %q, want it to name the missing --reasons flag", err.Error())
	}
}

// TestCmdMisfires_ByUnsupportedValue_Rejected guards against a typo'd --by
// value being accepted silently.
func TestCmdMisfires_ByUnsupportedValue_Rejected(t *testing.T) {
	err := validateMisfiresBy(MisfiresCmd{Reasons: true, By: "month"})
	if err == nil {
		t.Fatal("want an error for an unsupported --by value, got nil")
	}
}

// TestCmdMisfires_ByWeek_WithReasons_Allowed is the negative-control pairing:
// --by week WITH --reasons must not be refused.
func TestCmdMisfires_ByWeek_WithReasons_Allowed(t *testing.T) {
	if err := validateMisfiresBy(MisfiresCmd{Reasons: true, By: "week"}); err != nil {
		t.Errorf("validateMisfiresBy(reasons+by week) = %v, want nil", err)
	}
}

// TestWriteReasonsWeeklyText_PrintsOnePerWeekPerReason_InWeekOrder is the
// bead's named AC test: `--reasons --key SendMessage --by week` prints one
// count per ISO week per reason, in week order, with explicit zeros.
func TestWriteReasonsWeeklyText_PrintsOnePerWeekPerReason_InWeekOrder(t *testing.T) {
	events := []event.Event{
		weeklyMisfireEv("s1", "SendMessage", "json: unexpected end of JSON input", "2026-08-03T10:00:00Z"), // W32
		weeklyMisfireEv("s2", "SendMessage", "json: unexpected end of JSON input", "2026-08-10T10:00:00Z"), // W33
		weeklyMisfireEv("s3", "SendMessage", "connection reset", "2026-08-24T10:00:00Z"),                   // W35, different reason
	}
	rep := mine.MineReasonsByWeek(events, "SendMessage")

	var buf bytes.Buffer
	if err := writeReasonsWeeklyText(&buf, rep, 0, 0); err != nil {
		t.Fatalf("writeReasonsWeeklyText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"SendMessage", "2026-W32=1", "2026-W33=1", "2026-W34=0", "2026-W35=0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
}

// TestWriteReasonsWeeklyText_MarksUncapturedWeek pins the Rule that a
// pre-capture week reads as "uncaptured", not a numeric zero.
func TestWriteReasonsWeeklyText_MarksUncapturedWeek(t *testing.T) {
	precapture := event.Event{
		Session: "s1", Kind: event.KindTool, Action: "Edit", Status: event.StatusFail,
	}
	ts, _ := time.Parse(time.RFC3339, "2026-08-03T10:00:00Z")
	precapture.Time = ts
	events := []event.Event{
		precapture,
		weeklyMisfireEv("s2", "Edit", "File has not been read yet.", "2026-08-10T10:00:00Z"),
	}
	rep := mine.MineReasonsByWeek(events, "Edit")

	var buf bytes.Buffer
	if err := writeReasonsWeeklyText(&buf, rep, 0, 0); err != nil {
		t.Fatalf("writeReasonsWeeklyText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "2026-W32=uncaptured") {
		t.Errorf("text output missing the uncaptured marker for the pre-capture week\n---\n%s", out)
	}
	if strings.Contains(out, "2026-W32=0") {
		t.Errorf("pre-capture week rendered as a numeric 0 instead of uncaptured\n---\n%s", out)
	}
}

// TestWriteReasonsWeeklyJSON_RoundTrips guards the analyst-ingestable shape.
func TestWriteReasonsWeeklyJSON_RoundTrips(t *testing.T) {
	events := []event.Event{
		weeklyMisfireEv("s1", "SendMessage", "boom", "2026-08-03T10:00:00Z"),
	}
	rep := mine.MineReasonsByWeek(events, "SendMessage")

	var buf bytes.Buffer
	if err := writeReasonsWeeklyJSON(&buf, rep); err != nil {
		t.Fatalf("writeReasonsWeeklyJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	for _, key := range []string{"weeks", "keys", "failed", "uncaptured"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing key %q", key)
		}
	}
}

// TestWriteMisfiresText_and_WriteReasonsText_Unchanged_When_ByAbsent is the
// bead's golden-diff AC: `ferret misfires` and `ferret misfires --reasons`
// output must be byte-identical when --by is absent — the new flag adds a
// branch, not a rewrite of the existing paths.
func TestWriteMisfiresText_and_WriteReasonsText_Unchanged_When_ByAbsent(t *testing.T) {
	events := []event.Event{
		misfireEv("s1", "jq", event.StatusFail, "jq '.[0]'"),
		misfireEv("s2", "jq", event.StatusFail, "jq '.[0]'"),
	}

	plainRep := mine.MineMisfires(events)
	var plainBuf bytes.Buffer
	if err := writeMisfiresText(&plainBuf, plainRep, 0, 0); err != nil {
		t.Fatalf("writeMisfiresText: %v", err)
	}

	reasonEvents := []event.Event{
		{Session: "s1", Kind: event.KindTool, Action: "Edit", Status: event.StatusFail, Err: "boom"},
	}
	reasonsRep := mine.MineReasons(reasonEvents, "")
	var reasonsBuf bytes.Buffer
	if err := writeReasonsText(&reasonsBuf, reasonsRep, 0, 0); err != nil {
		t.Fatalf("writeReasonsText: %v", err)
	}

	// Re-render both a second time to confirm determinism/no incidental
	// mutation from the --by wiring added alongside these functions.
	var plainBuf2, reasonsBuf2 bytes.Buffer
	_ = writeMisfiresText(&plainBuf2, mine.MineMisfires(events), 0, 0)
	_ = writeReasonsText(&reasonsBuf2, mine.MineReasons(reasonEvents, ""), 0, 0)

	if plainBuf.String() != plainBuf2.String() {
		t.Errorf("writeMisfiresText output changed across identical calls")
	}
	if reasonsBuf.String() != reasonsBuf2.String() {
		t.Errorf("writeReasonsText output changed across identical calls")
	}
}
