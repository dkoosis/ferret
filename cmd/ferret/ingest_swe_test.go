package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// errInjected is a static sentinel (err113: no inline errors.New in tests).
var errInjected = errors.New("injected write failure")

// failAfterWriter persists the first n records, then fails every Write. It
// stands in for a disk filling mid-ingest (ENOSPC/quota).
type failAfterWriter struct {
	n      int
	writes int
}

func (w *failAfterWriter) Write(*event.Event) error {
	w.writes++
	if w.writes > w.n {
		return errInjected
	}
	return nil
}
func (w *failAfterWriter) Close() error { return nil }

// nopOutcomeWriter swallows outcome writes — the event path is what fails here.
type nopOutcomeWriter struct{}

func (nopOutcomeWriter) Write(*event.Outcome) error { return nil }
func (nopOutcomeWriter) Close() error               { return nil }

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

// TestIngestSWEWriteErrorAbortsNoManifest injects a writer that fails after K
// records. Acceptance: ingest returns nonzero and writes no manifest, so a
// later mine can never run on silently-truncated data.
func TestIngestSWEWriteErrorAbortsNoManifest(t *testing.T) {
	origEW, origOW := newEventWriter, newOutcomeWriter
	t.Cleanup(func() { newEventWriter, newOutcomeWriter = origEW, origOW })

	newEventWriter = func(string) (eventSink, error) { return &failAfterWriter{n: 5}, nil }
	newOutcomeWriter = func(string) (outcomeSink, error) { return nopOutcomeWriter{}, nil }

	data := t.TempDir()
	err := ingestSWE(data, filepath.FromSlash("../../testdata/swe-agent/sample.jsonl"), false)
	if err == nil {
		t.Fatal("ingestSWE returned nil, want a write error")
	}
	if !errors.Is(err, errWriteAbort) {
		t.Errorf("error = %v, want wrapped errWriteAbort", err)
	}

	if _, serr := os.Stat(filepath.Join(data, "manifest.json")); !os.IsNotExist(serr) {
		t.Errorf("manifest.json exists after partial ingest (stat err=%v); want absent", serr)
	}
}
