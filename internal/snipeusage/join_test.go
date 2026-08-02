package snipeusage

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestMatchExactHit(t *testing.T) {
	idx := NewIndex([]Record{
		{TS: "2026-08-01T20:52:01-04:00", Command: "sym", SessionKey: "sess-1", Rung: "exact"},
	})
	rec, ok := idx.Match("sess-1", "snipe_sym", mustTime(t, "2026-08-01T20:52:03-04:00"))
	if !ok {
		t.Fatal("want match")
	}
	if rec.Rung != "exact" {
		t.Errorf("Rung = %q, want %q", rec.Rung, "exact")
	}
	if idx.Matched != 1 || idx.Attempted != 1 {
		t.Errorf("Matched=%d Attempted=%d, want 1,1", idx.Matched, idx.Attempted)
	}
}

func TestMatchSessionMismatch(t *testing.T) {
	idx := NewIndex([]Record{
		{TS: "2026-08-01T20:52:01-04:00", Command: "sym", SessionKey: "sess-1"},
	})
	_, ok := idx.Match("sess-OTHER", "snipe_sym", mustTime(t, "2026-08-01T20:52:03-04:00"))
	if ok {
		t.Error("want miss on session mismatch")
	}
	if idx.Attempted != 1 || idx.Matched != 0 {
		t.Errorf("Matched=%d Attempted=%d, want 0,1", idx.Matched, idx.Attempted)
	}
}

func TestMatchActionMismatch(t *testing.T) {
	idx := NewIndex([]Record{
		{TS: "2026-08-01T20:52:01-04:00", Command: "sym", SessionKey: "sess-1"},
	})
	_, ok := idx.Match("sess-1", "snipe_def", mustTime(t, "2026-08-01T20:52:03-04:00"))
	if ok {
		t.Error("want miss on action mismatch")
	}
}

func TestMatchWindowExceeded(t *testing.T) {
	idx := NewIndex([]Record{
		{TS: "2026-08-01T20:52:01-04:00", Command: "sym", SessionKey: "sess-1"},
	})
	// 30s past the 10s window.
	_, ok := idx.Match("sess-1", "snipe_sym", mustTime(t, "2026-08-01T20:52:31-04:00"))
	if ok {
		t.Error("want miss when candidate is outside the join window")
	}
}

func TestMatchWindowExceededFuture(t *testing.T) {
	idx := NewIndex([]Record{
		{TS: "2026-08-01T20:52:31-04:00", Command: "sym", SessionKey: "sess-1"},
	})
	// `at` is 30s BEFORE the record's TS — outside the window on the other side.
	_, ok := idx.Match("sess-1", "snipe_sym", mustTime(t, "2026-08-01T20:52:01-04:00"))
	if ok {
		t.Error("want miss when candidate is outside the join window (future side)")
	}
}

// TestMatchRepeatedCallsOrderedPairing pins the ambiguous case from the plan:
// two identical repeated calls in one compound Bash chain share one
// tool_result timestamp. Both usage rows are still individually timestamped
// close together (in emit order), so two Match calls at the SAME `at` must
// pair in that same order rather than both grabbing the same row or
// swapping.
func TestMatchRepeatedCallsOrderedPairing(t *testing.T) {
	idx := NewIndex([]Record{
		{TS: "2026-08-01T20:52:00-04:00", Command: "sym", SessionKey: "sess-1", Arg: "Foo"},
		{TS: "2026-08-01T20:52:01-04:00", Command: "sym", SessionKey: "sess-1", Arg: "Bar"},
	})
	at := mustTime(t, "2026-08-01T20:52:02-04:00")

	first, ok := idx.Match("sess-1", "snipe_sym", at)
	if !ok || first.Arg != "Foo" {
		t.Fatalf("first match = %+v, ok=%v, want Arg=Foo", first, ok)
	}
	second, ok := idx.Match("sess-1", "snipe_sym", at)
	if !ok || second.Arg != "Bar" {
		t.Fatalf("second match = %+v, ok=%v, want Arg=Bar", second, ok)
	}
	// A third call at the same `at` finds nothing left to consume.
	if _, ok := idx.Match("sess-1", "snipe_sym", at); ok {
		t.Error("third match should miss — group exhausted")
	}
}

// TestMatchDroppedRowDoesNotCascade pins the plan's dropped-row tolerance:
// call 2's usage row never landed (e.g. SNIPE_NO_TELEMETRY skipped it), but
// call 1 and call 3's rows did. Call 2's Match must miss without disturbing
// call 3's later correct match.
func TestMatchDroppedRowDoesNotCascade(t *testing.T) {
	idx := NewIndex([]Record{
		{TS: "2026-08-01T20:52:00-04:00", Command: "sym", SessionKey: "sess-1", Arg: "call1"},
		// call2's row is missing entirely.
		{TS: "2026-08-01T20:53:00-04:00", Command: "sym", SessionKey: "sess-1", Arg: "call3"},
	})

	call1, ok := idx.Match("sess-1", "snipe_sym", mustTime(t, "2026-08-01T20:52:01-04:00"))
	if !ok || call1.Arg != "call1" {
		t.Fatalf("call1 match = %+v, ok=%v", call1, ok)
	}

	// call2 completes ~30s after call1's row and >10s away from call3's row —
	// outside the window on both sides, so it must miss rather than
	// mis-pairing with call3's far-future row.
	if _, ok := idx.Match("sess-1", "snipe_sym", mustTime(t, "2026-08-01T20:52:31-04:00")); ok {
		t.Fatal("call2 should miss (its row was dropped)")
	}

	call3, ok := idx.Match("sess-1", "snipe_sym", mustTime(t, "2026-08-01T20:53:01-04:00"))
	if !ok || call3.Arg != "call3" {
		t.Fatalf("call3 match = %+v, ok=%v, want Arg=call3 (not cascade-misaligned by the dropped call2 row)", call3, ok)
	}
}

func TestMatchUnknownGroupCounts(t *testing.T) {
	idx := NewIndex(nil)
	if _, ok := idx.Match("sess-x", "snipe_sym", time.Now()); ok {
		t.Error("want miss on empty index")
	}
	if idx.Attempted != 1 {
		t.Errorf("Attempted = %d, want 1 (even on an unknown group)", idx.Attempted)
	}
}
