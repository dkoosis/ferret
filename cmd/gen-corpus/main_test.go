package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
