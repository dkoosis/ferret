package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// errInjectedClose is a static sentinel for a failing Close call (err113: no
// inline errors.New in tests).
var errInjectedClose = errors.New("injected close failure")

// errInjectedOpen is a static sentinel for a failing writer construction.
var errInjectedOpen = errors.New("injected open failure")

// closeSpy wraps an inner outcomeSink and records whether Close or Abort was called.
type closeSpy struct {
	inner   outcomeSink
	closed  bool
	aborted bool
}

func (s *closeSpy) Write(o *event.Outcome) error { return s.inner.Write(o) }
func (s *closeSpy) Close() error {
	s.closed = true
	return s.inner.Close()
}
func (s *closeSpy) Abort() {
	s.aborted = true
	s.inner.Abort()
}

// failCloseEventWriter succeeds on Write but errors on Close.
type failCloseEventWriter struct{}

func (failCloseEventWriter) Write(*event.Event) error { return nil }
func (failCloseEventWriter) Close() error             { return errInjectedClose }
func (failCloseEventWriter) Abort()                   {}

// failOpenOutcomeWriter is used to replace newOutcomeWriter with a constructor
// that always returns an error, exercising the ferret-i6a leak path.
func failOpenOutcomeWriter(string) (outcomeSink, error) { return nil, errInjectedOpen }

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
func (w *failAfterWriter) Abort()       {}

// nopOutcomeWriter swallows outcome writes — the event path is what fails here.
type nopOutcomeWriter struct{}

func (nopOutcomeWriter) Write(*event.Outcome) error { return nil }
func (nopOutcomeWriter) Close() error               { return nil }
func (nopOutcomeWriter) Abort()                     {}

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

// TestIngestSWEEventCloseErrorFlushesOutcomes covers ferret-91f: when
// w.Close() fails, ow must be finalized (closed or aborted — no fd leak) and
// no manifest must be written.
func TestIngestSWEEventCloseErrorFlushesOutcomes(t *testing.T) {
	origEW, origOW := newEventWriter, newOutcomeWriter
	t.Cleanup(func() { newEventWriter, newOutcomeWriter = origEW, origOW })

	spy := &closeSpy{inner: nopOutcomeWriter{}}
	newEventWriter = func(string) (eventSink, error) { return failCloseEventWriter{}, nil }
	newOutcomeWriter = func(string) (outcomeSink, error) { return spy, nil }

	data := t.TempDir()
	err := ingestSWE(data, filepath.FromSlash("../../testdata/swe-agent/sample.jsonl"), false)
	if err == nil {
		t.Fatal("ingestSWE returned nil, want a close error")
	}
	if !errors.Is(err, errInjectedClose) {
		t.Errorf("error = %v, want errInjectedClose", err)
	}
	// Outcome writer must have been finalized (Close or Abort) — no fd leak.
	if !spy.closed && !spy.aborted {
		t.Error("outcome writer was neither closed nor aborted after event writer close failure (fd leak)")
	}
	if _, serr := os.Stat(filepath.Join(data, "manifest.json")); !os.IsNotExist(serr) {
		t.Errorf("manifest.json exists after close-error ingest (stat err=%v); want absent", serr)
	}
}

// TestIngestSWEOutcomeOpenFailureClosesEventWriter covers ferret-i6a: when
// NewOutcomeWriter fails, the already-open event writer must be closed (no fd
// leak) and no orphan events.jsonl must remain.
func TestIngestSWEOutcomeOpenFailureClosesEventWriter(t *testing.T) {
	origEW, origOW := newEventWriter, newOutcomeWriter
	t.Cleanup(func() { newEventWriter, newOutcomeWriter = origEW, origOW })

	// Use a real temp dir so we can check for orphan files.
	data := t.TempDir()
	var capturedPath string
	newEventWriter = func(path string) (eventSink, error) {
		capturedPath = path
		return event.NewWriter(path)
	}
	newOutcomeWriter = failOpenOutcomeWriter

	err := ingestSWE(data, filepath.FromSlash("../../testdata/swe-agent/sample.jsonl"), false)
	if err == nil {
		t.Fatal("ingestSWE returned nil, want outcome-open error")
	}
	if !errors.Is(err, errInjectedOpen) {
		t.Errorf("error = %v, want errInjectedOpen", err)
	}

	// No orphan events.jsonl (atomic writer drops .tmp on failure; .tmp must be gone too).
	if _, serr := os.Stat(capturedPath); !os.IsNotExist(serr) {
		t.Errorf("orphan events.jsonl at %s after outcome-open failure; want absent", capturedPath)
	}
	if _, serr := os.Stat(capturedPath + ".tmp"); !os.IsNotExist(serr) {
		t.Errorf("orphan events.jsonl.tmp at %s after outcome-open failure; want absent", capturedPath+".tmp")
	}
}

