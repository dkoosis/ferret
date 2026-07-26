package feedback

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var day1 = time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

// TestReserveGrantsUntilSessionCap: a session may spend up to SessionCap asks,
// each on a distinct target; the next is refused even though the day still has
// budget. Refusal is not an error.
func TestReserveGrantsUntilSessionCap(t *testing.T) {
	path := BudgetPath(t.TempDir())
	for i := range SessionCap {
		ok, err := Reserve(path, "s1", targetN(i), day1)
		if err != nil || !ok {
			t.Fatalf("grant %d: ok=%v err=%v", i, ok, err)
		}
	}
	ok, err := Reserve(path, "s1", "t-extra", day1)
	if err != nil {
		t.Fatalf("over-cap Reserve errored: %v", err)
	}
	if ok {
		t.Error("session over SessionCap must be refused")
	}
}

// TestReserveLatchesTarget: the same target is never asked twice — even under
// cap, even from a different session. A re-surfaced moment must not re-nag.
func TestReserveLatchesTarget(t *testing.T) {
	path := BudgetPath(t.TempDir())
	if ok, _ := Reserve(path, "s1", "t1", day1); !ok {
		t.Fatal("first ask on t1 must be granted")
	}
	if ok, _ := Reserve(path, "s2", "t1", day1); ok {
		t.Error("same target from another session must be latched off")
	}
}

// TestReserveEnforcesDailyCapAcrossSessions: the shared daily cap binds the sum
// over sessions, not each session alone — the whole reason it's shared state.
func TestReserveEnforcesDailyCapAcrossSessions(t *testing.T) {
	path := BudgetPath(t.TempDir())
	granted := 0
	for i := range DailyCap + 5 {
		// A fresh session each time so SessionCap never binds first — only the
		// daily cap can stop the grants.
		if ok, _ := Reserve(path, sessionN(i), targetN(i), day1); ok {
			granted++
		}
	}
	if granted != DailyCap {
		t.Errorf("granted %d asks across sessions, want DailyCap=%d", granted, DailyCap)
	}
}

// TestReserveResetsOnDateRoll: counts reset when the day changes, so yesterday's
// spend never starves today.
func TestReserveResetsOnDateRoll(t *testing.T) {
	path := BudgetPath(t.TempDir())
	for i := range DailyCap {
		if _, err := Reserve(path, sessionN(i), targetN(i), day1); err != nil {
			t.Fatalf("seed grant %d: %v", i, err)
		}
	}
	if ok, _ := Reserve(path, "s-new", "t-new", day1); ok {
		t.Fatal("day1 should be exhausted")
	}
	day2 := day1.Add(24 * time.Hour)
	if ok, err := Reserve(path, "s-new", "t-new", day2); err != nil || !ok {
		t.Errorf("a new day must reset the budget: ok=%v err=%v", ok, err)
	}
}

// TestReserveMissingFileGrants: the very first ask of the tap's life (no state
// file yet) must be granted, not blocked.
func TestReserveMissingFileGrants(t *testing.T) {
	path := BudgetPath(t.TempDir())
	if ok, err := Reserve(path, "s1", "t1", day1); err != nil || !ok {
		t.Errorf("first-ever Reserve: ok=%v err=%v", ok, err)
	}
}

// TestReserveSyncsParentDir: the atomic publish fsyncs the parent dir so a spend
// survives a crash — otherwise a crashed session could re-grant an already-spent
// ask on restart.
func TestReserveSyncsParentDir(t *testing.T) {
	dir := t.TempDir()
	path := BudgetPath(dir)
	var dirs []string
	orig := syncDir
	syncDir = func(d string) error { dirs = append(dirs, d); return orig(d) }
	t.Cleanup(func() { syncDir = orig })

	if ok, err := Reserve(path, "s1", "t1", day1); err != nil || !ok {
		t.Fatalf("Reserve: ok=%v err=%v", ok, err)
	}
	if !slices.Contains(dirs, dir) {
		t.Errorf("expected parent dir %q fsync'd, got %v", dir, dirs)
	}
}

// TestReserveConcurrentNeverOverGrants: many sessions racing to reserve must
// never grant past DailyCap — the flock has to serialize the read-check-write or
// two sessions each read "under cap" and both grant. This is the race the shared
// lock exists to close.
func TestReserveConcurrentNeverOverGrants(t *testing.T) {
	path := BudgetPath(t.TempDir())
	var granted atomic.Int64
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if ok, err := Reserve(path, sessionN(i), targetN(i), day1); err == nil && ok {
				granted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if g := granted.Load(); g != DailyCap {
		t.Errorf("concurrent grants = %d, want exactly DailyCap=%d (over-grant = flock race)", g, DailyCap)
	}
}

func targetN(i int) string  { return "t" + string(rune('A'+i%26)) + string(rune('0'+i/26)) }
func sessionN(i int) string { return "s" + string(rune('A'+i%26)) + string(rune('0'+i/26)) }
