package friction

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

func writeSigFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, SigFileName)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadSignaturesMissingFileIsEmpty(t *testing.T) {
	got, err := LoadSignatures(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("missing file must yield empty set, got %d", len(got))
	}
}

func TestLoadSignatures(t *testing.T) {
	p := writeSigFile(t,
		`{"fingerprint":"go_test go test <path>","label":"test loop"}`+"\n"+
			"\n"+ // blank line skipped
			`{"fingerprint":"git_checkout git checkout -b <path>","label":"branch-flip"}`+"\n")
	got, err := LoadSignatures(p)
	if err != nil {
		t.Fatalf("LoadSignatures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 signatures, got %d", len(got))
	}
	if got[0].Label != "test loop" || got[1].Fingerprint != "git_checkout git checkout -b <path>" {
		t.Errorf("parsed signatures wrong: %+v", got)
	}
}

func TestLoadSignaturesMalformedErrors(t *testing.T) {
	p := writeSigFile(t, `{"fingerprint":"ok"}`+"\n"+`{not json}`+"\n")
	if _, err := LoadSignatures(p); err == nil {
		t.Fatal("malformed signature line must error")
	}
}

// TestLoadThenDetect is the end-to-end contract: signatures loaded from a file
// seed a detector that flags a matching fresh event as a 2nd occurrence.
func TestLoadThenDetect(t *testing.T) {
	fp := Fingerprint("go_test go test ./internal/mine")
	p := writeSigFile(t, `{"fingerprint":"`+fp+`","label":"test loop"}`+"\n")
	sigs, err := LoadSignatures(p)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDetector(sigs)
	m, ok := d.Observe(failShell(7, "sess", "go_test", "go test ./internal/score"))
	if !ok {
		t.Fatal("loaded signature must flag a matching fresh event")
	}
	if m.Occurrence != 2 || m.Label != "test loop" {
		t.Errorf("match = %+v, want occurrence 2 label 'test loop'", m)
	}
}

// TestPersistLearnedAppendsDelta proves PersistLearned writes only the
// fingerprints not already known, and that they round-trip back through Load.
func TestPersistLearnedAppendsDelta(t *testing.T) {
	p := filepath.Join(t.TempDir(), SigFileName)
	known := []Signature{{Fingerprint: "already-known"}}
	learned := []Signature{
		{Fingerprint: "already-known"}, // skip: already known
		{Fingerprint: "new-a", Label: "a"},
		{Fingerprint: "new-a"}, // skip: dup within learned
		{Fingerprint: "new-b"},
		{Fingerprint: ""}, // skip: empty
	}
	n, err := PersistLearned(p, known, learned)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("appended %d, want 2 (new-a, new-b)", n)
	}
	got, err := LoadSignatures(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Fingerprint != "new-a" || got[0].Label != "a" || got[1].Fingerprint != "new-b" {
		t.Errorf("persisted set = %+v, want [new-a(a) new-b]", got)
	}
}

// TestCrossRunRecurrence is the Bug 1 contract: run 1 records signature X (first
// sighting, no flag) and persists it; run 2 is a FRESH detector loaded from the
// persisted file, and X's next occurrence is flagged as occurrence 2. Without
// persistence the learned corpus dies at process exit and cross-run recurrence
// is unreachable.
func TestCrossRunRecurrence(t *testing.T) {
	p := filepath.Join(t.TempDir(), SigFileName)

	// --- Run 1: empty file, one friction event. Records X, flags nothing. ---
	run1Sigs, err := LoadSignatures(p)
	if err != nil {
		t.Fatal(err)
	}
	d1 := NewDetector(run1Sigs)
	matches1 := d1.ScanInto([]event.Event{failShell(1, "run1", "go_test", "go test /Users/a/mine")})
	if len(matches1) != 0 {
		t.Fatalf("run 1 must flag nothing (first sighting), got %d", len(matches1))
	}
	if _, err := PersistLearned(p, run1Sigs, d1.Signatures()); err != nil {
		t.Fatal(err)
	}

	// --- Run 2: fresh process. Load persisted file, see X again → occurrence 2. ---
	run2Sigs, err := LoadSignatures(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(run2Sigs) == 0 {
		t.Fatal("run 2 loaded no signatures — run 1 did not persist")
	}
	d2 := NewDetector(run2Sigs)
	matches2 := d2.ScanInto([]event.Event{failShell(2, "run2", "go_test", "go test /Users/b/score")})
	if len(matches2) != 1 {
		t.Fatalf("run 2 must flag the recurrence, got %d matches", len(matches2))
	}
	if matches2[0].Occurrence != 2 {
		t.Errorf("cross-run occurrence = %d, want 2", matches2[0].Occurrence)
	}
}
