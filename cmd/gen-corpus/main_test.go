package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errInjectedRename = errors.New("injected rename failure")

// TestFlushRollsBackOnSubagentFailure forces the subagent write to fail and
// asserts that no orphaned session .jsonl is left in projDir. The session file
// references a subagent transcript; if it survived a subagent-write failure a
// consumer would see a session pointing at a file that does not exist.
func TestFlushRollsBackOnSubagentFailure(t *testing.T) {
	projDir := t.TempDir()
	session := "sess-0000-deadbeef"

	// Block MkdirAll(projDir/<session>/subagents): plant a regular file where
	// the session directory needs to be, so the subagent write path fails.
	blocker := filepath.Join(projDir, session)
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("plant blocker: %v", err)
	}

	g := &gen{
		projDir:  projDir,
		project:  "demo",
		session:  session,
		lines:    []string{`{"type":"user"}`},
		subLines: []string{`{"type":"assistant","isSidechain":true}`},
		subAgent: "explore-01",
	}

	if err := g.flush(); err == nil {
		t.Fatal("flush: expected error from blocked subagent write, got nil")
	}

	// The acceptance criterion: no orphaned session .jsonl remains in projDir.
	sessionFile := filepath.Join(projDir, session+".jsonl")
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Fatalf("orphaned session file present: stat err = %v, want IsNotExist", err)
	}
}

// TestFlushWritesBothArtifactsOnSuccess guards the happy path: when nothing
// fails, both the session file and the subagent file land on disk.
func TestFlushWritesBothArtifactsOnSuccess(t *testing.T) {
	projDir := t.TempDir()
	session := "sess-0001-cafef00d"

	g := &gen{
		projDir:  projDir,
		project:  "demo",
		session:  session,
		lines:    []string{`{"type":"user"}`},
		subLines: []string{`{"type":"assistant","isSidechain":true}`},
		subAgent: "explore-01",
	}

	if err := g.flush(); err != nil {
		t.Fatalf("flush: unexpected error: %v", err)
	}

	sessionFile := filepath.Join(projDir, session+".jsonl")
	if _, err := os.Stat(sessionFile); err != nil {
		t.Fatalf("session file missing: %v", err)
	}
	subFile := filepath.Join(projDir, session, "subagents", "agent-explore-01.jsonl")
	if _, err := os.Stat(subFile); err != nil {
		t.Fatalf("subagent file missing: %v", err)
	}
}

// TestFlushNoSubagentWritesSessionOnly covers the common archetype with no
// subagent lines: only the session file is written, no subagents dir created.
func TestFlushNoSubagentWritesSessionOnly(t *testing.T) {
	projDir := t.TempDir()
	session := "sess-0002-0badf00d"

	g := &gen{
		projDir: projDir,
		project: "demo",
		session: session,
		lines:   []string{`{"type":"user"}`},
	}

	if err := g.flush(); err != nil {
		t.Fatalf("flush: unexpected error: %v", err)
	}
	sessionFile := filepath.Join(projDir, session+".jsonl")
	if _, err := os.Stat(sessionFile); err != nil {
		t.Fatalf("session file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projDir, session)); !os.IsNotExist(err) {
		t.Fatalf("unexpected session dir created: stat err = %v, want IsNotExist", err)
	}
}

// TestFlush_LeavesNoTmpArtifact_When_WriteSucceeds asserts the atomic write
// path (tmp+rename) cleans up after itself: a successful flush must leave only
// the final files, never a stray *.tmp sibling that a corpus consumer would
// trip over.
func TestFlush_LeavesNoTmpArtifact_When_WriteSucceeds(t *testing.T) {
	projDir := t.TempDir()
	session := "sess-0003-feedface"

	g := &gen{
		projDir:  projDir,
		project:  "demo",
		session:  session,
		lines:    []string{`{"type":"user"}`},
		subLines: []string{`{"type":"assistant","isSidechain":true}`},
		subAgent: "explore-01",
	}
	if err := g.flush(); err != nil {
		t.Fatalf("flush: unexpected error: %v", err)
	}

	var tmps []string
	err := filepath.WalkDir(projDir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".tmp") {
			tmps = append(tmps, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(tmps) != 0 {
		t.Fatalf("stray tmp artifacts left after successful flush: %v", tmps)
	}
}

// TestWriteAtomic_PreservesExistingFile_When_RenameFails asserts the core
// atomicity invariant: writeAtomic must never replace the destination with a
// torn/partial file. We inject a rename failure (via the renameFile seam) and
// assert the pre-existing destination still holds its ORIGINAL, complete content
// — not a truncated or empty file — and that no stray *.tmp sibling is left.
//
// The failure is injected, not filesystem-permission-based: a chmod(0o500) guard
// is a no-op under root (CI runs as root), so rename succeeds and the torn-file
// guard goes unexercised (ferret-e3q). The seam makes the failure path fire
// regardless of process privilege.
//
// A bare os.WriteFile truncates-then-writes in place, so a failure after the
// truncate leaves a torn file; a tmp+Sync+Rename writes to a sibling and only
// swaps on success, so the original survives intact. This test fails against
// the non-atomic implementation (which has no writeAtomic and clobbers in
// place) and passes once flush routes through tmp+Sync+Rename.
func TestWriteAtomic_PreservesExistingFile_When_RenameFails(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.jsonl")

	const original = "line-1\nline-2\nline-3\n"
	if err := os.WriteFile(dest, []byte(original), 0o600); err != nil {
		t.Fatalf("seed original: %v", err)
	}

	// Inject a failing rename so the publish step fails deterministically,
	// independent of process privilege.
	orig := renameFile
	renameFile = func(string, string) error { return errInjectedRename }
	t.Cleanup(func() { renameFile = orig })

	err := writeAtomic(dest, []byte("torn"))
	if err == nil {
		t.Fatal("writeAtomic: expected error from injected rename failure, got nil")
	}
	if !errors.Is(err, errInjectedRename) {
		t.Fatalf("writeAtomic: want injected rename error, got %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != original {
		t.Fatalf("destination clobbered by failed write: got %q, want original %q", got, original)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("stray tmp artifact left after failed write: %s", e.Name())
		}
	}
}

// TestWriteAtomicSyncsParentDir guards ferret-xz8: the production codec fsyncs
// the parent dir after rename so the publish survives a crash/power-loss (the
// rename only dirties the dir inode), and the fixture generator had dropped it.
// writeAtomic must call syncDir on the destination's parent after a successful
// rename.
func TestWriteAtomicSyncsParentDir(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.jsonl")

	var synced string
	orig := syncDir
	syncDir = func(d string) error { synced = d; return nil }
	t.Cleanup(func() { syncDir = orig })

	if err := writeAtomic(dest, []byte("payload\n")); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	if synced != dir {
		t.Errorf("syncDir called with %q, want parent dir %q (rename not made durable)", synced, dir)
	}
}
