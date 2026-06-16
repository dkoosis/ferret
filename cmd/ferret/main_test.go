package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/mine"
)

// fixtureScores builds n lo→hi sorted StreamScores with distinct stream keys,
// mimicking ScoreSurprise's output so splitSurprise can be tested without a
// disk corpus.
func fixtureScores(n int) []mine.StreamScore {
	out := make([]mine.StreamScore, n)
	for i := range out {
		out[i] = mine.StreamScore{
			Stream: fmt.Sprintf("sess-%02d", i),
			Toks:   10 + i,
			Bits:   float64(i), // already sorted low→high
		}
	}
	return out
}

// TestSplitSurpriseNoOverlap guards ferret-045: cmdSurprise split the lo→hi
// score stream into "most routine" (front) and "most surprising" (back)
// sections with two independent slices that overlapped on small corpora —
// when len(scores) <= limit the same streams rendered in BOTH sections. The
// partition must keep the sections disjoint at every corpus size, and both the
// text and JSON output paths consume the same split so they stay in parity.
func TestSplitSurpriseNoOverlap(t *testing.T) {
	const limit = 20 // cmdSurprise's default; half = 10
	for _, n := range []int{0, 1, 2, 3, 5, 9, 10, 11, 15, 19, 20, 21, 40} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			scores := fixtureScores(n)
			routine, thrash := splitSurprise(scores, limit)

			// No stream may appear in both sections (the bug).
			seen := map[string]bool{}
			for _, s := range routine {
				seen[s.Stream] = true
			}
			for _, s := range thrash {
				if seen[s.Stream] {
					t.Errorf("n=%d: stream %q listed in both routine and thrash", n, s.Stream)
				}
			}

			// Each section is capped at limit/2.
			if half := limit / 2; len(routine) > half || len(thrash) > half {
				t.Errorf("n=%d: section over cap: routine=%d thrash=%d half=%d",
					n, len(routine), len(thrash), half)
			}

			// routine holds the lowest-bits streams, thrash the highest.
			if len(routine) > 0 && len(thrash) > 0 {
				if routine[0].Bits > thrash[0].Bits {
					t.Errorf("n=%d: routine should hold lower bits than thrash", n)
				}
			}
		})
	}
}

