package gates

import (
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// gateEv is a terse gate-check event builder: a tool_use→tool_result pair is
// already one Event at ingest, so Action is the gate tool and Detail is the
// classified verdict text.
func gateEv(seq int, session, action, detail string) event.Event {
	return event.Event{Seq: seq, Session: session, Kind: event.KindTool, Action: action, Detail: detail}
}

func TestGate(t *testing.T) {
	tests := []struct {
		action   string
		wantGate string
		wantOK   bool
	}{
		{"code-review", GateCodeReview, true},
		{"mcp__reviewer__code-review", GateCodeReview, true},
		{"plan-review", GatePlanReview, true},
		{"precommit", GatePrecommit, true},
		{"QA", GateQA, true},
		{"run-QA-suite", GateQA, true},
		{"Bash", "", false},
		{"Edit", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			g, ok := Gate(tt.action)
			if g != tt.wantGate || ok != tt.wantOK {
				t.Errorf("Gate(%q) = (%q,%v); want (%q,%v)", tt.action, g, ok, tt.wantGate, tt.wantOK)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Decision
	}{
		{"approved", "LGTM, approved — ready to merge", Approved},
		{"approved passed", "all checks passed, no issues", Approved},
		{"needs revision", "changes requested: must fix the nil deref", NeedsRevision},
		{"rejected", "rejected, please revise the error handling", NeedsRevision},
		{"failed", "precommit failed: gofmt diff", NeedsRevision},
		{"escalate beats reject", "rejected and needs a human decision — escalate", Escalate},
		{"escalate blocker", "blocker: cannot proceed without owner sign-off", Escalate},
		{"unknown", "ran the tool, here is some neutral output", Unknown},
		{"empty", "", Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.text); got != tt.want {
				t.Errorf("Classify(%q) = %q; want %q", tt.text, got, tt.want)
			}
		})
	}
}

// TestExtractClassifiesKnownPair is the load-bearing extraction case: a known
// APPROVED and a known NEEDS_REVISION gate check are classified and tallied.
func TestExtractClassifiesKnownPair(t *testing.T) {
	rep := Extract([]event.Event{
		gateEv(0, "s1", "code-review", "approved, looks good"),
		gateEv(1, "s1", "code-review", "changes requested: must fix"),
		gateEv(2, "s1", "Bash", "ls -la"), // not a gate → ignored
	})
	if len(rep.Checks) != 2 {
		t.Fatalf("checks=%d; want 2 (Bash is not a gate)", len(rep.Checks))
	}
	if len(rep.Gates) != 1 {
		t.Fatalf("gates=%d; want 1", len(rep.Gates))
	}
	gs := rep.Gates[0]
	if gs.Gate != GateCodeReview || gs.Checks != 2 || gs.Approved != 1 || gs.Revisions != 1 {
		t.Errorf("gate stat = %+v; want code-review checks=2 approved=1 revisions=1", gs)
	}
	if len(gs.Rejections) != 1 || gs.Rejections[0] != "s1" {
		t.Errorf("rejections=%v; want [s1]", gs.Rejections)
	}
}

// TestOverlapLowComplementaryHighRedundant is the core ω discrimination case:
// two gates rejecting DISJOINT sessions score ω=0 (complementary); two gates
// rejecting the SAME sessions score ω=1 (redundant).
func TestOverlapLowComplementaryHighRedundant(t *testing.T) {
	// Complementary: code-review rejects s1,s2; precommit rejects s3,s4. ω=0.
	complementary := Extract([]event.Event{
		gateEv(0, "s1", "code-review", "rejected"),
		gateEv(1, "s2", "code-review", "rejected"),
		gateEv(2, "s3", "precommit", "failed"),
		gateEv(3, "s4", "precommit", "failed"),
	})
	ov := findOverlap(t, complementary.Overlaps, GateCodeReview, GatePrecommit)
	if ov.Shared != 0 || ov.Union != 4 || ov.Omega != 0 {
		t.Errorf("complementary ω = %+v; want shared=0 union=4 omega=0", ov)
	}

	// Redundant: both gates reject the same two sessions. ω=1.
	redundant := Extract([]event.Event{
		gateEv(0, "s1", "code-review", "rejected"),
		gateEv(1, "s2", "code-review", "rejected"),
		gateEv(2, "s1", "precommit", "failed"),
		gateEv(3, "s2", "precommit", "failed"),
	})
	ov = findOverlap(t, redundant.Overlaps, GateCodeReview, GatePrecommit)
	if ov.Shared != 2 || ov.Union != 2 || ov.Omega != 1 {
		t.Errorf("redundant ω = %+v; want shared=2 union=2 omega=1", ov)
	}
}

// TestOverlapPartial checks the in-between ω: one shared rejection out of three
// distinct sessions → ω = 1/3.
func TestOverlapPartial(t *testing.T) {
	rep := Extract([]event.Event{
		gateEv(0, "s1", "code-review", "rejected"),
		gateEv(1, "s2", "code-review", "rejected"),
		gateEv(2, "s2", "QA", "failed"),
		gateEv(3, "s3", "QA", "failed"),
	})
	ov := findOverlap(t, rep.Overlaps, GateCodeReview, GateQA)
	if ov.Shared != 1 || ov.Union != 3 || !approx(ov.Omega, 1.0/3) {
		t.Errorf("partial ω = %+v; want shared=1 union=3 omega=1/3", ov)
	}
}

// TestOverlapApprovalsDoNotCount guards that ω is over REJECTIONS only — a gate
// that only ever approves a session does not overlap with one that rejects it.
func TestOverlapApprovalsDoNotCount(t *testing.T) {
	rep := Extract([]event.Event{
		gateEv(0, "s1", "code-review", "approved"), // approval, not a rejection
		gateEv(1, "s1", "precommit", "failed"),     // rejection
	})
	// code-review has no rejections → its rejection set is empty → no pair emitted.
	if len(rep.Overlaps) != 0 {
		t.Errorf("overlaps=%v; want none (code-review never rejected)", rep.Overlaps)
	}
}

// TestLoopsSplitAtApprovedBoundary is the confirmed-friction-loop case: a run of
// rejections terminated by an APPROVED is one resolved loop; rejections left
// open at session end are an unresolved loop.
func TestLoopsSplitAtApprovedBoundary(t *testing.T) {
	rep := Extract([]event.Event{
		// s1: two rejections then approve → one resolved loop of 2 revisions.
		gateEv(0, "s1", "code-review", "needs revision"),
		gateEv(1, "s1", "code-review", "still needs revision"),
		gateEv(2, "s1", "code-review", "approved"),
		// s2: reject, (no approval) → one unresolved loop of 1 revision.
		gateEv(3, "s2", "code-review", "rejected"),
	})
	if len(rep.Loops) != 2 {
		t.Fatalf("loops=%d; want 2\n%+v", len(rep.Loops), rep.Loops)
	}
	resolved := findLoop(t, rep.Loops, "s1")
	if resolved.Revisions != 2 || !resolved.Resolved {
		t.Errorf("s1 loop = %+v; want revisions=2 resolved=true", resolved)
	}
	unresolved := findLoop(t, rep.Loops, "s2")
	if unresolved.Revisions != 1 || unresolved.Resolved {
		t.Errorf("s2 loop = %+v; want revisions=1 resolved=false", unresolved)
	}
}

// TestLoopsCleanPassIsNotALoop guards that a gate that approves with no prior
// rejection emits no loop — only friction is a loop.
func TestLoopsCleanPassIsNotALoop(t *testing.T) {
	rep := Extract([]event.Event{
		gateEv(0, "s1", "code-review", "approved"),
	})
	if len(rep.Loops) != 0 {
		t.Errorf("loops=%v; want none (clean pass)", rep.Loops)
	}
}

// TestExtractDeterministic guards the determinism contract: unordered input must
// produce a byte-identical Report across runs (sorted output, no map leakage).
func TestExtractDeterministic(t *testing.T) {
	in := []event.Event{
		gateEv(5, "s2", "precommit", "failed"),
		gateEv(1, "s1", "code-review", "rejected"),
		gateEv(3, "s1", "precommit", "failed"),
		gateEv(2, "s2", "code-review", "approved"),
	}
	first := Extract(in)
	for i := range 5 {
		got := Extract(in)
		if len(got.Checks) != len(first.Checks) || len(got.Gates) != len(first.Gates) ||
			len(got.Overlaps) != len(first.Overlaps) || len(got.Loops) != len(first.Loops) {
			t.Fatalf("run %d: shape drift", i)
		}
		for k := range got.Checks {
			if got.Checks[k] != first.Checks[k] {
				t.Fatalf("run %d check %d: %+v != %+v", i, k, got.Checks[k], first.Checks[k])
			}
		}
		for k := range got.Overlaps {
			if got.Overlaps[k] != first.Overlaps[k] {
				t.Fatalf("run %d overlap %d: %+v != %+v", i, k, got.Overlaps[k], first.Overlaps[k])
			}
		}
	}
}

func findOverlap(t *testing.T, ovs []Overlap, a, b string) Overlap {
	t.Helper()
	for _, ov := range ovs {
		if (ov.A == a && ov.B == b) || (ov.A == b && ov.B == a) {
			return ov
		}
	}
	t.Fatalf("no overlap for (%s,%s) in %+v", a, b, ovs)
	return Overlap{}
}

func findLoop(t *testing.T, loops []Loop, session string) Loop {
	t.Helper()
	for _, l := range loops {
		if l.Session == session {
			return l
		}
	}
	t.Fatalf("no loop for session %s in %+v", session, loops)
	return Loop{}
}

func approx(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	return d < eps && d > -eps
}
