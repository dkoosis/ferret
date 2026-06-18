package fixes

import (
	"os"
	"path/filepath"
	"strings"
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

// TestLoadCorruptMidLineErrors: a malformed line with valid entries AFTER it is
// genuine mid-ledger corruption — a hard error, not a silent skip. A corrupt
// ledger that joined as "no fixes" would erase the loop's memory without warning.
func TestLoadCorruptMidLineErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixes.jsonl")
	body := `{"motif":"a","fix":"x","addedAt":"2026-06-12T09:00:00Z","baselineBurn":1}
{not json}
{"motif":"b","fix":"y","addedAt":"2026-06-12T10:00:00Z","baselineBurn":2}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("corrupt mid-ledger line must surface an error, not parse as partial")
	}
}

// TestLoadSalvagesCorruptTrailingLine: a single corrupt TRAILING line (a torn
// append) is dropped with a stderr warning while every prior entry still loads —
// one bad append must not brick all future ledger reads (ferret-020).
func TestLoadSalvagesCorruptTrailingLine(t *testing.T) {
	var buf strings.Builder
	old := stderr
	stderr = &buf
	t.Cleanup(func() { stderr = old })

	path := filepath.Join(t.TempDir(), "fixes.jsonl")
	body := `{"motif":"a","fix":"x","addedAt":"2026-06-12T09:00:00Z","baselineBurn":1}
{"motif":"b","fix":"y","addedAt":` // truncated mid-record, no newline
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load salvageable ledger: %v", err)
	}
	if len(got) != 1 || got[0].Motif != "a" {
		t.Errorf("expected the one prior entry to survive, got %+v", got)
	}
	if !strings.Contains(buf.String(), "corrupt trailing ledger line dropped") {
		t.Errorf("expected a salvage warning on stderr, got %q", buf.String())
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

// TestMotifKeyCollisionFree guards ferret-0vz/s3z: a bare comma-join made
// ["a,b","c"] and ["a","b,c"] both render "a,b,c", so the ledger's last-wins
// index silently overwrote one fix with the other. Escaping commas within tokens
// makes the mapping injective.
func TestMotifKeyCollisionFree(t *testing.T) {
	if a, b := MotifKey([]string{"a,b", "c"}), MotifKey([]string{"a", "b,c"}); a == b {
		t.Errorf("MotifKey collided: both = %q for distinct token sequences", a)
	}
}

// TestParseMotifRoundTrip guards ferret-s3z path (a): a spaced --motif must
// produce the SAME key the report computes from the corpus tokens. The writer
// runs ParseMotif then MotifKey; the reader runs MotifKey over the corpus tokens
// — both must land on the identical key or the join silently misses.
func TestParseMotifRoundTrip(t *testing.T) {
	writeKey := MotifKey(ParseMotif("Edit!, Read")) // user typed a space after the comma
	readKey := MotifKey([]string{"Edit!", "Read"})  // report builds this from the corpus
	if writeKey != readKey {
		t.Errorf("spaced --motif key %q != report key %q — the join would miss", writeKey, readKey)
	}
	if readKey != "Edit!,Read" {
		t.Errorf("canonical key = %q, want %q", readKey, "Edit!,Read")
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

// TestDispBackCompat: a row with no disposition (every ledger row written before
// the field existed) must read as a fix, preserving the original baseline→delta
// behavior. wontfix/watch read back verbatim.
func TestDispBackCompat(t *testing.T) {
	cases := []struct {
		name string
		e    Entry
		want string
	}{
		{"empty reads as fix", Entry{}, DispositionFix},
		{"explicit fix", Entry{Disposition: DispositionFix}, DispositionFix},
		{"wontfix", Entry{Disposition: DispositionWontfix}, DispositionWontfix},
		{"watch", Entry{Disposition: DispositionWatch}, DispositionWatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.e.Disp(); got != tc.want {
				t.Errorf("Disp() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSuppressed: only the non-fix verdicts (wontfix/watch) suppress a motif
// from the actionable report; a fix (incl. the empty back-compat default) does
// not.
func TestSuppressed(t *testing.T) {
	cases := []struct {
		name string
		e    Entry
		want bool
	}{
		{"empty (default fix)", Entry{}, false},
		{"fix", Entry{Disposition: DispositionFix}, false},
		{"wontfix", Entry{Disposition: DispositionWontfix}, true},
		{"watch", Entry{Disposition: DispositionWatch}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.e.Suppressed(); got != tc.want {
				t.Errorf("Suppressed() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestValidDisposition: the empty string is valid (normalizes to fix); the three
// named verdicts are valid; anything else is rejected so the writer can fail
// fast on a typo.
func TestValidDisposition(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{DispositionFix, true},
		{DispositionWontfix, true},
		{DispositionWatch, true},
		{"bogus", false},
		{"FIX", false},
	}
	for _, tc := range cases {
		if got := ValidDisposition(tc.in); got != tc.want {
			t.Errorf("ValidDisposition(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestDispositionRoundTrip: a non-fix verdict written through Append must come
// back from Load intact, and a row whose JSON omits the field must Load as a fix
// — the on-disk back-compat contract, not just the in-memory Disp() default.
func TestDispositionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixes.jsonl")
	at := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	want := []Entry{
		{Motif: "Edit!,Read", Fix: "hook can't reclaim spent payload", Note: "behavioral-only", AddedAt: at, Disposition: DispositionWontfix},
		{Motif: "Grep,Read", Fix: "re-rank after ferret-07s", AddedAt: at.Add(time.Hour), Disposition: DispositionWatch},
		{Motif: "Write!,Read", Fix: "skill: write-once", AddedAt: at.Add(2 * time.Hour), BaselineBurn: 41000, Disposition: DispositionFix},
	}
	for _, e := range want {
		if err := Append(path, e); err != nil {
			t.Fatalf("Append(%q): %v", e.Motif, err)
		}
	}
	// A legacy row whose JSON has no disposition field at all.
	legacy := `{"motif":"Bash!,Bash","fix":"old","addedAt":"2026-06-01T00:00:00Z","baselineBurn":7000}` + "\n"
	if err := appendRaw(path, legacy); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("Load returned %d entries, want 4", len(got))
	}
	for i := range want {
		if got[i].Disp() != want[i].Disp() {
			t.Errorf("entry %d disposition = %q, want %q", i, got[i].Disp(), want[i].Disp())
		}
	}
	if d := got[3].Disp(); d != DispositionFix {
		t.Errorf("legacy row (no disposition field) Disp() = %q, want %q", d, DispositionFix)
	}
	if got[3].Suppressed() {
		t.Error("legacy row must not be suppressed — it reads as a fix")
	}
}

func appendRaw(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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
