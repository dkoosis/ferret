package score

import (
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// --- friction event builders (unique names; prompt/tool/shell live in retrieval_test) ---

func fp(seq int, text string) event.Event {
	return event.Event{Seq: seq, Kind: event.KindPrompt, Prompt: text}
}

// fa is a tool action with a status (ok|fail|cfail|none) and optional retry flag.
func fa(seq int, action, status string, retry bool) event.Event {
	return event.Event{Seq: seq, Kind: event.KindTool, Action: action, Status: status, Retry: retry}
}

// fsh is a shell action (Action = normalized command, e.g. "git_commit").
func fsh(seq int, cmd, status string) event.Event {
	return event.Event{Seq: seq, Kind: event.KindShell, Action: cmd, Status: status}
}

func TestFrictionFailedActions(t *testing.T) {
	cases := []struct {
		name string
		evs  []event.Event
		want int
	}{
		{"no failures", []event.Event{fa(0, "Read", event.StatusOK, false)}, 0},
		{"one fail", []event.Event{fa(0, "Edit", event.StatusFail, false)}, 1},
		{"cfail counts", []event.Event{fsh(0, "git_push", event.StatusCFail)}, 1},
		{"mixed", []event.Event{
			fa(0, "Edit", event.StatusFail, false),
			fa(1, "Edit", event.StatusOK, false),
			fsh(2, "go_build", event.StatusFail),
		}, 2},
		{"none-status is not a failure", []event.Event{fa(0, "Read", event.StatusNone, false)}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeFriction(tc.evs).FailedActions; got != tc.want {
				t.Errorf("FailedActions = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFrictionLatePreconditions(t *testing.T) {
	cases := []struct {
		name string
		evs  []event.Event
		want int
	}{
		{"no retries", []event.Event{fa(0, "Read", event.StatusOK, false)}, 0},
		{"one retry after fail", []event.Event{
			fa(0, "Edit", event.StatusFail, false),
			fa(1, "Read", event.StatusOK, false), // establish the missing precondition
			fa(2, "Edit", event.StatusOK, true),  // re-fire once it holds
		}, 1},
		{"two retries", []event.Event{
			fa(0, "Bash", event.StatusFail, false),
			fa(1, "Bash", event.StatusOK, true),
			fa(2, "Edit", event.StatusFail, false),
			fa(3, "Edit", event.StatusOK, true),
		}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeFriction(tc.evs).LatePreconditions; got != tc.want {
				t.Errorf("LatePreconditions = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFrictionTurnsToGoal(t *testing.T) {
	cases := []struct {
		name        string
		evs         []event.Event
		wantTurns   int
		wantReached bool
	}{
		{"goal on second turn", []event.Event{
			fp(0, "add a feature"),
			fa(1, "Edit", event.StatusOK, false),
			fp(2, "now ship it"),
			fsh(3, "git_commit", event.StatusOK),
		}, 2, true},
		{"goal never reached", []event.Event{
			fp(0, "do a thing"),
			fa(1, "Read", event.StatusOK, false),
			fp(2, "and another"),
		}, 2, false},
		{"commit before any prompt", []event.Event{
			fsh(0, "git_commit", event.StatusOK),
		}, 0, true},
		{"read-only git is not a goal", []event.Event{
			fp(0, "check status"),
			fsh(1, "git_status", event.StatusOK),
		}, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := ComputeFriction(tc.evs)
			if fr.TurnsToGoal != tc.wantTurns || fr.GoalReached != tc.wantReached {
				t.Errorf("TurnsToGoal=%d reached=%v, want %d/%v",
					fr.TurnsToGoal, fr.GoalReached, tc.wantTurns, tc.wantReached)
			}
		})
	}
}

func TestFrictionConfirmationWaste(t *testing.T) {
	cases := []struct {
		name string
		evs  []event.Event
		want int
	}{
		{"bare yes", []event.Event{fp(0, "yes")}, 1},
		{"go ahead", []event.Event{fp(0, "go ahead")}, 1},
		{"trailing punctuation", []event.Event{fp(0, "sure.")}, 1},
		{"substantive turn is not a grant", []event.Event{fp(0, "yes, but also rename the file")}, 0},
		{"continue is a premature-stop, not confirmation-waste", []event.Event{fp(0, "continue")}, 0},
		{"two grants", []event.Event{fp(0, "ok"), fp(1, "proceed")}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeFriction(tc.evs).ConfirmationWaste; got != tc.want {
				t.Errorf("ConfirmationWaste = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFrictionPrematureStops(t *testing.T) {
	cases := []struct {
		name string
		evs  []event.Event
		want int
	}{
		{"continue", []event.Event{fp(0, "continue")}, 1},
		{"keep going", []event.Event{fp(0, "keep going please")}, 1},
		{"finish the rest", []event.Event{fp(0, "finish the rest")}, 1},
		{"do the rest", []event.Event{fp(0, "now do the rest")}, 1},
		{"you stopped early", []event.Event{fp(0, "you stopped before finishing")}, 1},
		{"plain instruction is not a prod", []event.Event{fp(0, "rename the file to foo")}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeFriction(tc.evs).PrematureStops; got != tc.want {
				t.Errorf("PrematureStops = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFrictionIgnoredConstraints(t *testing.T) {
	cases := []struct {
		name string
		evs  []event.Event
		want int
	}{
		{"constraint then repair within window", []event.Event{
			fp(0, "make sure you use tabs not spaces"),
			fp(1, "no, that's wrong — you used spaces"),
		}, 1},
		{"constraint honoured (no later repair)", []event.Event{
			fp(0, "make sure you use tabs"),
			fp(1, "yes perfect thanks"),
		}, 0},
		{"repair beyond the window does not count", []event.Event{
			fp(0, "always add a test"),
			fp(1, "ok"),
			fp(2, "sounds good"),
			fp(3, "looks good"),
			fp(4, "no you forgot the test"),
		}, 0},
		{"no constraint stated", []event.Event{
			fp(0, "add a feature"),
			fp(1, "no, wrong"),
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeFriction(tc.evs).IgnoredConstraints; got != tc.want {
				t.Errorf("IgnoredConstraints = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFrictionRatesAndAny(t *testing.T) {
	fr := ComputeFriction([]event.Event{
		fp(0, "do a thing"),
		fa(1, "Edit", event.StatusFail, false),
		fa(2, "Edit", event.StatusOK, true),
	})
	if fr.Actions != 2 || fr.Prompts != 1 {
		t.Fatalf("denominators: actions=%d prompts=%d, want 2/1", fr.Actions, fr.Prompts)
	}
	if got := fr.FailRate(); got != 0.5 {
		t.Errorf("FailRate = %v, want 0.5", got)
	}
	if got := fr.LatePreconditionRate(); got != 0.5 {
		t.Errorf("LatePreconditionRate = %v, want 0.5", got)
	}
	if !fr.Any() {
		t.Error("Any() = false, want true (session carried failures)")
	}
	// empty stream → no signal, zero-value rates (no divide-by-zero)
	empty := ComputeFriction(nil)
	if empty.Any() || empty.FailRate() != 0 || empty.PrematureStopRate() != 0 {
		t.Errorf("empty stream should be signal-less with zero rates, got %+v", empty)
	}
}
