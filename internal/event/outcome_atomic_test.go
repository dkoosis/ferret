package event

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestOutcomeWriterHappyPathRenames asserts a clean Close lands the artifact at
// path and leaves no dangling .tmp.
func TestOutcomeWriterHappyPathRenames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outcomes.jsonl")

	w, err := NewOutcomeWriter(path)
	if err != nil {
		t.Fatalf("NewOutcomeWriter: %v", err)
	}
	if err := w.Write(&Outcome{Stream: "p/s@a", Target: true}); err != nil {
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

	out, err := ReadOutcomes(path)
	if err != nil {
		t.Fatalf("ReadOutcomes: %v", err)
	}
	if o, ok := out["p/s@a"]; !ok || !o.Target {
		t.Errorf("landed outcome wrong: %+v", out)
	}
}

// TestOutcomeWriterFailLeavesPriorIntact simulates a write-then-fail over an
// existing outcomes artifact: the prior file stays intact and readable, with no
// dangling .tmp.
func TestOutcomeWriterFailLeavesPriorIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outcomes.jsonl")

	prior, err := NewOutcomeWriter(path)
	if err != nil {
		t.Fatalf("NewOutcomeWriter (prior): %v", err)
	}
	if err := prior.Write(&Outcome{Stream: "prior", Target: true}); err != nil {
		t.Fatalf("Write (prior): %v", err)
	}
	if err := prior.Close(); err != nil {
		t.Fatalf("Close (prior): %v", err)
	}
	priorBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prior: %v", err)
	}

	w, err := NewOutcomeWriter(path)
	if err != nil {
		t.Fatalf("NewOutcomeWriter (today): %v", err)
	}
	w.buf = bufio.NewWriter(failWriter{})
	w.enc = json.NewEncoder(w.buf)
	_ = w.Write(&Outcome{Stream: "today"})

	if err := w.Close(); err == nil {
		t.Fatal("expected Close to fail on interrupted run, got nil")
	} else if !errors.Is(err, errBoomAtomic) {
		t.Errorf("expected boom-atomic in close error, got %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("prior artifact gone after failed run: %v", err)
	}
	if string(gotBytes) != string(priorBytes) {
		t.Errorf("prior artifact mutated: got %q want %q", gotBytes, priorBytes)
	}

	out, err := ReadOutcomes(path)
	if err != nil {
		t.Fatalf("ReadOutcomes after failed run: %v", err)
	}
	if _, ok := out["prior"]; !ok || len(out) != 1 {
		t.Errorf("prior outcome corrupted: %+v", out)
	}

	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected .tmp cleaned up after failed run, stat err=%v", err)
	}
}
