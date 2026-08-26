package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/shellnorm"
)

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

	// Newer, but inside the tolerance → fresh. The session running ferret is
	// itself appending under the ingest root, so a strict newest > builtAt test
	// reports STALE seconds after a successful ingest and never reads fresh.
	within := built.Add(staleTolerance - time.Minute)
	if err := os.Chtimes(tx, within, within); err != nil {
		t.Fatal(err)
	}
	if stale, _, _ := corpusStale(manifestPath); stale {
		t.Errorf("transcript %v newer than build must stay fresh inside the %v tolerance",
			staleTolerance-time.Minute, staleTolerance)
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

// TestEnsureData_RefusesCorpus_When_SchemaVersionIsAnEarlierEra pins the
// ferret-4wc gate: a corpus from a previous artifact era is refused with the
// re-ingest command named, NOT silently mined. Mining it would produce numbers
// that look fine beside today's and mean something different — the exact
// failure the gate exists to prevent.
func TestEnsureData_RefusesCorpus_When_SchemaVersionIsAnEarlierEra(t *testing.T) {
	dir := t.TempDir()
	writeEraManifest(t, dir, event.SchemaVersion-1)

	c := &common{data: dir}
	err := c.ensureData()
	if err == nil {
		t.Fatal("ensureData accepted a corpus from an earlier era — it must refuse")
	}
	if !strings.Contains(err.Error(), "ferret ingest") {
		t.Errorf("refusal must name the fix, got: %v", err)
	}
}

// TestEnsureData_RefusesCorpus_When_ManifestPredatesProvenance pins the
// zero-value case: manifests written before schemaVersion existed decode to 0,
// which is precisely the era boundary the gate must catch rather than wave
// through as "no version recorded, probably fine".
func TestEnsureData_RefusesCorpus_When_ManifestPredatesProvenance(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(`{"createdAt":"2026-08-01T00:00:00Z","root":"/tmp/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&common{data: dir}).ensureData(); err == nil {
		t.Error("a pre-provenance manifest (schemaVersion absent → 0) must be refused")
	}
}

// TestEnsureData_AcceptsCorpus_When_SchemaVersionMatches pins that the gate
// stays out of the way on the normal path.
func TestEnsureData_AcceptsCorpus_When_SchemaVersionMatches(t *testing.T) {
	dir := t.TempDir()
	writeEraManifest(t, dir, event.SchemaVersion)
	if err := (&common{data: dir}).ensureData(); err != nil {
		t.Errorf("ensureData refused a current-era corpus: %v", err)
	}
}

// writeEraManifest writes a complete manifest at a chosen schema version, with
// a root that does not exist so the staleness probe stays silent.
func writeEraManifest(t *testing.T, dir string, version int) {
	t.Helper()
	m := event.Manifest{
		SchemaVersion: version,
		CreatedAt:     time.Now(),
		Root:          filepath.Join(dir, "no-such-root"),
		Provenance:    event.Provenance{Ferret: "abc123", Normalizer: shellnorm.Version},
		Stats:         event.NewStats(),
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestEraDrift_NamesTheDifferingPart_When_ProvenanceDisagrees pins the
// ferret-4wc reporting AC: status must say WHICH era moved, because "stale"
// alone cannot separate "transcripts changed" from "ferret changed" and the
// two have different remedies.
func TestEraDrift_NamesTheDifferingPart_When_ProvenanceDisagrees(t *testing.T) {
	cases := []struct {
		name string
		m    event.Manifest
		want string
	}{
		{"schema", event.Manifest{SchemaVersion: event.SchemaVersion - 1}, "schema"},
		{"normalizer", event.Manifest{SchemaVersion: event.SchemaVersion,
			Provenance: event.Provenance{Normalizer: "0", Ferret: buildRevision()}}, "normalizer"},
		{"build", event.Manifest{SchemaVersion: event.SchemaVersion,
			Provenance: event.Provenance{Normalizer: shellnorm.Version, Ferret: "deadbeef0000"}}, "built by ferret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drift, why := eraDrift(&tc.m)
			if !drift {
				t.Fatalf("expected drift for %s, got none", tc.name)
			}
			if !strings.Contains(why, tc.want) {
				t.Errorf("reason %q does not name %q", why, tc.want)
			}
		})
	}

	same := event.Manifest{SchemaVersion: event.SchemaVersion,
		Provenance: event.Provenance{Normalizer: shellnorm.Version, Ferret: buildRevision()}}
	if drift, why := eraDrift(&same); drift {
		t.Errorf("matching provenance reported drift: %s", why)
	}
}
