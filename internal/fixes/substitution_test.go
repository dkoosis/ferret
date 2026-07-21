package fixes

import (
	"path/filepath"
	"testing"
	"time"
)

func subFixture(intent, wrong, better string) Substitution {
	return Substitution{IntentClass: intent, WrongTool: wrong, Better: better}
}

// TestRecordSubCreateThenBump: a first record creates the rule at Occurrences 1;
// a second with the same (intent, better) bumps to 2 without adding a row, and
// refreshes the mutable fields.
func TestRecordSubCreateThenBump(t *testing.T) {
	path := filepath.Join(t.TempDir(), SubFileName)
	t0 := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)

	rule, created, err := RecordSub(path, mustExample(subFixture("symbol-callers", "rg", "snipe callers <sym>"), "rg 'Classify\\('", "sessA"), t0)
	if err != nil || !created {
		t.Fatalf("first RecordSub: created=%v err=%v", created, err)
	}
	if rule.Occurrences != 1 || !rule.AddedAt.Equal(t0) {
		t.Errorf("first rule = %+v, want occ=1 addedAt=%v", rule, t0)
	}

	rule, created, err = RecordSub(path, mustExample(subFixture("symbol-callers", "grep", "snipe callers <sym>"), "grep -n Classify", "sessB"), t1)
	if err != nil || created {
		t.Fatalf("second RecordSub: created=%v err=%v (want bump)", created, err)
	}
	if rule.Occurrences != 2 {
		t.Errorf("occ = %d, want 2", rule.Occurrences)
	}
	if !rule.AddedAt.Equal(t0) || !rule.UpdatedAt.Equal(t1) {
		t.Errorf("addedAt/updatedAt = %v/%v, want %v/%v", rule.AddedAt, rule.UpdatedAt, t0, t1)
	}
	if rule.Example != "grep -n Classify" || rule.Session != "sessB" || rule.WrongTool != "grep" {
		t.Errorf("mutable fields not refreshed on bump: %+v", rule)
	}

	subs, err := LoadSubs(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("ledger has %d rows, want 1 (bump must not append)", len(subs))
	}
}

// TestRecordSubDistinctRules: different intent OR different better = separate
// rules, not a bump.
func TestRecordSubDistinctRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), SubFileName)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	if _, _, err := RecordSub(path, subFixture("symbol-callers", "rg", "snipe callers <sym>"), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RecordSub(path, subFixture("symbol-def", "rg", "snipe def <sym>"), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RecordSub(path, subFixture("symbol-callers", "rg", "snipe refs <sym>"), now); err != nil {
		t.Fatal(err)
	}
	subs, err := LoadSubs(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 3 {
		t.Fatalf("got %d rules, want 3 (all distinct on intent or better)", len(subs))
	}
}

// TestSubKeyEscaping: a '|' inside a field cannot forge the key separator, so
// two fields that would naively concat to the same string stay distinct rules.
func TestSubKeyEscaping(t *testing.T) {
	a := Substitution{IntentClass: "a|b", Better: "c"}
	b := Substitution{IntentClass: "a", Better: "b|c"}
	if a.subKey() == b.subKey() {
		t.Errorf("subKey collision: %q == %q", a.subKey(), b.subKey())
	}
}

// TestLoadSubsMissingFile: no ledger yet reads as empty, not an error.
func TestLoadSubsMissingFile(t *testing.T) {
	subs, err := LoadSubs(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(subs) != 0 {
		t.Errorf("got %d subs, want 0", len(subs))
	}
}

func mustExample(s Substitution, example, session string) Substitution {
	s.Example = example
	s.Session = session
	return s
}
