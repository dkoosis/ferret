package event

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// errBoomAtomic is a static sentinel (err113: no dynamic errors in tests)
// returned by the failing writer used to force a flush error mid-run.
var errBoomAtomic = errors.New("boom-atomic")

// hookSyncDir swaps the package syncDir seam for the duration of a test,
// recording every directory path it is asked to fsync, and restores the
// real implementation on cleanup.
func hookSyncDir(t *testing.T) *[]string {
	t.Helper()
	var dirs []string
	orig := syncDir
	syncDir = func(dir string) error {
		dirs = append(dirs, dir)
		return orig(dir)
	}
	t.Cleanup(func() { syncDir = orig })
	return &dirs
}

// TestWriterClose_SyncsParentDir asserts the publish path fsyncs the parent
// directory after the rename so the rename is crash-durable. os.Rename only
// mutates the directory entry; without fsync'ing the dir inode the rename can
// be lost on power-loss, diverging from the manifest.
func TestWriterClose_SyncsParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	dirs := hookSyncDir(t)

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

	if !slices.Contains(*dirs, dir) {
		t.Errorf("expected parent dir %q fsync'd after rename, got dirs=%v", dir, *dirs)
	}
}

// TestWriteManifest_SyncsParentDir asserts WriteManifest also fsyncs the
// parent directory after its rename, so the manifest and events.jsonl cannot
// diverge across a power-loss window.
func TestWriteManifest_SyncsParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	dirs := hookSyncDir(t)

	if err := WriteManifest(path, &Manifest{Root: "/some/root", Stats: &Stats{}}); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	if !slices.Contains(*dirs, dir) {
		t.Errorf("expected parent dir %q fsync'd after rename, got dirs=%v", dir, *dirs)
	}
}

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
	assertNoDanglingTmp(t, path)

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
	if !bytes.Equal(gotBytes, priorBytes) {
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
	assertNoDanglingTmp(t, path)
}

// assertNoDanglingTmp fails if any per-process temp sibling (events.jsonl.*.tmp,
// the unique pattern NewWriter now creates for ferret-0vz) survives a finished
// run — the artifact should be published by rename and the tmp removed.
func assertNoDanglingTmp(t *testing.T, path string) {
	t.Helper()
	matches, err := filepath.Glob(path + ".*.tmp")
	if err != nil {
		t.Fatalf("glob tmp siblings: %v", err)
	}
	if len(matches) > 0 {
		t.Errorf("expected no dangling .tmp, found %v", matches)
	}
}

// TestWriteManifestAtomic asserts WriteManifest publishes atomically: the
// final manifest is valid JSON, no .tmp is left dangling, and round-trips.
// A bare os.WriteFile truncates then writes — a crash mid-write would leave
// a 0-byte/partial completeness sentinel.
func TestWriteManifestAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	m := &Manifest{Root: "/some/root", Stats: &Stats{}}
	if err := WriteManifest(path, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(b) == 0 || !json.Valid(b) {
		t.Errorf("manifest not valid non-empty JSON: %q", b)
	}
	var got Manifest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if got.Root != "/some/root" {
		t.Errorf("round-trip Root = %q, want /some/root", got.Root)
	}

	// No dangling temp file after a successful publish.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected manifest .tmp cleaned up, stat err=%v", err)
	}
}
