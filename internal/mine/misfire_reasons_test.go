package mine

import (
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

func failEv(session, action, errText string) event.Event {
	return event.Event{
		Session: session, Kind: event.KindTool, Action: action,
		Status: event.StatusFail, Err: errText,
	}
}

// TestMineReasons_GroupsHookDenialByScriptName_Not_Message is the bead's
// named AC test: a PreToolUse hook denial groups by the invoked script's
// name, never by its (session-specific, noisy) message text.
func TestMineReasons_GroupsHookDenialByScriptName_Not_Message(t *testing.T) {
	events := []event.Event{
		failEv("s1", "Edit", "PreToolUse:Edit hook error: [bash /path/to/foo.sh …]"),
	}
	rep := MineReasons(events, "")
	if len(rep.Keys) != 1 {
		t.Fatalf("keys = %d, want 1; got %+v", len(rep.Keys), rep.Keys)
	}
	kr := rep.Keys[0]
	if kr.Key != "Edit" {
		t.Fatalf("key = %q, want Edit", kr.Key)
	}
	if len(kr.Reasons) != 1 || kr.Reasons[0].Reason != "hook:foo" {
		t.Fatalf("reasons = %+v, want one row reason=hook:foo", kr.Reasons)
	}
	if kr.Reasons[0].Count != 1 {
		t.Errorf("count = %d, want 1", kr.Reasons[0].Count)
	}
}

// TestMineReasons_GroupsDispatcherHookDenialBySelfReportedGateName covers the
// real-corpus shape: a shared dispatch-pretool.sh runs the actual gate, which
// reports its own name right after the bracket ("]: gate-bd-sync: …") — that
// self-reported name is the useful "script name" here, not the generic router.
func TestMineReasons_GroupsDispatcherHookDenialBySelfReportedGateName(t *testing.T) {
	events := []event.Event{
		failEv("s1", "Bash", "PreToolUse:Bash hook error: [bash /Users/x/.claude/hooks/dispatch-pretool.sh]: gate-bd-sync: refusing this bd write"),
	}
	rep := MineReasons(events, "")
	kr := rep.Keys[0]
	if len(kr.Reasons) != 1 || kr.Reasons[0].Reason != "hook:gate-bd-sync" {
		t.Fatalf("reasons = %+v, want one row reason=hook:gate-bd-sync", kr.Reasons)
	}
}

// TestMineReasons_DistinctRealFailures_Do_Not_Collapse is the bead's other
// named AC test: two real (non-hook) failures with different messages must
// land in different reason buckets, not merge into one.
func TestMineReasons_DistinctRealFailures_Do_Not_Collapse(t *testing.T) {
	events := []event.Event{
		failEv("s1", "Edit", "File has not been read yet. Read it first."),
		failEv("s2", "Edit", "File has been modified since it was last read."),
	}
	rep := MineReasons(events, "")
	kr := rep.Keys[0]
	if len(kr.Reasons) != 2 {
		t.Fatalf("reasons = %+v, want 2 distinct buckets for 2 distinct messages", kr.Reasons)
	}
	if kr.Reasons[0].Reason == kr.Reasons[1].Reason {
		t.Errorf("both reasons collapsed to %q", kr.Reasons[0].Reason)
	}
}

// TestMineReasons_SameMessagePrefixCollapsesIntoOneReason is the positive
// mirror of the above: identical text (post whitespace-collapse) for the same
// key aggregates into one bucket with the right count.
func TestMineReasons_SameMessagePrefixCollapsesIntoOneReason(t *testing.T) {
	events := []event.Event{
		failEv("s1", "Edit", "File has not been read yet."),
		failEv("s2", "Edit", "File   has not been read yet."), // extra whitespace, same collapsed text
		failEv("s3", "Edit", "File has not been read yet."),
	}
	rep := MineReasons(events, "")
	kr := rep.Keys[0]
	if len(kr.Reasons) != 1 {
		t.Fatalf("reasons = %+v, want 1 bucket (identical collapsed text)", kr.Reasons)
	}
	if kr.Reasons[0].Count != 3 {
		t.Errorf("count = %d, want 3", kr.Reasons[0].Count)
	}
}

// TestMineReasons_KeyFilterScopesToOneKey checks --key: only the requested
// misfire key's failures are counted, others are excluded from both the
// per-key breakdown and the coverage totals.
func TestMineReasons_KeyFilterScopesToOneKey(t *testing.T) {
	events := []event.Event{
		failEv("s1", "Edit", "boom edit"),
		failEv("s1", "Read", "boom read"),
	}
	rep := MineReasons(events, "Edit")
	if len(rep.Keys) != 1 || rep.Keys[0].Key != "Edit" {
		t.Fatalf("keys = %+v, want only Edit", rep.Keys)
	}
	if rep.Failed != 1 {
		t.Errorf("failed = %d, want 1 (Read excluded by --key)", rep.Failed)
	}
}

// TestMineReasons_StatesCoverage_When_ErrIsUncaptured pins the bead's Rule:
// a pre-capture corpus (Err == "" on every failed event, decoding an old
// schema-3 row that predates this field) must read as "not captured", which
// means Uncaptured must equal Failed and no reason rows are fabricated.
func TestMineReasons_StatesCoverage_When_ErrIsUncaptured(t *testing.T) {
	events := []event.Event{
		{Session: "s1", Kind: event.KindTool, Action: "Edit", Status: event.StatusFail}, // no Err — pre-capture row
	}
	rep := MineReasons(events, "")
	if rep.Failed != 1 {
		t.Errorf("failed = %d, want 1", rep.Failed)
	}
	if rep.Uncaptured != 1 {
		t.Errorf("uncaptured = %d, want 1 — a failed event with no Err must count as uncaptured, not as zero failures", rep.Uncaptured)
	}
	if len(rep.Keys) != 0 {
		t.Errorf("keys = %+v, want none — no reason text exists to group by", rep.Keys)
	}
}

// TestMineReasons_MixedCoverage_CountsOnlyTheUncapturedShare covers a mixed
// corpus (some rows pre-date Err capture, some don't) so the coverage line
// reports a genuine partial share rather than snapping to all-or-nothing.
func TestMineReasons_MixedCoverage_CountsOnlyTheUncapturedShare(t *testing.T) {
	events := []event.Event{
		failEv("s1", "Edit", "boom"),
		{Session: "s2", Kind: event.KindTool, Action: "Edit", Status: event.StatusFail}, // uncaptured
	}
	rep := MineReasons(events, "")
	if rep.Failed != 2 {
		t.Errorf("failed = %d, want 2", rep.Failed)
	}
	if rep.Uncaptured != 1 {
		t.Errorf("uncaptured = %d, want 1", rep.Uncaptured)
	}
}

// TestMineReasons_LongReasonTruncatesToAbout70Chars pins the Rules' "first
// ~70 chars, whitespace-collapsed" bound so a very long native error message
// doesn't blow the report row out.
func TestMineReasons_LongReasonTruncatesToAbout70Chars(t *testing.T) {
	text := strings.Repeat("word ", 40)
	events := []event.Event{failEv("s1", "Edit", text)}
	rep := MineReasons(events, "")
	got := rep.Keys[0].Reasons[0].Reason
	if len([]rune(got)) > ReasonMax {
		t.Errorf("reason length = %d runes, want <= %d: %q", len([]rune(got)), ReasonMax, got)
	}
}
