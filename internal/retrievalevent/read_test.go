package retrievalevent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goldenPath = "testdata/retrieval-golden.jsonl"

// TestReadEventsGolden ingests the vendored producer fixture (tx-dii8m,
// verbatim copy of trixi's retrieval-golden.jsonl) and asserts the contract
// shape: 12 rows, 10 search / 2 read, every row schema_version==1, and the
// session-fallback row (event_id 8192031425364a5b) is flagged so callers can
// filter it (Trap 2 — agent_id degraded to session_id).
func TestReadEventsGolden(t *testing.T) {
	events, err := ReadEvents(goldenPath)
	if err != nil {
		t.Fatalf("ReadEvents(%s): %v", goldenPath, err)
	}
	if len(events) != 12 {
		t.Fatalf("len(events) = %d, want 12", len(events))
	}

	var search, read int
	var sawFallback bool
	for _, e := range events {
		if e.SchemaVersion != SchemaVersion {
			t.Errorf("event %s schema_version = %d, want %d", e.EventID, e.SchemaVersion, SchemaVersion)
		}
		switch e.Kind {
		case KindSearch:
			search++
		case KindRead:
			read++
		default:
			t.Errorf("event %s has unknown kind %q", e.EventID, e.Kind)
		}
		if e.EventID == "8192031425364a5b" {
			sawFallback = true
			if e.Attribution != AttributionSessionFallback {
				t.Errorf("fallback row attribution = %q, want %q", e.Attribution, AttributionSessionFallback)
			}
		}
	}
	if search != 10 {
		t.Errorf("search rows = %d, want 10", search)
	}
	if read != 2 {
		t.Errorf("read rows = %d, want 2", read)
	}
	if !sawFallback {
		t.Fatal("did not see the session-fallback row (event_id 8192031425364a5b) in the fixture")
	}
}

// TestReadEventsSchemaMismatch asserts contract Trap 1: a row whose
// schema_version does not match the version this reader was built against
// makes ReadEvents fail loudly rather than compute silently-wrong joins.
func TestReadEventsSchemaMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	line := `{"schema_version":2,"kind":"search","event_id":"deadbeef","ts":"2026-06-29T20:52:00Z","session_id":"s","agent_id":"a","agent_type":"parent"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ReadEvents(path)
	if err == nil {
		t.Fatal("ReadEvents with schema_version=2 row: want error, got nil")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("error = %q, want it to mention schema_version", err.Error())
	}
}

// TestLinkReads asserts the read→search join surface: a read row's
// search_ref resolves to its owning search event.
func TestLinkReads(t *testing.T) {
	events, err := ReadEvents(goldenPath)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	links := LinkReads(events)
	if len(links) != 2 {
		t.Fatalf("len(links) = %d, want 2 (one per read row)", len(links))
	}
	for searchID, reads := range links {
		if searchID == "" {
			t.Error("empty search key in links")
		}
		if len(reads) == 0 {
			t.Errorf("search %s has no linked reads", searchID)
		}
	}
}
