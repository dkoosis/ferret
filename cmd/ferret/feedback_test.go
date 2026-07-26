package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveRetrievalEventsPathPicksLexicallyLast: the live retrieval-event
// feed rotates monthly (retrieval-live-YYYY-MM.jsonl); the filename embeds the
// month, so lexical order is chronological order and picking the last match is
// "the current file" without parsing any date.
func TestResolveRetrievalEventsPathPicksLexicallyLast(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"retrieval-live-2026-06.jsonl", "retrieval-live-2026-07.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := resolveRetrievalEventsPath(dir)
	if err != nil {
		t.Fatalf("resolveRetrievalEventsPath: %v", err)
	}
	want := filepath.Join(dir, "retrieval-live-2026-07.jsonl")
	if got != want {
		t.Errorf("resolveRetrievalEventsPath = %q, want %q", got, want)
	}
}

// TestResolveRetrievalEventsPathNoMatch: an events dir with no retrieval-live
// file (a session before trixi's producer has ever fired) is a clean error,
// not a nil/empty path a caller could silently treat as "no events".
func TestResolveRetrievalEventsPathNoMatch(t *testing.T) {
	if _, err := resolveRetrievalEventsPath(t.TempDir()); err == nil {
		t.Error("want an error when no retrieval-live-*.jsonl file exists")
	}
}

// TestResolveRetrievalEventsPathIgnoresUnrelatedFiles: only the
// retrieval-live-*.jsonl pattern counts — an unrelated file in the same dir
// must not be picked or block resolution.
func TestResolveRetrievalEventsPathIgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "retrieval-live-2026-07.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveRetrievalEventsPath(dir)
	if err != nil {
		t.Fatalf("resolveRetrievalEventsPath: %v", err)
	}
	want := filepath.Join(dir, "retrieval-live-2026-07.jsonl")
	if got != want {
		t.Errorf("resolveRetrievalEventsPath = %q, want %q", got, want)
	}
}

// TestFeedbackPrepFreshEnvIsSilentNoOp: on a project whose events dir holds no
// retrieval-live-*.jsonl yet (a fresh env before trixi's producer has fired),
// `feedback prep` must print {"pending":false} and exit 0 — the ordinary
// empty-feed state, NOT an error the async Stop hook turns into an exit-2 nag
// every turn. Regression test for ferret-ql1.
func TestFeedbackPrepFreshEnvIsSilentNoOp(t *testing.T) {
	emptyEvents := t.TempDir() // no retrieval-live-*.jsonl inside
	CLI.Feedback.Prep.Session = "s1"
	CLI.Feedback.Prep.Data = t.TempDir()
	CLI.Feedback.Prep.Events = emptyEvents
	t.Cleanup(func() { CLI.Feedback.Prep.Session, CLI.Feedback.Prep.Data, CLI.Feedback.Prep.Events = "", "", "" })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	runErr := cmdFeedbackPrep()
	_ = w.Close()
	os.Stdout = saved
	out, _ := io.ReadAll(r)

	if runErr != nil {
		t.Fatalf("fresh-env prep must not error, got %v", runErr)
	}
	var res prepResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("prep output %q not valid prepResult JSON: %v", out, err)
	}
	if res.Pending {
		t.Errorf("fresh-env prep must report pending=false, got %+v", res)
	}
}

// TestPendingBankPathMatchesFeedbackPackage: pendingBankPath is a thin alias
// over feedback.PendingPath — cmd/ferret must not re-derive the filename
// scheme (single source of truth for where the bank file lives).
func TestPendingBankPathMatchesFeedbackPackage(t *testing.T) {
	dir := t.TempDir()
	got := pendingBankPath(dir, "s1")
	want := filepath.Join(dir, "feedback-pending-s1.json")
	if got != want {
		t.Errorf("pendingBankPath = %q, want %q", got, want)
	}
}

// TestCursorPathIsSessionScoped: the cursor file is per-session (like the
// bank, unlike the shared budget) — two sessions must resolve to distinct
// paths so one session's tail position never clobbers another's.
func TestCursorPathIsSessionScoped(t *testing.T) {
	dir := t.TempDir()
	a := cursorPath(dir, "s1")
	b := cursorPath(dir, "s2")
	if a == b {
		t.Errorf("cursorPath must be session-scoped, got the same path %q for both sessions", a)
	}
}
