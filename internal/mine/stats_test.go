package mine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// writeEventsFile serializes evs to a temp events.jsonl and returns its path.
func writeEventsFile(t *testing.T, evs []event.Event) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := event.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := range evs {
		if err := w.Write(&evs[i]); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

// TestSummarize_CountsCwdResetsPerBucket covers step 9: a CwdReset event
// counts into its bucket's CwdResets field, corpus-wide.
func TestSummarize_CountsCwdResetsPerBucket(t *testing.T) {
	evs := []event.Event{
		{Project: "p1", Session: "s1", Kind: event.KindShell, Action: "ls", CwdReset: true},
		{Project: "p1", Session: "s1", Kind: event.KindShell, Action: "cat"},
		{Project: "p1", Session: "s2", Kind: event.KindShell, Action: "ls", CwdReset: true},
	}
	path := writeEventsFile(t, evs)
	s, err := Summarize(path, "corpus")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(s.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(s.Buckets))
	}
	if s.Buckets[0].CwdResets != 2 {
		t.Errorf("CwdResets = %d, want 2", s.Buckets[0].CwdResets)
	}
}

// TestSummarize_LegacyCorpusNoCwdResetsField covers backward compatibility: a
// corpus written before CwdReset existed decodes with the count at zero, no
// error — mirrors internal/event's legacy-decode contract.
func TestSummarize_LegacyCorpusNoCwdResetsField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	line := `{"i":1,"p":"p1","s":"s1","k":"shell","act":"ls"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Summarize(path, "corpus")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(s.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(s.Buckets))
	}
	if s.Buckets[0].CwdResets != 0 {
		t.Errorf("CwdResets = %d, want 0 (legacy corpus, field absent)", s.Buckets[0].CwdResets)
	}
}