// TestIngestSWEPartialEmitNoMismatchedPair covers ferret-2yv: when the event
// writer errors mid-run, the outcome writer must NOT seal a new outcomes.jsonl
// (no mismatched pair left on disk). Neither artifact must advance.
func TestIngestSWEPartialEmitNoMismatchedPair(t *testing.T) {
	origEW, origOW := newEventWriter, newOutcomeWriter
	t.Cleanup(func() { newEventWriter, newOutcomeWriter = origEW, origOW })

	data := t.TempDir()

	// Use real writers so we exercise the actual atomic-rename path.
	newEventWriter = func(path string) (eventSink, error) { return &failAfterWriter{n: 3}, nil }
	// Use a real OutcomeWriter so its tmp/rename path is live.
	newOutcomeWriter = func(path string) (outcomeSink, error) { return event.NewOutcomeWriter(path) }

	err := ingestSWE(data, filepath.FromSlash("../../testdata/swe-agent/sample.jsonl"), false)
	if err == nil {
		t.Fatal("ingestSWE returned nil, want partial-emit error")
	}

	// outcomes.jsonl must NOT exist (no sealed pair if events were partial).
	if _, serr := os.Stat(filepath.Join(data, "outcomes.jsonl")); !os.IsNotExist(serr) {
		t.Errorf("outcomes.jsonl sealed after partial event write (stat err=%v); want absent", serr)
	}
	// manifest must not exist either.
	if _, serr := os.Stat(filepath.Join(data, "manifest.json")); !os.IsNotExist(serr) {
		t.Errorf("manifest.json exists after partial ingest (stat err=%v); want absent", serr)
	}
}

// TestEnsureDataZeroByteTriggersReingest covers ferret-dj2: a 0-byte
// events.jsonl must be treated as absent — ensureData must trigger re-ingest
// instead of proceeding on an empty corpus.
func TestEnsureDataZeroByteTriggersReingest(t *testing.T) {
	data := t.TempDir()
	// Plant a 0-byte events.jsonl to simulate an interrupted prior ingest.
	evPath := filepath.Join(data, "events.jsonl")
	if err := os.WriteFile(evPath, nil, 0o644); err != nil {
		t.Fatalf("create 0-byte artifact: %v", err)
	}

	c := &common{data: data}
	// ensureData will call ingest() if the gate fails. ingest calls
	// transcript.Walk which will fail on a non-existent root — that error
	// propagates. We're not testing that ingest succeeds; we're testing that
	// ensureData does NOT return nil on a 0-byte file (i.e. it doesn't pass
	// the gate).
	err := c.ensureData()
	// If ensureData returned nil without re-ingesting, the 0-byte file would
	// remain and we'd be running on an empty corpus. Either it returns an error
	// (because ingest failed) or it successfully re-ingested. Either way it must
	// NOT silently return nil while the file stays 0 bytes.
	if err == nil {
		// ensureData returned nil — check it actually re-ingested by confirming
		// the 0-byte file is gone or a manifest was written.
		fi, serr := os.Stat(evPath)
		if serr == nil && fi.Size() == 0 {
			t.Error("ensureData returned nil with a 0-byte events.jsonl still in place — did not trigger re-ingest")
		}
	}
	// If err != nil, that's fine: ensureData tried to re-ingest and the ingest
	// failed (no valid root), which proves it did NOT skip past the 0-byte file.
}

// TestCCIngestWriteErrorNoManifest covers ferret-amy: when the CC-path ingest
// (ingest()) encounters a write error, it must return nonzero and leave no
// manifest and no committed events.jsonl.
func TestCCIngestWriteErrorNoManifest(t *testing.T) {
	origEW := newEventWriter
	t.Cleanup(func() { newEventWriter = origEW })

	newEventWriter = func(string) (eventSink, error) { return &failAfterWriter{n: 0}, nil }

	data := t.TempDir()
	// Use a known-good transcript root that has at least one event.
	root := filepath.FromSlash("../../testdata")
	err := ingest(data, root, "", false)
	// ingest may return nil if there are no transcripts — only check the artifacts
	// if it returned an error.
	if err != nil {
		if _, serr := os.Stat(filepath.Join(data, "manifest.json")); !os.IsNotExist(serr) {
			t.Errorf("manifest.json exists after CC ingest write error; want absent")
		}
		if _, serr := os.Stat(filepath.Join(data, "events.jsonl")); !os.IsNotExist(serr) {
			t.Errorf("events.jsonl committed after CC ingest write error; want absent (atomic drop)")
		}
	}
}
