package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/gates"
)

func gateEv(seq int, session, action, detail string) event.Event {
	return event.Event{Seq: seq, Session: session, Kind: event.KindTool, Action: action, Detail: detail}
}

// TestWriteGatesTextRedundantPair renders a corpus where two gates reject the
// same sessions: ω must surface as redundant, and the per-gate + loop lines show.
func TestWriteGatesTextRedundantPair(t *testing.T) {
	rep := gates.Extract([]event.Event{
		gateEv(0, "s1", "code-review", "needs revision"),
		gateEv(1, "s1", "code-review", "approved"),
		gateEv(2, "s1", "precommit", "failed"),
		gateEv(3, "s2", "code-review", "rejected"),
		gateEv(4, "s2", "precommit", "failed"),
	})
	var buf bytes.Buffer
	if err := writeGatesText(&buf, rep); err != nil {
		t.Fatalf("writeGatesText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"code-review",
		"precommit",
		"ω=1.00",
		"⚠ redundant",
		"confirmed friction loops",
		"resolved", // s1 code-review loop resolved at the APPROVED
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
}

// TestWriteGatesTextComplementary renders disjoint rejections: ω=0, labeled
// complementary (both gates earn their cost).
func TestWriteGatesTextComplementary(t *testing.T) {
	rep := gates.Extract([]event.Event{
		gateEv(0, "s1", "code-review", "rejected"),
		gateEv(1, "s2", "precommit", "failed"),
	})
	var buf bytes.Buffer
	if err := writeGatesText(&buf, rep); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "ω=0.00") || !strings.Contains(out, "complementary") {
		t.Errorf("expected complementary ω=0.00\n---\n%s", out)
	}
}

// TestWriteGatesTextNoGates renders the empty-corpus message.
func TestWriteGatesTextNoGates(t *testing.T) {
	var buf bytes.Buffer
	if err := writeGatesText(&buf, gates.Extract([]event.Event{gateEv(0, "s1", "Bash", "ls")})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no gate checks found") {
		t.Errorf("expected no-gates message\n---\n%s", buf.String())
	}
}

// TestWriteGatesJSONShape guards the analyst-ingestable JSON contract.
func TestWriteGatesJSONShape(t *testing.T) {
	rep := gates.Extract([]event.Event{
		gateEv(0, "s1", "code-review", "rejected"),
		gateEv(1, "s1", "precommit", "failed"),
	})
	var buf bytes.Buffer
	if err := writeGatesJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	for _, key := range []string{"gates", "overlaps", "loops", "checks"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing key %q", key)
		}
	}
	checks, ok := got["checks"].(float64)
	if !ok || checks != 2 {
		t.Errorf("checks=%v; want 2", got["checks"])
	}
}

// TestLoadEventsRoundTrip writes an events.jsonl and reads it back through the
// command's loader, confirming gate extraction over a real artifact file.
func TestLoadEventsRoundTrip(t *testing.T) {
	path := t.TempDir() + "/events.jsonl"
	w, err := event.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	evs := []event.Event{
		gateEv(0, "s1", "code-review", "rejected"),
		gateEv(1, "s1", "code-review", "approved"),
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
	if len(got) != 2 {
		t.Fatalf("loaded %d events; want 2", len(got))
	}
	rep := gates.Extract(got)
	if len(rep.Gates) != 1 || rep.Gates[0].Checks != 2 {
		t.Errorf("extract over loaded events = %+v; want 1 gate, 2 checks", rep.Gates)
	}
}
