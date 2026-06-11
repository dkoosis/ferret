package main

import (
	"path/filepath"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// TestIngestSWEGolden drives the SWE-agent adapter over the committed fixture
// and asserts the two artifacts: events.jsonl and the outcomes sidecar.
func TestIngestSWEGolden(t *testing.T) {
	data := t.TempDir()
	if err := ingestSWE(data, filepath.FromSlash("../../testdata/swe-agent/sample.jsonl"), false); err != nil {
		t.Fatalf("ingestSWE: %v", err)
	}

	var evs []*event.Event
	if err := event.Read(filepath.Join(data, "events.jsonl"), func(e *event.Event) error {
		evs = append(evs, e)
		return nil
	}); err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(evs) != 16 {
		t.Fatalf("events = %d, want 16", len(evs))
	}
	for _, e := range evs {
		if e.Project != "swe-agent" {
			t.Errorf("event project = %q, want swe-agent", e.Project)
		}
	}
	// The django run has three failing `python manage.py test` observations.
	fails := 0
	for _, e := range evs {
		if e.Status == event.StatusFail {
			fails++
		}
	}
	if fails != 4 { // 3 django + 1 flask ls
		t.Errorf("fail-marked events = %d, want 4", fails)
	}

	outs, err := event.ReadOutcomes(filepath.Join(data, "outcomes.jsonl"))
	if err != nil {
		t.Fatalf("read outcomes: %v", err)
	}
	if len(outs) != 3 {
		t.Fatalf("outcomes = %d, want 3", len(outs))
	}
	if o := outs["swe-agent/astropy__astropy-1001@"]; !o.Target {
		t.Errorf("astropy outcome target = false, want true")
	}
	if o := outs["swe-agent/django__django-2002@"]; o.Target {
		t.Errorf("django outcome target = true, want false")
	}
}