// TestManifestComplete guards the ensureData completeness gate: a bare
// os.Stat is not enough. A 0-byte manifest (interrupted ingest) or a
// truncated/invalid-JSON manifest must NOT count as a complete corpus,
// or ensureData skips re-ingest and silently mines a partial events.jsonl.
func TestManifestComplete(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "manifest.json")

	if manifestComplete(p) {
		t.Error("missing manifest must not be complete")
	}

	if err := os.WriteFile(p, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if manifestComplete(p) {
		t.Error("0-byte manifest must not be complete (interrupted ingest)")
	}

	if err := os.WriteFile(p, []byte(`{"root":"/x","stats":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if manifestComplete(p) {
		t.Error("truncated/invalid-JSON manifest must not be complete")
	}

	if err := os.WriteFile(p, []byte(`{"root":"/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !manifestComplete(p) {
		t.Error("non-empty, valid-JSON manifest must be complete")
	}
}

// writeTestManifest writes a manifest.json with a controlled CreatedAt/Root so
// the staleness helpers can be exercised without a real ingest.
func writeTestManifest(t *testing.T, path, root string, createdAt time.Time) {
	t.Helper()
	b, err := json.Marshal(event.Manifest{CreatedAt: createdAt, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeTranscript drops a *.jsonl under a project-slug dir (transcript.Walk
// requires ≥2 path segments) and stamps it with the given mtime.
func writeTranscript(t *testing.T, root, slug, name string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestNewestTranscriptMod: the helper returns the most recent transcript mtime
// under root, and degrades to the zero time for an empty/unreadable tree rather
// than erroring (staleness is advisory).
func TestNewestTranscriptMod(t *testing.T) {
	if got := newestTranscriptMod(filepath.Join(t.TempDir(), "absent")); !got.IsZero() {
		t.Errorf("unreadable root must yield zero time, got %v", got)
	}

	root := t.TempDir()
	if got := newestTranscriptMod(root); !got.IsZero() {
		t.Errorf("empty root must yield zero time, got %v", got)
	}

	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeTranscript(t, root, "proj-a", "old.jsonl", base)
	newest := base.Add(30 * time.Minute)
	writeTranscript(t, root, "proj-b", "new.jsonl", newest)

	if got := newestTranscriptMod(root); !got.Equal(newest) {
		t.Errorf("newestTranscriptMod = %v, want newest %v", got, newest)
	}
}

// TestCorpusStale guards ferret-17q: ensureData served a built corpus forever,
// never noticing that the source transcripts had moved on. corpusStale must
// report true when any transcript under the manifest's recorded root is newer
// than the corpus build time, and stay silent (false) when the manifest is
// missing/unreadable or records no root — staleness is advisory, so absence of
// evidence must not produce a spurious warning.
func TestCorpusStale(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	manifestPath := filepath.Join(dataDir, "manifest.json")

	built := time.Now().Add(-time.Hour).Truncate(time.Second)
	tx := writeTranscript(t, root, "proj-slug", "sess1.jsonl", built.Add(30*time.Minute))
	writeTestManifest(t, manifestPath, root, built)

	stale, gotBuilt, gotNewest := corpusStale(manifestPath)
	if !stale {
		t.Error("transcript newer than build time must be stale")
	}
	if !gotBuilt.Equal(built) {
		t.Errorf("reported build time = %v, want %v", gotBuilt, built)
	}
	if gotNewest.Before(built) {
		t.Errorf("reported newest %v should be after build %v", gotNewest, built)
	}

	// Transcript older than the build → fresh.
	older := built.Add(-30 * time.Minute)
	if err := os.Chtimes(tx, older, older); err != nil {
		t.Fatal(err)
	}
	if stale, _, _ := corpusStale(manifestPath); stale {
		t.Error("transcript older than build time must not be stale")
	}

	// Missing manifest → advisory silence, not stale.
	if stale, _, _ := corpusStale(filepath.Join(dataDir, "absent.json")); stale {
		t.Error("missing manifest must not report stale")
	}

	// Manifest recording no root → cannot judge staleness → not stale.
	emptyRoot := filepath.Join(dataDir, "emptyroot.json")
	writeTestManifest(t, emptyRoot, "", built)
	if stale, _, _ := corpusStale(emptyRoot); stale {
		t.Error("manifest with empty root must not report stale")
	}
}

func TestMermaidLabelEscaping(t *testing.T) {
	for in, want := range map[string]string{
		`Grep:"foo"`:    "Grep:#quot;foo#quot;",
		"Read:a[0].go":  "Read:a#91;0#93;.go",
		"sh:awk {p}":    "sh:awk #123;p#125;",
		"sh:git_status": "sh:git_status",
	} {
		if got := mermaidLabel(in); got != want {
			t.Errorf("mermaidLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateFormat(t *testing.T) {
	c := &common{format: "josn"}
	if err := c.validate("text", "json"); err == nil {
		t.Error("unknown -format must fail loudly, not fall through to text")
	}
	c = &common{format: "json", maxBytes: 1024}
	err := c.validate("text", "json")
	if err == nil || !strings.Contains(err.Error(), "max-bytes") {
		t.Errorf("-max-bytes with json must be rejected, got %v", err)
	}
	c = &common{format: "json"}
	if err := c.validate("text", "json"); err != nil {
		t.Errorf("valid format rejected: %v", err)
	}
}

// TestDefaultPathsSurfaceHomeError guards against the silent-relative-path bug:
// when os.UserHomeDir fails (HOME/USERPROFILE unset under cron, systemd, minimal
// Docker, CI), defaultData/defaultRoot must NOT discard the error and return a
// relative ".ferret" / ".claude/projects" path (which then writes artifacts
// under the current working dir, making corpus location silently depend on CWD).
// They must surface the error instead.
var errTestNoHome = errors.New("$HOME is not defined")

func TestDefaultPathsSurfaceHomeError(t *testing.T) {
	orig := userHomeDir
	t.Cleanup(func() { userHomeDir = orig })
	userHomeDir = func() (string, error) {
		return "", errTestNoHome
	}

	if got, err := defaultData(); err == nil {
		t.Errorf("defaultData must surface UserHomeDir error, got path %q, nil err", got)
	}
	if got, err := defaultRoot(); err == nil {
		t.Errorf("defaultRoot must surface UserHomeDir error, got path %q, nil err", got)
	}
}

// TestDefaultPathsAbsoluteOnSuccess: the happy path still returns the expected
// absolute paths under the home dir.
func TestDefaultPathsAbsoluteOnSuccess(t *testing.T) {
	orig := userHomeDir
	t.Cleanup(func() { userHomeDir = orig })
	home := "/home/u"
	userHomeDir = func() (string, error) { return home, nil }

	d, err := defaultData()
	if err != nil || d != filepath.Join(home, ".ferret") {
		t.Errorf("defaultData() = %q, %v; want /home/u/.ferret, nil", d, err)
	}
	r, err := defaultRoot()
	if err != nil || r != filepath.Join(home, ".claude", "projects") {
		t.Errorf("defaultRoot() = %q, %v; want /home/u/.claude/projects, nil", r, err)
	}
}
