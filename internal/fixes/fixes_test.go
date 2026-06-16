package fixes

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAppendLoadRoundTrip: a fix written through Append must come back from
// Load with every field intact and in append order. This is the ledger's core
// contract — a recorded fix has to survive to the next ingest or the loop never
// closes.
func TestAppendLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixes.jsonl")
	at := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	want := []Entry{
		{Motif: "Edit!,Read", Fix: "hookify read-before-edit", Note: "top burn", AddedAt: at, BaselineBurn: 253000},
		{Motif: "Write!,Read,Write", Fix: "skill: write-once", AddedAt: at.Add(time.Hour), BaselineBurn: 41000},
	}
	for _, e := range want {
		if err := Append(path, e); err != nil {
			t.Fatalf("Append(%q): %v", e.Motif, err)
		}
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Load returned %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Motif != want[i].Motif || got[i].Fix != want[i].Fix ||
			got[i].Note != want[i].Note || got[i].BaselineBurn != want[i].BaselineBurn ||
			!got[i].AddedAt.Equal(want[i].AddedAt) {
			t.Errorf("entry %d round-trip mismatch:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

// TestAppendCreatesLedgerDir: Append must create the data dir on first use, so
// a fix can be recorded before any ingest has materialised ~/.ferret.
func TestAppendCreatesLedgerDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "fixes.jsonl")
	if err := Append(path, Entry{Motif: "A,B", Fix: "x"}); err != nil {
		t.Fatalf("Append into missing dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ledger not created: %v", err)
	}
}

// TestLoadMissingLedgerIsEmpty: a missing ledger is an empty ledger, not an
// error — report --since-fixes must be able to join unconditionally before any
// fix has ever been recorded.
func TestLoadMissingLedgerIsEmpty(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("Load(missing) must not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Load(missing) = %d entries, want 0", len(got))
	}
}

// TestLoadCorruptLineErrors: a malformed ledger line is a hard error, not a
// silent skip — a corrupt ledger that joined as "no fixes" would erase the
// loop's memory without warning.
func TestLoadCorruptLineErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixes.jsonl")
	if err := os.WriteFile(path, []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("corrupt ledger line must surface an error, not parse as empty")
	}
}

// TestMotifKey: the join key is the comma-joined token sequence — the single
// definition both the writer and reader share so they cannot drift.
func TestMotifKey(t *testing.T) {
	if got := MotifKey([]string{"Edit!", "Read"}); got != "Edit!,Read" {
		t.Errorf("MotifKey = %q, want %q", got, "Edit!,Read")
	}
	if got := MotifKey(nil); got != "" {
		t.Errorf("MotifKey(nil) = %q, want empty", got)
	}
}

// TestIndexLatestWins: when a motif is fixed, regresses, and is re-fixed, the
// ledger keeps all rows but Index resolves to the most recent — report must
// annotate with the live fix, not a superseded one.
func TestIndexLatestWins(t *testing.T) {
	at := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	entries := []Entry{
		{Motif: "Edit!,Read", Fix: "first attempt", AddedAt: at, BaselineBurn: 253000},
		{Motif: "Grep,Read", Fix: "unrelated", AddedAt: at, BaselineBurn: 9000},
		{Motif: "Edit!,Read", Fix: "re-fix after regression", AddedAt: at.Add(48 * time.Hour), BaselineBurn: 120000},
	}
	idx := Index(entries)
	if len(idx) != 2 {
		t.Fatalf("Index size = %d, want 2 distinct motifs", len(idx))
	}
	got := idx["Edit!,Read"]
	if got.Fix != "re-fix after regression" || got.BaselineBurn != 120000 {
		t.Errorf("Index kept superseded entry: %+v", got)
	}
}

// TestAnnotationArrow: the delta direction reads ↓ when burn fell (the fix
// landed), ↑ when it rose (regression), = when unchanged.
func TestAnnotationArrow(t *testing.T) {
	base := Entry{BaselineBurn: 253000}
	cases := []struct {
		current int
		want    string
	}{
		{11000, "↓"},
		{300000, "↑"},
		{253000, "="},
	}
	for _, tc := range cases {
		got := Annotation{Entry: base, Current: tc.current}.Arrow()
		if got != tc.want {
			t.Errorf("Arrow(baseline=%d, current=%d) = %q, want %q", base.BaselineBurn, tc.current, got, tc.want)
		}
	}
}
