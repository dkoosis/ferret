package mine

import (
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// substEv builds a shell event carrying the raw (truncated) command text
// MineSubstitutions reads via shellnorm.Argv — mirrors pollingEv's shape,
// with Pipe/Compound as extra optional flags since escape-hatch cases need
// them set.
func substEv(session, action, detail string, pipe, compound bool) event.Event {
	return event.Event{
		Session: session, Kind: event.KindShell, Action: action, Detail: detail,
		Pipe: pipe, Compound: compound,
	}
}

// TestMineSubstitutions_Hits covers the verb→native-tool table (step 5): one
// case per supported verb, each a plain call the detector recognizes as
// substitutable with no judge call — a pure table lookup + shellnorm.Argv scan.
func TestMineSubstitutions_Hits(t *testing.T) {
	cases := []struct {
		name   string
		action string
		detail string
		tool   string
	}{
		{"rg bare pattern", "rg", "rg foo", "Grep"},
		{"grep with case-insensitive flag", "grep", "grep -i foo f.go", "Grep"},
		{"ls bare path", "ls", "ls src/", "Glob"},
		{"find -name", "find", "find . -name '*.go'", "Glob"},
		{"cat single file", "cat", "cat main.go", "Read"},
		{"head -n", "head", "head -n 20 f", "Read"},
		{"tail -n", "tail", "tail -n 5 f", "Read"},
		{"sed print range", "sed", "sed -n '10,20p' f", "Read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := MineSubstitutions([]event.Event{substEv("s1", c.action, c.detail, false, false)})
			if len(rep.Rows) != 1 {
				t.Fatalf("rows = %d, want 1 (excluded=%v)", len(rep.Rows), rep.Excluded)
			}
			row := rep.Rows[0]
			if row.Key != "sh:"+c.action {
				t.Errorf("Key = %q, want %q", row.Key, "sh:"+c.action)
			}
			if row.Tool != c.tool {
				t.Errorf("Tool = %q, want %q", row.Tool, c.tool)
			}
			if row.Calls != 1 || row.Sessions != 1 {
				t.Errorf("Calls/Sessions = %d/%d, want 1/1", row.Calls, row.Sessions)
			}
			if row.Exemplar != c.detail {
				t.Errorf("Exemplar = %q, want %q", row.Exemplar, c.detail)
			}
			if len(rep.Excluded) != 0 {
				t.Errorf("Excluded = %v, want empty", rep.Excluded)
			}
		})
	}
}

// TestMineSubstitutions_Hatches covers every escape-hatch class (step 6):
// each must produce zero rows and increment the matching Excluded tally —
// the reasons a call is a floor candidate, never a guess.
func TestMineSubstitutions_Hatches(t *testing.T) {
	cases := []struct {
		name   string
		ev     event.Event
		reason string
	}{
		{"pipe", substEv("s1", "rg", "rg foo | head", true, false), "pipe"},
		{"compound", substEv("s1", "cat", "cat f.go && echo done", false, true), "compound"},
		{"unsupported flag: ls -la", substEv("s1", "ls", "ls -la", false, false), "unsupported_flag"},
		{"unsupported flag: grep -c", substEv("s1", "grep", "grep -c foo f.go", false, false), "unsupported_flag"},
		{"unsupported flag: tail -f", substEv("s1", "tail", "tail -f f", false, false), "unsupported_flag"},
		{"head -n -5 is all-but-last-5", substEv("s1", "head", "head -n -5 f", false, false), "unsupported_flag"},
		{"tail -n +5 starts at line 5", substEv("s1", "tail", "tail -n +5 f", false, false), "unsupported_flag"},
		{"repeated find -name ANDs two globs", substEv("s1", "find", "find . -name a.go -name b.go", false, false), "unsupported_flag"},
		{"env assignment prefix", substEv("s1", "rg", "RG_CONFIG=x rg foo", false, false), "unparseable"},
		{"redirect", substEv("s1", "rg", "rg foo > out.txt", false, false), "redirect"},
		{"expansion", substEv("s1", "cat", "cat $F", false, false), "expansion"},
		{"arity", substEv("s1", "cat", "cat a b", false, false), "arity"},
		{"unparseable", substEv("s1", "cat", "cat 'unterminated", false, false), "unparseable"},
		{"truncated", substEv("s1", "rg", "rg "+string(make([]byte, 160)), false, false), "truncated"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := MineSubstitutions([]event.Event{c.ev})
			if len(rep.Rows) != 0 {
				t.Errorf("rows = %d, want 0: %+v", len(rep.Rows), rep.Rows)
			}
			if rep.Excluded[c.reason] != 1 {
				t.Errorf("Excluded[%q] = %d, want 1; full tally=%v", c.reason, rep.Excluded[c.reason], rep.Excluded)
			}
		})
	}
}

