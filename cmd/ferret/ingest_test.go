package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// errSyntheticDiskFull is the stub writer's injected mid-write failure.
var errSyntheticDiskFull = errors.New("disk full (synthetic)")

// writeIngestFixture drops a transcript with a few tool calls so ingest produces
// a deterministic, non-zero event count.
func writeIngestFixture(t *testing.T, root string) {
	t.Helper()
	lines := make([]string, 0, 11)
	lines = append(lines, `{"type":"user","sessionId":"s1","message":{"role":"user","content":"do work"}}`)
	for i := range 5 {
		lines = append(lines,
			fmt.Sprintf(`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","content":[`+
				`{"type":"tool_use","id":"t%d","name":"Bash","input":{"command":"echo %d"}}]}}`, i, i),
			fmt.Sprintf(`{"type":"user","sessionId":"s1","message":{"role":"user","content":[`+
				`{"type":"tool_result","tool_use_id":"t%d","is_error":false,"content":"ok"}]}}`, i),
		)
	}
	writeSpineFixture(t, root, "-Users-dev-proj", "s1.jsonl", lines)
}

func countEvents(t *testing.T, path string) int {
	t.Helper()
	n := 0
	if err := event.Read(path, func(*event.Event) error { n++; return nil }); err != nil {
		t.Fatalf("Read events: %v", err)
	}
	return n
}

// TestConcurrentIngestNoInterleave guards ferret-0vz: two ingests racing on one
// data dir used to both os.Create the same fixed events.jsonl.tmp and interleave
// NDJSON into a shredded mix, the last rename publishing it. With a per-process
// unique tmp + an advisory lock, the published artifact must decode to exactly
// one run's events — never the doubled/torn union of both.
func TestConcurrentIngestNoInterleave(t *testing.T) {
	root := t.TempDir()
	writeIngestFixture(t, root)

	// Single-run baseline count.
	baseDir := t.TempDir()
	if err := ingest(baseDir, root, "", "", false); err != nil {
		t.Fatalf("baseline ingest: %v", err)
	}
	want := countEvents(t, filepath.Join(baseDir, "events.jsonl"))
	if want == 0 {
		t.Fatal("fixture produced no events; test cannot discriminate")
	}

	// Two concurrent ingests against ONE shared data dir.
	dataDir := t.TempDir()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = ingest(dataDir, root, "", "", false)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent ingest %d: %v", i, err)
		}
	}

	got := countEvents(t, filepath.Join(dataDir, "events.jsonl"))
	if got != want {
		t.Errorf("concurrent ingest produced %d events; want exactly one run's %d (interleaving/doubling)", got, want)
	}
}

// failOnNthWriter is an eventSink that fails the Nth Write, recording whether the
// run was rolled back via Abort (correct) or sealed via Close (the bug).
type failOnNthWriter struct {
	n           int
	count       int
	abortCalled bool
	closeCalled bool
}

func (w *failOnNthWriter) Write(*event.Event) error {
	w.count++
	if w.count >= w.n {
		return errSyntheticDiskFull
	}
	return nil
}
func (w *failOnNthWriter) Close() error { w.closeCalled = true; return nil }
func (w *failOnNthWriter) Abort()       { w.abortCalled = true }

// TestIngestAbortsOnWriteError guards ferret-0m7: a mid-write failure must ABORT
// (close-without-rename) so no partial events.jsonl is published and no manifest
// is sealed — not Close, which would flush+fsync+rename the truncated tmp onto
// events.jsonl with only the suppressed manifest as a safety net.
func TestIngestAbortsOnWriteError(t *testing.T) {
	root := t.TempDir()
	writeIngestFixture(t, root)
	dataDir := t.TempDir()

	stub := &failOnNthWriter{n: 2}
	orig := newEventWriter
	newEventWriter = func(string) (eventSink, error) { return stub, nil }
	t.Cleanup(func() { newEventWriter = orig })

	err := ingest(dataDir, root, "", "", false)
	if err == nil {
		t.Fatal("ingest with a failing writer must return an error")
	}
	if !stub.abortCalled {
		t.Error("mid-write failure must call Abort (roll back), not seal the artifact")
	}
	if stub.closeCalled {
		t.Error("mid-write failure must NOT call Close (which would publish the truncated tmp)")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "manifest.json")); !os.IsNotExist(err) {
		t.Errorf("no manifest must be sealed over a partial run, stat err=%v", err)
	}
}
