package snipeusage

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadRecordsToleratesMalformedAndEmptyCommand(t *testing.T) {
	dir := t.TempDir()
	body := `{"ts":"2026-08-01T20:48:46-04:00","cmd":"def","outcome":"ok","ms":22}
not json at all
{"ts":"2026-08-01T20:49:15-04:00","cmd":"","outcome":"ok","ms":10}
{"ts":"2026-08-01T20:52:01-04:00","cmd":"boundary","outcome":"ok","ms":22,"rung":"exact","index_state":"fresh","session_key":"sess-1"}

{"ts":"2026-08-01T20:52:39-04:00","cmd":"lits","outcome":"ok","ms":8,"rung":"exact","index_state":"fresh","session_key":"sess-1"}
`
	p := writeFile(t, dir, "usage.jsonl", body)

	recs, err := ReadRecords(p)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3 (malformed line + empty-Command row skipped): %+v", len(recs), recs)
	}
	if recs[0].Command != "def" || recs[1].Command != "boundary" || recs[2].Command != "lits" {
		t.Errorf("unexpected records: %+v", recs)
	}
}

func TestReadRecordsMissingFile(t *testing.T) {
	if _, err := ReadRecords(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Error("want error for missing file")
	}
}

func TestReadGlobCombinesMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	sub1 := filepath.Join(dir, "repo1", ".snipe")
	sub2 := filepath.Join(dir, "repo2", ".snipe")
	if err := os.MkdirAll(sub1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub2, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, sub1, "usage.jsonl", `{"ts":"2026-08-01T20:48:46-04:00","cmd":"def","outcome":"ok","ms":22,"session_key":"a"}`+"\n")
	writeFile(t, sub2, "usage.jsonl", `{"ts":"2026-08-01T20:48:47-04:00","cmd":"sym","outcome":"ok","ms":9,"session_key":"b"}`+"\n")

	recs, err := ReadGlob(filepath.Join(dir, "*", ".snipe", "usage.jsonl"))
	if err != nil {
		t.Fatalf("ReadGlob: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (combined across both matches): %+v", len(recs), recs)
	}
}

func TestReadGlobNoMatches(t *testing.T) {
	recs, err := ReadGlob(filepath.Join(t.TempDir(), "*", "nope.jsonl"))
	if err != nil {
		t.Fatalf("ReadGlob: %v", err)
	}
	if recs != nil {
		t.Errorf("got %+v, want nil for zero matches", recs)
	}
}
