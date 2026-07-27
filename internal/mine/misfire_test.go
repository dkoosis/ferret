package mine

import (
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

func ev(session, kind, action, target, status, detail string) event.Event {
	return event.Event{
		Session: session, Kind: kind, Action: action, Target: target,
		Status: status, Detail: detail,
	}
}

func TestMineMisfires_RanksByFailCountTimesSessionSpread_When_MultipleKeysFail(t *testing.T) {
	events := []event.Event{
		// "jq" fails in 3 distinct sessions (score 3*3=9) — the heaviest misfire.
		ev("s1", event.KindShell, "jq", "", event.StatusFail, "jq '.[0]'"),
		ev("s2", event.KindShell, "jq", "", event.StatusFail, "jq '.[0]'"),
		ev("s3", event.KindShell, "jq", "", event.StatusFail, "jq '.[0]'"),
		// "rg" fails twice but only in one session (score 2*1=2).
		ev("s1", event.KindShell, "rg", "", event.StatusFail, "rg foo"),
		ev("s1", event.KindShell, "rg", "", event.StatusFail, "rg foo"),
	}
	rep := MineMisfires(events)
	if len(rep.Rows) != 2 {
		t.Fatalf("rows = %d, want 2; got %+v", len(rep.Rows), rep.Rows)
	}
	if rep.Rows[0].Key != "jq" {
		t.Errorf("top row = %q, want jq (score should rank fails×sessions)", rep.Rows[0].Key)
	}
	if rep.Rows[0].Score != 9 {
		t.Errorf("jq score = %v, want 9", rep.Rows[0].Score)
	}
	if rep.Rows[1].Key != "rg" || rep.Rows[1].Score != 2 {
		t.Errorf("second row = %+v, want rg score=2", rep.Rows[1])
	}
}

func TestMineMisfires_ComputesFailRate_When_KeyHasMixedOutcomes(t *testing.T) {
	events := []event.Event{
		ev("s1", event.KindShell, "bd_show", "", event.StatusFail, "bd show x | jq '.[0]'"),
		ev("s1", event.KindShell, "bd_show", "", event.StatusOK, "bd show x --short"),
		ev("s2", event.KindShell, "bd_show", "", event.StatusOK, "bd show y --short"),
		ev("s2", event.KindShell, "bd_show", "", event.StatusOK, "bd show z --short"),
	}
	rep := MineMisfires(events)
	if len(rep.Rows) != 1 {
		t.Fatalf("rows = %d, want 1; got %+v", len(rep.Rows), rep.Rows)
	}
	row := rep.Rows[0]
	if row.Calls != 4 || row.Fails != 1 {
		t.Fatalf("calls/fails = %d/%d, want 4/1", row.Calls, row.Fails)
	}
	if got, want := row.FailRate, 0.25; got != want {
		t.Errorf("failRate = %v, want %v", got, want)
	}
}

func TestMineMisfires_ExcludesSuccessOnlyKeys_When_NoFailurePresent(t *testing.T) {
	events := []event.Event{
		ev("s1", event.KindShell, "git_status", "", event.StatusOK, "git status"),
		ev("s1", event.KindShell, "git_status", "", event.StatusOK, "git status"),
	}
	rep := MineMisfires(events)
	if len(rep.Rows) != 0 {
		t.Errorf("rows = %+v, want none — a key with zero failures is not a misfire", rep.Rows)
	}
}

func TestMineMisfires_ExcludesPromptEvents_When_CountingCalls(t *testing.T) {
	events := []event.Event{
		{Session: "s1", Kind: event.KindPrompt, Action: "", Status: ""},
		ev("s1", event.KindShell, "jq", "", event.StatusFail, "jq '.[0]'"),
	}
	rep := MineMisfires(events)
	if len(rep.Rows) != 1 || rep.Rows[0].Calls != 1 {
		t.Fatalf("prompt event leaked into the misfire table: %+v", rep.Rows)
	}
}

func TestMineMisfires_EmitsRepairPair_When_RetryFollowsFailureWithSameKey(t *testing.T) {
	failedEv := ev("s1", event.KindShell, "jq", "", event.StatusFail, "jq '.[0]' out.json")
	fixedEv := ev("s1", event.KindShell, "jq", "", event.StatusOK, "jq '.result[0]' out.json")
	fixedEv.Retry = true // set by internal/event/build.go's finish() in the real pipeline

	rep := MineMisfires([]event.Event{failedEv, fixedEv})
	if len(rep.Repairs) != 1 {
		t.Fatalf("repairs = %+v, want exactly one pair", rep.Repairs)
	}
	p := rep.Repairs[0]
	if p.Key != "jq" || p.FailedRaw != "jq '.[0]' out.json" || p.FixedRaw != "jq '.result[0]' out.json" {
		t.Errorf("pair = %+v, want the failed→fixed jq forms", p)
	}
	if p.Count != 1 {
		t.Errorf("count = %d, want 1", p.Count)
	}
}

func TestMineMisfires_AggregatesRepeatedRepairs_When_SameFormPairRecurs(t *testing.T) {
	mk := func(session string) (event.Event, event.Event) {
		f := ev(session, event.KindShell, "jq", "", event.StatusFail, "jq '.[0]'")
		o := ev(session, event.KindShell, "jq", "", event.StatusOK, "jq '.result[0]'")
		o.Retry = true
		return f, o
	}
	events := make([]event.Event, 0, 6)
	for _, s := range []string{"s1", "s2", "s3"} {
		f, o := mk(s)
		events = append(events, f, o)
	}
	rep := MineMisfires(events)
	if len(rep.Repairs) != 1 {
		t.Fatalf("repairs = %+v, want one aggregated pair across 3 sessions", rep.Repairs)
	}
	if rep.Repairs[0].Count != 3 {
		t.Errorf("count = %d, want 3", rep.Repairs[0].Count)
	}
}

func TestMineMisfires_FallsBackToKeyLevelPair_When_EventsCarryNoDetailText(t *testing.T) {
	// Tool events set no Detail (only shell events carry raw command text —
	// see internal/event/build.go fromToolUse). The repair pair still fires,
	// collapsed to key-level per the bead's v1 fallback allowance.
	failedEv := event.Event{Session: "s1", Kind: event.KindTool, Action: "mcp__trixi__get_nug", Target: "abc", Status: event.StatusFail}
	fixedEv := event.Event{Session: "s1", Kind: event.KindTool, Action: "mcp__trixi__get_nug", Target: "abc", Status: event.StatusOK, Retry: true}

	rep := MineMisfires([]event.Event{failedEv, fixedEv})
	if len(rep.Repairs) != 1 {
		t.Fatalf("repairs = %+v, want one key-level pair", rep.Repairs)
	}
	p := rep.Repairs[0]
	if p.FailedRaw != "" || p.FixedRaw != "" {
		t.Errorf("pair = %+v, want empty raw forms (no Detail on tool events)", p)
	}
	if p.Key != "mcp__trixi__get_nug" || p.Count != 1 {
		t.Errorf("pair = %+v, want key-level count of 1", p)
	}
}

func TestMineMisfires_SkipsCfailAsRepairAnchor_When_FailingSegmentUnknown(t *testing.T) {
	// StatusCFail's failing segment is unknown (compound chain) — it must not
	// anchor a repair pair, mirroring finish()'s rule that cfail neither opens
	// nor closes the retry window.
	cfailEv := ev("s1", event.KindShell, "bash", "", event.StatusCFail, "a && b && c")
	okEv := ev("s1", event.KindShell, "bash", "", event.StatusOK, "a")
	okEv.Retry = true

	rep := MineMisfires([]event.Event{cfailEv, okEv})
	if len(rep.Repairs) != 0 {
		t.Errorf("repairs = %+v, want none — cfail must not anchor a pair", rep.Repairs)
	}
	// cfail still counts toward the fail ranking (mirrors friction.FailedActions).
	if len(rep.Rows) != 1 || rep.Rows[0].Fails != 1 {
		t.Errorf("rows = %+v, want cfail counted as a fail", rep.Rows)
	}
}

func TestMineMisfires_IgnoresRetryWithoutOpenFailure_When_NoPendingMatch(t *testing.T) {
	// Retry=true with no matching pending failure (e.g. the fail fell outside
	// this event slice, or targeted a different key) must not fabricate a pair.
	okEv := ev("s1", event.KindShell, "jq", "", event.StatusOK, "jq '.foo'")
	okEv.Retry = true

	rep := MineMisfires([]event.Event{okEv})
	if len(rep.Repairs) != 0 {
		t.Errorf("repairs = %+v, want none — no failure was pending", rep.Repairs)
	}
}

// TestMineMisfires_SurfacesKnownJQBdShowMotif_When_CorpusReproducesFerret67o
// is the bead's named AC fixture: the jq / `bd show` saga (152 errors across
// 130 transcripts before a manual tools.md fix, ferret-67o) must rank at or
// near the top when the corpus contains it. The live ~/.ferret corpus at the
// time this bead shipped had already absorbed the tools.md fix (only 2
// bd_show fails, 0 jq fails remained — the fix worked), so it no longer
// reproduces the motif; this fixture stands in as the AC's required coverage.
func TestMineMisfires_SurfacesKnownJQBdShowMotif_When_CorpusReproducesFerret67o(t *testing.T) {
	events := make([]event.Event, 0, 132)
	for i := range 130 {
		session := "sess-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		events = append(events, ev(session, event.KindShell, "bd_show", "", event.StatusFail, "bd show x | jq '.[0]'"))
	}
	// A little unrelated noise so the motif has to actually rank, not just
	// be the only row.
	events = append(events,
		ev("noise-1", event.KindShell, "git_status", "", event.StatusFail, "git status --typo"),
		ev("noise-1", event.KindShell, "git_status", "", event.StatusOK, "git status"),
	)

	rep := MineMisfires(events)
	if len(rep.Rows) == 0 {
		t.Fatalf("no rows — motif did not surface")
	}
	top := rep.Rows[0]
	if top.Key != "bd_show" {
		t.Fatalf("top row = %q, want bd_show (the known motif) to lead the ranking", top.Key)
	}
	if top.Fails != 130 {
		t.Errorf("fails = %d, want 130", top.Fails)
	}
}
