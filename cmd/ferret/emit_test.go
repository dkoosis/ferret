package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	candrec "github.com/dkoosis/ferret/internal/candidate"
	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/spool"
	"github.com/dkoosis/ferret/internal/transcript"
)

// TestEmitCursor_MidRunGrowthNotSkipped is the regression for the cursor TOCTOU:
// a transcript that grows AFTER changed() decides it is active but before the run
// finishes must stay visibly changed for the next run. The cursor persists the
// modtime OBSERVED at decision time (not a post-work re-stat), so a later growth
// is still After() the stored value and re-activates the source. The pre-fix code
// re-stat'd in mark() and stored the grown modtime, silently skipping the growth.
func TestEmitCursor_MidRunGrowthNotSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess1.jsonl")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m0 := time.Date(2026, 7, 28, 4, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, m0, m0); err != nil {
		t.Fatal(err)
	}
	sources := map[string]transcript.Source{"k": {Path: path, Session: "sess1"}}

	cur := &emitCursor{Sources: map[string]string{}}
	active, observed := cur.changed(sources)
	if len(active) != 1 {
		t.Fatalf("fresh cursor should mark the source active, got %d", len(active))
	}

	// Simulate the transcript growing DURING the run (after the changed() stat).
	m1 := m0.Add(time.Hour)
	if err := os.Chtimes(path, m1, m1); err != nil {
		t.Fatal(err)
	}
	cur.mark(observed) // must store m0 (observed), not re-stat to m1

	active2, _ := cur.changed(sources)
	if len(active2) != 1 {
		t.Errorf("mid-run growth was skipped: source not re-activated next run (stored=%q)", cur.Sources[path])
	}
}

// emitFixture writes a small events.jsonl under data and a matching transcript
// file under root, so transcript.Walk's source key aligns with the corpus stream
// key (project/session@agent). Returns the transcript path for path-threading
// assertions.
func emitFixture(t *testing.T, data, root string) string {
	t.Helper()
	evPath := filepath.Join(data, "events.jsonl")
	w, err := event.NewWriter(evPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	actions := []string{"Read", "Edit", "Read", "Bash", "Grep", "Write"}
	for i, act := range actions {
		ev := event.Event{
			Seq: i, Project: "projslug", Session: "sess1",
			Kind: event.KindTool, Action: act, Status: event.StatusOK, Bytes: 100,
		}
		if err := w.Write(&ev); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	tdir := filepath.Join(root, "projslug")
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatal(err)
	}
	tpath := filepath.Join(tdir, "sess1.jsonl")
	if err := os.WriteFile(tpath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return tpath
}

func testEmitOpts(data, root string) emitOpts {
	return emitOpts{data: data, root: root, order: 3, window: 3, minBits: 0}
}

// TestRunEmit_EndToEnd is the golden-fixture + contract check: emit over a real
// events artifact + transcript root writes candidate rows to the monthly spool,
// each carrying the frozen shape, the threaded transcript path, and NO claim text.
func TestRunEmit_EndToEnd(t *testing.T) {
	orig := emitNow
	t.Cleanup(func() { emitNow = orig })
	at := time.Date(2026, 7, 28, 4, 12, 9, 0, time.UTC)
	emitNow = func() time.Time { return at }

	data, root := t.TempDir(), t.TempDir()
	tpath := emitFixture(t, data, root)

	res, cands, err := runEmit(testEmitOpts(data, root))
	if err != nil {
		t.Fatalf("runEmit: %v", err)
	}
	// 6 tokens, window 3 → 2 spans, both cleared (minBits 0).
	if res.Written != 2 || len(cands) != 2 {
		t.Fatalf("written=%d derived=%d, want 2/2", res.Written, len(cands))
	}

	spoolFile := spool.Path(spool.Dir(data), at)
	b, err := os.ReadFile(spoolFile)
	if err != nil {
		t.Fatalf("read spool %s: %v", spoolFile, err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("spool rows = %d, want 2", len(lines))
	}
	for _, ln := range lines {
		var c candrec.Candidate
		if err := json.Unmarshal([]byte(ln), &c); err != nil {
			t.Fatalf("row not valid JSON: %v", err)
		}
		if c.SchemaVersion != 1 || c.Sensor != "ferret" {
			t.Errorf("bad envelope: %+v", c)
		}
		if c.Source.TranscriptPath != tpath {
			t.Errorf("transcript_path = %q, want threaded %q", c.Source.TranscriptPath, tpath)
		}
		if c.Source.Agent != "parent" {
			t.Errorf("agent = %q, want parent", c.Source.Agent)
		}
		if !strings.HasPrefix(c.Signals.Fingerprint, "sha256:") {
			t.Errorf("fingerprint %q not sha256-prefixed", c.Signals.Fingerprint)
		}
		if c.Signals.Recurrence < 1 {
			t.Errorf("recurrence = %d, want ≥1", c.Signals.Recurrence)
		}
		for _, banned := range []string{"claim", `"text"`, `"fact"`} {
			if strings.Contains(ln, banned) {
				t.Errorf("row leaked %q — LLM-free invariant broken:\n%s", banned, ln)
			}
		}
	}
}

// TestRunEmit_Idempotent asserts a second run over an unchanged corpus writes no
// new rows (cursor short-circuit + id dedup), and the spool file is unchanged.
func TestRunEmit_Idempotent(t *testing.T) {
	orig := emitNow
	t.Cleanup(func() { emitNow = orig })
	at := time.Date(2026, 7, 28, 4, 0, 0, 0, time.UTC)
	emitNow = func() time.Time { return at }

	data, root := t.TempDir(), t.TempDir()
	emitFixture(t, data, root)

	first, _, err := runEmit(testEmitOpts(data, root))
	if err != nil {
		t.Fatal(err)
	}
	spoolFile := spool.Path(spool.Dir(data), at)
	before, _ := os.ReadFile(spoolFile)

	second, _, err := runEmit(testEmitOpts(data, root))
	if err != nil {
		t.Fatal(err)
	}
	if second.Written != 0 {
		t.Errorf("second run wrote %d rows, want 0 (first wrote %d)", second.Written, first.Written)
	}
	after, _ := os.ReadFile(spoolFile)
	if !bytes.Equal(before, after) {
		t.Error("spool file changed on idempotent re-run")
	}
}

// TestRunEmit_DryRunWritesNothing asserts --dry-run derives candidates but leaves
// the spool, cursor, and signatures files untouched.
func TestRunEmit_DryRunWritesNothing(t *testing.T) {
	orig := emitNow
	t.Cleanup(func() { emitNow = orig })
	emitNow = func() time.Time { return time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC) }

	data, root := t.TempDir(), t.TempDir()
	emitFixture(t, data, root)

	opts := testEmitOpts(data, root)
	opts.dryRun = true
	res, cands, err := runEmit(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) == 0 || res.Written == 0 {
		t.Fatalf("dry-run should derive would-write candidates, got derived=%d written=%d", len(cands), res.Written)
	}
	if _, err := os.Stat(spool.Dir(data)); !os.IsNotExist(err) {
		t.Errorf("dry-run created a spool dir (err=%v)", err)
	}
	if _, err := os.Stat(emitCursorPath(data)); !os.IsNotExist(err) {
		t.Error("dry-run wrote a cursor")
	}
}
