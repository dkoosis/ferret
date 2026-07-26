package feedback

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPendingRoundtrip: SavePending then LoadPending returns the same
// candidate — the handoff bank's basic contract.
func TestPendingRoundtrip(t *testing.T) {
	path := PendingPath(t.TempDir(), "s1")
	want := AskCandidate{
		TargetRef: "evt-1",
		SegmentID: "seg-1",
		TS:        "2026-07-25T15:00:00Z",
		Question:  "That get_nug \"x\" 3 turns back — did it help? [y/n]",
		Reason:    "lattice=helped but returned nugs graded irrelevant",
	}
	if err := SavePending(path, want); err != nil {
		t.Fatalf("SavePending: %v", err)
	}
	got, ok, err := LoadPending(path)
	if err != nil {
		t.Fatalf("LoadPending: %v", err)
	}
	if !ok {
		t.Fatal("LoadPending: want ok=true after a save")
	}
	if got != want {
		t.Errorf("LoadPending = %+v, want %+v", got, want)
	}
}

// TestLoadPendingMissingFile: no bank file yet (the common case — no ask has
// fired) reads as absent, not an error.
func TestLoadPendingMissingFile(t *testing.T) {
	path := PendingPath(t.TempDir(), "s1")
	got, ok, err := LoadPending(path)
	if err != nil {
		t.Fatalf("LoadPending on a missing file must not error, got %v", err)
	}
	if ok {
		t.Error("LoadPending on a missing file must report ok=false")
	}
	if got != (AskCandidate{}) {
		t.Errorf("LoadPending on a missing file must return the zero candidate, got %+v", got)
	}
}

// TestLoadPendingCorruptFileSelfHeals: a corrupt bank file must not hard-block
// the tap forever — it reads as absent (with a warning), the same self-heal
// contract as budget.go's loadState.
func TestLoadPendingCorruptFileSelfHeals(t *testing.T) {
	dir := t.TempDir()
	path := PendingPath(dir, "s1")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf fakeWriter
	orig := stderr
	stderr = &buf
	t.Cleanup(func() { stderr = orig })

	got, ok, err := LoadPending(path)
	if err != nil {
		t.Fatalf("corrupt file must self-heal, not error: %v", err)
	}
	if ok {
		t.Error("corrupt file must read as absent")
	}
	if got != (AskCandidate{}) {
		t.Errorf("corrupt file must yield the zero candidate, got %+v", got)
	}
	if buf.String() == "" {
		t.Error("corrupt file must warn to the stderr seam")
	}
}

// TestClearPendingIdempotent: clearing an already-absent bank file is not an
// error — check's one-shot consumption clears the file whether granted or
// denied, and a retry (or a race with no pending write) must not fail.
func TestClearPendingIdempotent(t *testing.T) {
	path := PendingPath(t.TempDir(), "s1")
	if err := ClearPending(path); err != nil {
		t.Errorf("ClearPending on an absent file must not error, got %v", err)
	}
	if err := SavePending(path, AskCandidate{TargetRef: "evt-1"}); err != nil {
		t.Fatal(err)
	}
	if err := ClearPending(path); err != nil {
		t.Fatalf("ClearPending: %v", err)
	}
	if _, ok, _ := LoadPending(path); ok {
		t.Error("ClearPending must remove the file")
	}
	if err := ClearPending(path); err != nil {
		t.Errorf("second ClearPending must still be a no-op, got %v", err)
	}
}

// TestArmedRoundtrip: SaveArmed then LoadArmed returns the same candidate —
// mirrors TestPendingRoundtrip for the second hop of the relay (ferret-j33).
func TestArmedRoundtrip(t *testing.T) {
	path := ArmedPath(t.TempDir(), "s1")
	want := AskCandidate{
		TargetRef: "evt-1",
		SegmentID: "seg-1",
		TS:        "2026-07-25T15:00:00Z",
		Question:  "That get_nug \"x\" 3 turns back — did it help? [y/n]",
		Reason:    "lattice=helped but returned nugs graded irrelevant",
	}
	if err := SaveArmed(path, want); err != nil {
		t.Fatalf("SaveArmed: %v", err)
	}
	got, ok, err := LoadArmed(path)
	if err != nil {
		t.Fatalf("LoadArmed: %v", err)
	}
	if !ok {
		t.Fatal("LoadArmed: want ok=true after a save")
	}
	if got != want {
		t.Errorf("LoadArmed = %+v, want %+v", got, want)
	}
}

// TestLoadArmedMissingFile: no armed file yet (the common case — no ask has
// been granted this turn) reads as absent, not an error.
func TestLoadArmedMissingFile(t *testing.T) {
	path := ArmedPath(t.TempDir(), "s1")
	got, ok, err := LoadArmed(path)
	if err != nil {
		t.Fatalf("LoadArmed on a missing file must not error, got %v", err)
	}
	if ok {
		t.Error("LoadArmed on a missing file must report ok=false")
	}
	if got != (AskCandidate{}) {
		t.Errorf("LoadArmed on a missing file must return the zero candidate, got %+v", got)
	}
}

// TestClearArmedIdempotent: clearing an already-absent armed file is not an
// error — answer's unconditional defer must tolerate a turn where nothing
// was ever armed.
func TestClearArmedIdempotent(t *testing.T) {
	path := ArmedPath(t.TempDir(), "s1")
	if err := ClearArmed(path); err != nil {
		t.Errorf("ClearArmed on an absent file must not error, got %v", err)
	}
	if err := SaveArmed(path, AskCandidate{TargetRef: "evt-1"}); err != nil {
		t.Fatal(err)
	}
	if err := ClearArmed(path); err != nil {
		t.Fatalf("ClearArmed: %v", err)
	}
	if _, ok, _ := LoadArmed(path); ok {
		t.Error("ClearArmed must remove the file")
	}
	if err := ClearArmed(path); err != nil {
		t.Errorf("second ClearArmed must still be a no-op, got %v", err)
	}
}

// TestPendingAndArmedPathsDoNotCollide: the two hops of the relay must live
// at distinct paths under the same session/data dir — a naming collision
// would let `check`'s clear of the pending bank race with `answer`'s read of
// the armed file, or vice versa.
func TestPendingAndArmedPathsDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	if PendingPath(dir, "s1") == ArmedPath(dir, "s1") {
		t.Error("pending and armed paths must not collide")
	}
}

// fakeWriter is a minimal io.Writer capture, avoiding a bytes.Buffer import
// just for a truthiness check.
type fakeWriter struct{ s string }

func (w *fakeWriter) Write(p []byte) (int, error) {
	w.s += string(p)
	return len(p), nil
}
func (w *fakeWriter) String() string { return w.s }
