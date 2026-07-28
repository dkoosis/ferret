package spool

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/candidate"
)

// mustWriter builds a Writer, failing the test on error so the returned value is
// guaranteed non-nil (satisfies nilaway at the call sites, which would otherwise
// see an ignored error and a possibly-nil receiver).
func mustWriter(t *testing.T, dir string) *Writer {
	t.Helper()
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	return w
}

func cand(t *testing.T, session string, start int, emitted time.Time) candidate.Candidate {
	t.Helper()
	return candidate.New(
		candidate.Source{TranscriptPath: "/x/" + session + ".jsonl", Project: "proj", Session: session, SeqStart: start, SeqEnd: start + 4},
		emitted, 9.42, 3, "edit read edit",
	)
}

// TestAppend_GoldenRow writes one candidate and asserts the row on disk decodes
// back to exactly the contract shape — the golden-spool fixture check.
func TestAppend_GoldenRow(t *testing.T) {
	dir := t.TempDir()
	w := mustWriter(t, dir)
	at := time.Date(2026, 7, 28, 4, 12, 9, 0, time.UTC)
	c := cand(t, "de34f538", 112, at)
	wrote, err := w.Append(c)
	if err != nil || !wrote {
		t.Fatalf("Append: wrote=%v err=%v", wrote, err)
	}

	path := Path(dir, at)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spool file %s: %v", path, err)
	}
	var got candidate.Candidate
	if err := json.Unmarshal(bytes.TrimSpace(b), &got); err != nil {
		t.Fatalf("row not valid JSON: %v", err)
	}
	if got != c {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, c)
	}
	if got.SchemaVersion != candidate.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", got.SchemaVersion, candidate.SchemaVersion)
	}
}

// TestAppend_Idempotent asserts re-appending the same candidate (and a fresh
// Writer over the same dir, as a crash/cursor-loss restart would) writes no
// duplicate row — the re-run idempotency guarantee.
func TestAppend_Idempotent(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 7, 28, 4, 0, 0, 0, time.UTC)
	c := cand(t, "sess", 10, at)

	w1 := mustWriter(t, dir)
	if wrote, _ := w1.Append(c); !wrote {
		t.Fatal("first Append should write")
	}
	if wrote, _ := w1.Append(c); wrote {
		t.Error("second Append (same writer) should skip")
	}

	// Fresh writer = a new process after a restart: it must load the existing id
	// and still skip.
	w2, err := NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if wrote, _ := w2.Append(c); wrote {
		t.Error("Append after restart should skip an already-spooled id")
	}

	if n := countRows(t, Path(dir, at)); n != 1 {
		t.Errorf("row count = %d, want 1 (no duplicate)", n)
	}
}

// TestAppend_ConcurrentDedup asserts the under-lock on-disk re-check: two writers
// that both snapshotted the spool empty (the concurrent-process case) must not
// both append the same id. The stale-snapshot writer's Append has to catch the
// row the other wrote and skip — the in-memory seen-set alone would miss it.
func TestAppend_ConcurrentDedup(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 7, 28, 4, 0, 0, 0, time.UTC)
	c := cand(t, "sess", 10, at)

	w1 := mustWriter(t, dir) // both snapshot the (empty) spool before either writes
	w2 := mustWriter(t, dir)

	if wrote, err := w1.Append(c); err != nil || !wrote {
		t.Fatalf("w1.Append: wrote=%v err=%v", wrote, err)
	}
	// w2's seen set is stale (empty); only the under-lock disk re-check can catch
	// the duplicate.
	if wrote, err := w2.Append(c); err != nil || wrote {
		t.Errorf("stale-snapshot writer wrote a duplicate: wrote=%v err=%v", wrote, err)
	}
	if n := countRows(t, Path(dir, at)); n != 1 {
		t.Errorf("row count = %d, want 1 (no concurrent duplicate)", n)
	}
}

// TestAppend_RotationBoundary asserts candidates emitted in different months land
// in different files, named by their own emitted_at month.
func TestAppend_RotationBoundary(t *testing.T) {
	dir := t.TempDir()
	w := mustWriter(t, dir)

	jul := time.Date(2026, 7, 31, 23, 59, 0, 0, time.UTC)
	aug := time.Date(2026, 8, 1, 0, 1, 0, 0, time.UTC)
	if _, err := w.Append(cand(t, "s1", 1, jul)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(cand(t, "s2", 1, aug)); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "candidates-2026-07.jsonl")); err != nil {
		t.Errorf("July file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "candidates-2026-08.jsonl")); err != nil {
		t.Errorf("August file missing: %v", err)
	}
}

// TestAppend_FsyncsDirOnFirstCreate asserts the crash-durability publish path: the
// parent dir is fsync'd when the month file is first created (so the new file's
// directory entry survives a power loss), and NOT on a subsequent append into the
// existing file.
func TestAppend_FsyncsDirOnFirstCreate(t *testing.T) {
	orig := syncDir
	t.Cleanup(func() { syncDir = orig })
	var calls int
	syncDir = func(dir string) error { calls++; return orig(dir) }

	dir := t.TempDir()
	at := time.Date(2026, 7, 28, 4, 0, 0, 0, time.UTC)
	w := mustWriter(t, dir)

	if _, err := w.Append(cand(t, "s1", 1, at)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("syncDir calls after first create = %d, want 1", calls)
	}
	if _, err := w.Append(cand(t, "s1", 20, at)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("syncDir calls after append into existing file = %d, want still 1", calls)
	}
}

// TestLoadIDs_SalvagesCorruptTrailingRow asserts a crash-torn final row is
// dropped with a warning (not a hard error), while the ids ahead of it load — so
// one torn append does not brick the whole spool for the idempotency backstop.
func TestLoadIDs_SalvagesCorruptTrailingRow(t *testing.T) {
	orig := stderr
	t.Cleanup(func() { stderr = orig })
	var warn bytes.Buffer
	stderr = &warn

	dir := t.TempDir()
	at := time.Date(2026, 7, 28, 4, 0, 0, 0, time.UTC)
	w := mustWriter(t, dir)
	good := cand(t, "sess", 5, at)
	if _, err := w.Append(good); err != nil {
		t.Fatal(err)
	}
	// Simulate a torn append: a partial JSON line at EOF.
	f, _ := os.OpenFile(Path(dir, at), os.O_WRONLY|os.O_APPEND, 0o644)
	_, _ = f.WriteString(`{"schema_version":1,"id":"fc-torn`)
	_ = f.Close()

	ids, err := LoadIDs(dir)
	if err != nil {
		t.Fatalf("LoadIDs should salvage, got err: %v", err)
	}
	if _, ok := ids[good.ID]; !ok {
		t.Errorf("good id lost during salvage")
	}
	if warn.Len() == 0 {
		t.Error("expected a stderr warning on the salvaged trailing row")
	}
}

func countRows(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	n := 0
	s := bufio.NewScanner(f)
	for s.Scan() {
		if len(bytes.TrimSpace(s.Bytes())) > 0 {
			n++
		}
	}
	if err := s.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return n
}
