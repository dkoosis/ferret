package mine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeParallelFixture writes raw event JSONL to a temp events.jsonl — same
// inline-fixture style as burn_test.go.
func writeParallelFixture(t *testing.T, lines string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// levelOf returns the row for concurrency level n, failing when it is absent.
func levelOf(t *testing.T, p *ConcurrencyProfile, n int) LevelRow {
	t.Helper()
	for i := range p.Levels {
		if p.Levels[i].N == n {
			return p.Levels[i]
		}
	}
	t.Fatalf("no level N=%d in profile %+v", n, p.Levels)
	return LevelRow{}
}

// TestParallel_ChargesBytesToWindowLevel_When_SessionsOverlap pins the core
// attribution: two sessions overlapping in wall-clock put their bytes at N=2,
// the solo stretches at N=1.
//
// Fixture timeline (minutes past 10:00):
//
//	s1  00 ── 10        200 B at 00, 200 B at 10
//	s2       05 ── 15   600 B at 05, 0 B at 15
//
// s1's 10:10 event and s2's 10:05 event both fall inside the overlap, so
// 200+600 = 800 of 1000 bytes are spent two-up: 80%.
func TestParallel_ChargesBytesToWindowLevel_When_SessionsOverlap(t *testing.T) {
	lines := `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","t":"2026-08-14T10:00:00Z","b":200}
{"i":2,"p":"proj","s":"s1","k":"tool","act":"Read","t":"2026-08-14T10:10:00Z","b":200}
{"i":3,"p":"proj","s":"s2","k":"tool","act":"Read","t":"2026-08-14T10:05:00Z","b":600}
{"i":4,"p":"proj","s":"s2","k":"tool","act":"Read","t":"2026-08-14T10:15:00Z","b":0}
`
	res, err := Parallel(writeParallelFixture(t, lines), ParallelOptions{IdleGap: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	if res.Sessions != 2 || res.Events != 4 {
		t.Fatalf("sessions=%d events=%d, want 2/4", res.Sessions, res.Events)
	}
	if res.Window.Max != 2 {
		t.Errorf("Window.Max = %d, want 2 (s1 and s2 overlap 10:05-10:10)", res.Window.Max)
	}
	two := levelOf(t, &res.Window, 2)
	if two.Bytes != 800 {
		t.Errorf("N=2 bytes = %d, want 800 (both in-overlap events)", two.Bytes)
	}
	if two.BytesPct != 80 {
		t.Errorf("N=2 bytesPct = %.1f, want 80", two.BytesPct)
	}
	if got := levelOf(t, &res.Window, 1).Bytes; got != 200 {
		t.Errorf("N=1 bytes = %d, want 200 (s1's solo 10:00 event)", got)
	}
	// Overlap is 10:05→10:10 = 5 min; solo stretches are 10:00→10:05 and
	// 10:10→10:15 = 10 min. Hours are wall-clock, not session-hours.
	if got := two.Hours; got < 0.083 || got > 0.084 {
		t.Errorf("N=2 hours = %.4f, want ~0.0833 (5 minutes)", got)
	}
	if got := levelOf(t, &res.Window, 1).Hours; got < 0.166 || got > 0.167 {
		t.Errorf("N=1 hours = %.4f, want ~0.1667 (10 minutes)", got)
	}
}

// TestParallel_SeparatesFanoutFromWindow_When_OneSessionRunsSubagents is the
// split dk required: one session with two subagents must read as fan-out
// concurrency 3 (parent + 2) and window concurrency 1 — nothing overlaps in
// wall-clock because there is only one session. Keying on session_id alone
// would report no concurrency at all here, which is the trap this pins.
func TestParallel_SeparatesFanoutFromWindow_When_OneSessionRunsSubagents(t *testing.T) {
	lines := `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Task","t":"2026-08-14T10:00:00Z","b":100}
{"i":2,"p":"proj","s":"s1","a":"ag1","k":"tool","act":"Read","t":"2026-08-14T10:01:00Z","b":300}
{"i":3,"p":"proj","s":"s1","a":"ag2","k":"tool","act":"Read","t":"2026-08-14T10:01:30Z","b":500}
{"i":4,"p":"proj","s":"s1","a":"ag1","k":"tool","act":"Read","t":"2026-08-14T10:02:00Z","b":100}
{"i":5,"p":"proj","s":"s1","k":"tool","act":"Read","t":"2026-08-14T10:03:00Z","b":100}
`
	res, err := Parallel(writeParallelFixture(t, lines), ParallelOptions{IdleGap: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	if res.Window.Max != 1 {
		t.Errorf("Window.Max = %d, want 1 — one session cannot overlap itself", res.Window.Max)
	}
	if res.Fanout.Max != 3 {
		t.Errorf("Fanout.Max = %d, want 3 (main + ag1 + ag2 live at 10:01:30)", res.Fanout.Max)
	}
	// Byte arithmetic, spelled out (1100 B total): the parent's 10:00 and 10:03
	// events sit outside both subagent spans (N=1, 200 B); ag1's two events run
	// beside the parent (N=2, 400 B); ag2's single event lands while ag1 is also
	// live (N=3, 500 B). 900 of 1100 = 81.8% of the spend happened fanned out.
	if got := levelOf(t, &res.Fanout, 1).Bytes; got != 200 {
		t.Errorf("fan-out N=1 bytes = %d, want 200 (the parent's two solo events)", got)
	}
	if got := levelOf(t, &res.Fanout, 3).Bytes; got != 500 {
		t.Errorf("fan-out N=3 bytes = %d, want 500 (ag2's event, three threads live)", got)
	}
	if got := levelOf(t, &res.Fanout, 2).BytesPctAtOrAbove; got < 81.8 || got > 81.9 {
		t.Errorf("fan-out %%bytes@2+ = %.1f, want ~81.8 (900 of 1100 spent fanned out)", got)
	}
}

// TestParallel_SplitsOnIdleGap_When_SessionGoesQuiet pins the modeling choice
// that keeps the numbers honest: a session's first-to-last span is NOT its
// running window. s1 works at 10:00, sleeps an hour, works again at 11:00;
// s2 runs at 10:30. With a 5-minute gap they never overlap, and s1's idle hour
// is not counted as active time.
func TestParallel_SplitsOnIdleGap_When_SessionGoesQuiet(t *testing.T) {
	lines := `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","t":"2026-08-14T10:00:00Z","b":10}
{"i":2,"p":"proj","s":"s1","k":"tool","act":"Read","t":"2026-08-14T11:00:00Z","b":10}
{"i":3,"p":"proj","s":"s2","k":"tool","act":"Read","t":"2026-08-14T10:30:00Z","b":10}
`
	path := writeParallelFixture(t, lines)

	gapped, err := Parallel(path, ParallelOptions{IdleGap: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if gapped.Window.Max != 1 {
		t.Errorf("Window.Max = %d with a 5m gap, want 1 — s1 was idle when s2 ran", gapped.Window.Max)
	}
	if gapped.ActiveHours != 0 {
		t.Errorf("ActiveHours = %f, want 0 — three isolated instants have no duration", gapped.ActiveHours)
	}

	// Widen the gap past the idle hour and the same corpus reads as overlap:
	// the threshold is doing the work, not an accident of the fixture.
	merged, err := Parallel(path, ParallelOptions{IdleGap: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Window.Max != 2 {
		t.Errorf("Window.Max = %d with a 2h gap, want 2 — s1's block now spans s2's run", merged.Window.Max)
	}
}

// TestParallel_ExcludesUntimedEvents_When_TimestampMissing pins the honest
// denominator: Event.Time is advisory, and an untimed event cannot be placed on
// the timeline. It is counted and excluded, never guessed onto a level.
func TestParallel_ExcludesUntimedEvents_When_TimestampMissing(t *testing.T) {
	lines := `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","t":"2026-08-14T10:00:00Z","b":100}
{"i":2,"p":"proj","s":"s1","k":"tool","act":"Read","b":900}
`
	res, err := Parallel(writeParallelFixture(t, lines), ParallelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 1 || res.Untimed != 1 {
		t.Errorf("events=%d untimed=%d, want 1/1", res.Events, res.Untimed)
	}
	if res.Bytes != 100 {
		t.Errorf("Bytes = %d, want 100 — the untimed event's bytes stay out of the denominator", res.Bytes)
	}
}

// TestParallel_ReturnsEmptyProfiles_When_CorpusHasNoTimedEvents pins the empty
// case: no panic, no fabricated level, zero everything.
func TestParallel_ReturnsEmptyProfiles_When_CorpusHasNoTimedEvents(t *testing.T) {
	res, err := Parallel(writeParallelFixture(t, ""), ParallelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 0 || len(res.Window.Levels) != 0 || len(res.Fanout.Levels) != 0 {
		t.Errorf("empty corpus produced %+v", res)
	}
	if res.IdleGapMin != DefaultIdleGap.Minutes() {
		t.Errorf("IdleGapMin = %f, want the default %f", res.IdleGapMin, DefaultIdleGap.Minutes())
	}
}

// TestParallel_WeightsMeansByTimeAndBytes_When_CostConcentratesInOverlap pins
// the two means diverging — the finding the report exists to surface. The
// overlap is short but carries almost all the bytes, so the byte-weighted mean
// sits far above the time-weighted one.
func TestParallel_WeightsMeansByTimeAndBytes_When_CostConcentratesInOverlap(t *testing.T) {
	lines := `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","t":"2026-08-14T10:00:00Z","b":1}
{"i":2,"p":"proj","s":"s1","k":"tool","act":"Read","t":"2026-08-14T10:20:00Z","b":1}
{"i":3,"p":"proj","s":"s2","k":"tool","act":"Read","t":"2026-08-14T10:19:00Z","b":998}
`
	res, err := Parallel(writeParallelFixture(t, lines), ParallelOptions{IdleGap: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if res.Window.ByteWeightedMean <= res.Window.TimeWeightedMean {
		t.Errorf("byte-weighted mean %.3f should exceed time-weighted %.3f — the overlap is short and expensive",
			res.Window.ByteWeightedMean, res.Window.TimeWeightedMean)
	}
	if got := levelOf(t, &res.Window, 2).BytesPctAtOrAbove; got < 99.8 {
		t.Errorf("%%bytes@2+ = %.2f, want ~99.9", got)
	}
}