// TestMineSubstitutions_ExcludedTallyAccumulates checks the Excluded map sums
// across multiple events sharing a reason, rather than being overwritten.
func TestMineSubstitutions_ExcludedTallyAccumulates(t *testing.T) {
	events := []event.Event{
		substEv("s1", "rg", "rg foo | head", true, false),
		substEv("s2", "cat", "cat a.go | head", true, false),
	}
	rep := MineSubstitutions(events)
	if rep.Excluded["pipe"] != 2 {
		t.Errorf("Excluded[pipe] = %d, want 2", rep.Excluded["pipe"])
	}
}

// TestMineSubstitutions_Ranking covers step 7: Score = Calls × Sessions, a
// habit spread across sessions outranks single-session volume, and ties break
// deterministically on Key.
func TestMineSubstitutions_Ranking(t *testing.T) {
	events := []event.Event{
		// rg: 3 calls, all in one session — volume, no spread.
		substEv("s1", "rg", "rg foo", false, false),
		substEv("s1", "rg", "rg bar", false, false),
		substEv("s1", "rg", "rg baz", false, false),
		// grep: 2 calls, spread across 2 sessions — fewer calls, more spread.
		substEv("s2", "grep", "grep -i foo f.go", false, false),
		substEv("s3", "grep", "grep -i bar f.go", false, false),
	}
	rep := MineSubstitutions(events)
	if len(rep.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rep.Rows))
	}
	if rep.Rows[0].Key != "sh:grep" {
		t.Errorf("top row = %q (score=%.0f), want sh:grep (score 4 > rg's score 3) — spread beats volume",
			rep.Rows[0].Key, rep.Rows[0].Score)
	}
	if rep.Rows[0].Score != 4 || rep.Rows[1].Score != 3 {
		t.Errorf("scores = %.0f, %.0f, want 4, 3", rep.Rows[0].Score, rep.Rows[1].Score)
	}
}

// TestMineSubstitutions_RankingTieBreak pins byte-stable ordering when Score,
// Calls, and Sessions are all equal — the tie breaks on Key ascending.
func TestMineSubstitutions_RankingTieBreak(t *testing.T) {
	events := []event.Event{
		substEv("s1", "head", "head -n 1 f", false, false),
		substEv("s1", "cat", "cat f.go", false, false),
	}
	for range [5]int{} { // determinism must hold across repeated runs, not just once
		rep := MineSubstitutions(events)
		if len(rep.Rows) != 2 {
			t.Fatalf("rows = %d, want 2", len(rep.Rows))
		}
		if rep.Rows[0].Key != "sh:cat" || rep.Rows[1].Key != "sh:head" {
			t.Fatalf("order = %q, %q, want sh:cat, sh:head (Key ascending tie-break)",
				rep.Rows[0].Key, rep.Rows[1].Key)
		}
	}
}

// TestMineSubstitutions_SessionsDenominator mirrors polling's report-level
// Sessions field: distinct sessions containing >=1 shell event with Detail,
// independent of whether that event was a substitution candidate.
func TestMineSubstitutions_SessionsDenominator(t *testing.T) {
	events := []event.Event{
		substEv("s1", "rg", "rg foo", false, false),
		substEv("s2", "go_test", "go test ./...", false, false), // not a candidate verb
	}
	rep := MineSubstitutions(events)
	if rep.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2", rep.Sessions)
	}
}
