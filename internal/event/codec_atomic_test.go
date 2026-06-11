package event

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// errBoomAtomic is a static sentinel (err113: no dynamic errors in tests)
// returned by the failing writer used to force a flush error mid-run.
var errBoomAtomic = errors.New("boom-atomic")

// failWriter fails every Write, forcing bufio.Writer.Flush to error so we can
// exercise the interrupted-run path without a real disk-full.
type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) { return 0, errBoomAtomic }

// TestWriterHappyPathRenames asserts the final artifact lands at path (not the
// .tmp) after a clean Close, and no .tmp is left dangling.
func TestWriterHappyPathRenames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Write(&Event{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("final artifact missing at path: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected no dangling .tmp, stat err=%v", err)
	}

	// The landed artifact must be readable back.
	var n int
	if err := Read(path, func(*Event) error { n++; return nil }); err != nil {
		t.Fatalf("Read landed artifact: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 event read back, got %d", n)
	}
}

// TestWriterFailLeavesPriorIntact simulates a write-then-fail over an existing
// artifact dir: the prior events.jsonl must remain intact and readable, and no
// .tmp may be left dangling.
func TestWriterFailLeavesPriorIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Lay down a prior, complete artifact (yesterday's run).
	prior, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter (prior): %v", err)
	}
	if err := prior.Write(&Event{Project: "prior"}); err != nil {
		t.Fatalf("Write (prior): %v", err)
	}
	if err := prior.Close(); err != nil {
		t.Fatalf("Close (prior): %v", err)
	}
	priorBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prior: %v", err)
	}

	// Today's run: open a real tmp via NewWriter so the cleanup path runs on
	// the actual tmp file, then swap in a failing buf so finish() errors.
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter (today): %v", err)
	}
	w.buf = bufio.NewWriter(failWriter{})
	w.enc = json.NewEncoder(w.buf)
	_ = w.Write(&Event{Project: "today"}) // buffer bytes so Flush attempts a write

	if err := w.Close(); err == nil {
		t.Fatal("expected Close to fail on interrupted run, got nil")
	} else if !errors.Is(err, errBoomAtomic) {
		t.Errorf("expected boom-atomic in close error, got %v", err)
	}

	// Prior artifact must survive untouched.
	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("prior artifact gone after failed run: %v", err)
	}
	if string(gotBytes) != string(priorBytes) {
		t.Errorf("prior artifact mutated: got %q want %q", gotBytes, priorBytes)
	}

	// And it must still be readable as the original event.
	var ids []string
	if err := Read(path, func(e *Event) error { ids = append(ids, e.Project); return nil }); err != nil {
		t.Fatalf("Read prior after failed run: %v", err)
	}
	if len(ids) != 1 || ids[0] != "prior" {
		t.Errorf("prior content corrupted: ids=%v", ids)
	}

	// No dangling .tmp.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected .tmp cleaned up after failed run, stat err=%v", err)
	}
}
