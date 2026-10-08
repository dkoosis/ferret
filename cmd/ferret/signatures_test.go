package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/friction"
	"github.com/dkoosis/ferret/internal/shellnorm"
)

// writeNug writes one mnemd-shaped nug file (YAML frontmatter + body) under
// dir and returns its path.
func writeNug(t *testing.T, dir, name, id, kind, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	fm := "---\nid: '" + id + "'\nkind: '" + kind + "'\ngeneratedBy: 'test'\n---\n\n" + body + "\n"
	if err := os.WriteFile(p, []byte(fm), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// failedShell builds the event ingest would store for a failed Bash call of
// cmd: split with shellnorm, first segment's Cmd as Action and Raw as Detail,
// status fail — the same shape internal/event's shellSegmentEvent produces.
func failedShell(t *testing.T, session string, seq int, cmd string) event.Event {
	t.Helper()
	segs, _ := shellnorm.Split(cmd)
	if len(segs) == 0 {
		t.Fatalf("shellnorm.Split(%q) yielded no segments", cmd)
	}
	return event.Event{
		Session: session,
		Seq:     seq,
		Kind:    event.KindShell,
		Action:  segs[0].Cmd,
		Detail:  segs[0].Raw,
		Status:  event.StatusFail,
	}
}

// TestRunSignaturesSeed_FirstFreshSightingIsOccurrence2 is the bead's
// acceptance test (ferret-7gu): seed one friction nug, run the seeder, then
// show the recurrence detector — loaded from the file the seeder wrote, exactly
// as cmdRecurrence loads it — flags that friction's NEXT sighting as
// occurrence 2, carrying the nug's id in the label.
func TestRunSignaturesSeed_FirstFreshSightingIsOccurrence2(t *testing.T) {
	nugs, data := t.TempDir(), t.TempDir()
	writeNug(t, nugs, "inbox/awk-blanked-descriptions.md", "f253ff7f4d18", "friction",
		"tool: bd update --description $(pipeline). problem: awk failed on a multi-line -v string and bd update wrote the empty string. resolution: write the body to a file first.")
	sigPath := friction.SigPath(data)

	res, err := runSignaturesSeed(nugs, sigPath)
	if err != nil {
		t.Fatalf("runSignaturesSeed: %v", err)
	}
	if res.Nugs != 1 || res.Signatures != 1 || res.Added != 1 || len(res.Skipped) != 0 {
		t.Fatalf("result = %+v, want 1 nug → 1 signature, 1 added, none skipped", res)
	}

	sigs, err := friction.LoadSignatures(sigPath)
	if err != nil {
		t.Fatalf("LoadSignatures: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("signatures on disk = %d, want 1", len(sigs))
	}
	if !strings.Contains(sigs[0].Label, "(nug f253ff7f4d18)") {
		t.Errorf("label %q does not carry the nug id", sigs[0].Label)
	}

	// A fresh corpus in which the recorded trap happens ONCE: without the seed
	// this is a first sighting and nothing is flagged; with it, occurrence 2.
	fresh := []event.Event{
		failedShell(t, "s-new", 7, `bd update --description $(awk -v body="$x" '{print}' f)`),
	}
	if got := friction.Scan(nil, fresh); len(got) != 0 {
		t.Fatalf("unseeded detector flagged %d matches on a single sighting, want 0", len(got))
	}
	matches := friction.Scan(sigs, fresh)
	if len(matches) != 1 {
		t.Fatalf("seeded detector flagged %d matches, want 1", len(matches))
	}
	m := matches[0]
	if m.Occurrence != 2 || m.Session != "s-new" || m.Seq != 7 {
		t.Errorf("match = %+v, want occurrence 2 at s-new#7", m)
	}
	if m.Label != sigs[0].Label {
		t.Errorf("match label %q, want the seeded label %q", m.Label, sigs[0].Label)
	}
}

// TestRunSignaturesSeed_SkipsNonFrictionAndNoTool asserts the walk admits only
// kind friction, skips dot-dirs, reports a tool-less friction nug by id rather
// than failing, and is idempotent (a second run adds nothing).
func TestRunSignaturesSeed_SkipsNonFrictionAndNoTool(t *testing.T) {
	nugs, data := t.TempDir(), t.TempDir()
	writeNug(t, nugs, "a.md", "aaaaaaaaaaaa", "friction", "tool: go test ./internal/x. problem: flaky.")
	writeNug(t, nugs, "b.md", "bbbbbbbbbbbb", "reference/pin", "tool: go test ./internal/x. problem: not friction.")
	writeNug(t, nugs, "c.md", "cccccccccccc", "friction", "I forgot the grammar entirely.")
	writeNug(t, nugs, ".mnemd/cache.md", "dddddddddddd", "friction", "tool: rm -rf /. problem: cache dir must be skipped.")
	if err := os.WriteFile(filepath.Join(nugs, "plain.md"), []byte("no frontmatter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sigPath := friction.SigPath(data)

	res, err := runSignaturesSeed(nugs, sigPath)
	if err != nil {
		t.Fatalf("runSignaturesSeed: %v", err)
	}
	if res.Nugs != 2 || res.Signatures != 1 || res.Added != 1 {
		t.Errorf("result = %+v, want 2 friction nugs → 1 signature, 1 added", res)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "cccccccccccc" {
		t.Errorf("skipped = %v, want the tool-less nug only", res.Skipped)
	}

	again, err := runSignaturesSeed(nugs, sigPath)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if again.Added != 0 {
		t.Errorf("second run added %d, want 0 (idempotent)", again.Added)
	}
	sigs, err := friction.LoadSignatures(sigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 1 {
		t.Errorf("signatures on disk = %d, want 1", len(sigs))
	}
}
