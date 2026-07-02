package score

import (
	"regexp"
	"strings"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/event"
)

// Deterministic architectural-friction telemetry (ferret-bbp.10).
//
// A count/regex-computable layer over the bbp spine (segmentation + per-segment
// Outcome + landmark goal-recognition). It measures how much AVOIDABLE work a
// session spent — failed actions, preconditions discovered mid-act, constraints
// dropped then re-asserted, permission round-trips, agent stops the human had to
// undo — read straight off the event stream with NO LLM and NO ground truth. The
// affect / semantic-judgment metrics (salience, genericity, effect-mismatch,
// interp-fidelity) are NOT here: they need a reader that understands meaning, so
// they belong to the LLM half (ferret-bbp.5) and are deliberately deferred.
//
// Borrowed method + citation (repo convention ferret-cite-algos-in-code):
//   - Precondition / effect frame for the late-precondition + ignored-constraint
//     metrics: STRIPS (Fikes & Nilsson, "STRIPS: A New Approach to the Application
//     of Theorem Proving to Problem Solving", Artificial Intelligence 2(3–4), 1971)
//     and GOAP (Orkin, "Three States and a Plan: The A.I. of F.E.A.R.", GDC 2006).
//     A precondition is a fact that must hold BEFORE an action fires; a late
//     precondition is one surfaced only after the action began (a failure→redo),
//     and an ignored constraint is a stated precondition/effect the plan dropped.
//   - Downstream behaviour as an implicit label (a constraint counts as IGNORED
//     only when a later repair turn contradicts it). Joachims, KDD 2002.
//
// Like the rest of internal/score it is a pure function of the event stream: same
// events → same metrics, byte-stable (doc.go). Callers pass one session's events
// in Seq order (BuildEpisodes' contract); ComputeFriction does not re-sort.

// constraintWindow is how many later user turns an ignored-constraint scan looks
// ahead for a contradicting repair. Small: a constraint the plan dropped is
// contested soon, not many turns later.
const constraintWindow = 3

// prematureStopCues match a user turn that prods the agent to continue / finish /
// do-the-rest — the tell that the agent stopped before the goal was met and the
// human had to restart it. Kept distinct from a bare permission-grant ("yes"):
// "continue" / "finish the rest" are the bead's own premature-stop vocabulary, so
// they classify here and are NOT double-counted as confirmation-waste.
var prematureStopCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bkeep (going|at it)\b`),
	regexp.MustCompile(`(?i)\b(carry on|go on|continue)\b`),
	regexp.MustCompile(`(?i)\bproceed with\b`),
	regexp.MustCompile(`(?i)\bfinish (it|up|the (rest|job|task))\b`),
	regexp.MustCompile(`(?i)\b(do|complete) the (rest|task)\b`),
	regexp.MustCompile(`(?i)\brest of (it|them|the)\b`),
	regexp.MustCompile(`(?i)\bthe (rest|remaining)\b`),
	regexp.MustCompile(`(?i)\bdon'?t stop\b`),
	regexp.MustCompile(`(?i)\b(why did you|you) stop`),
}

// Friction is one session's deterministic friction rollup. Counts, not rates, so
// a cross-session aggregate can SUM them (rates are recovered at render from the
// denominators). Absence is a zero — nothing failed, nothing was re-asserted.
type Friction struct {
	Prompts int `json:"prompts,omitempty"` // genuine user turns — the rate denominator
	Actions int `json:"actions,omitempty"` // tool + shell events — the rate denominator

	// TurnsToGoal is the count of user turns until the first terminal-action goal
	// signal (a commit/push/PR — the same "shipped" tell outcome.go reads). When
	// GoalReached is false the goal was never met in the session and TurnsToGoal
	// holds the total turn count (a lower bound on the real cost).
	TurnsToGoal int  `json:"turnsToGoal,omitempty"`
	GoalReached bool `json:"goalReached,omitempty"`

	FailedActions      int `json:"failedActions,omitempty"`      // tool/shell events with a fail | cfail status
	LatePreconditions  int `json:"latePreconditions,omitempty"`  // retries after a failure — a precondition surfaced mid-act
	IgnoredConstraints int `json:"ignoredConstraints,omitempty"` // a constraint turn contradicted by a later repair (within the window)
	ConfirmationWaste  int `json:"confirmationWaste,omitempty"`  // bare permission-grant turns (a forced "yes" / "go ahead")
	PrematureStops     int `json:"prematureStops,omitempty"`     // turns prodding the agent to continue / finish / do-the-rest
}

// FailRate is failed actions over total actions ∈ [0,1]; 0 when no actions.
func (f Friction) FailRate() float64 { return ratio(f.FailedActions, f.Actions) }

// LatePreconditionRate is retries-after-failure over total actions ∈ [0,1].
func (f Friction) LatePreconditionRate() float64 { return ratio(f.LatePreconditions, f.Actions) }

// PrematureStopRate is continue/finish prods over total user turns ∈ [0,1].
func (f Friction) PrematureStopRate() float64 { return ratio(f.PrematureStops, f.Prompts) }

// Any reports whether the session carried any friction signal — the render gate
// (a signal-less session emits nothing, mirroring reportDialogueNote).
func (f Friction) Any() bool {
	return f.FailedActions > 0 || f.LatePreconditions > 0 || f.IgnoredConstraints > 0 ||
		f.ConfirmationWaste > 0 || f.PrematureStops > 0 || f.GoalReached
}

func ratio(n, d int) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// ComputeFriction reduces one session's ordered events into its friction rollup.
// A single pass tallies the action-side metrics (failures, retries, the terminal
// goal signal) and collects the per-turn dialogue moves; a short second pass over
// the moves scans for constraints a later repair contradicts. Pure: no I/O, no
// time, no map-order — same events → same rollup.
func ComputeFriction(evs []event.Event) Friction {
	var fr Friction
	var moves []dialogue.Move // one per genuine user turn, in order (constraint scan)
	promptCount := 0

	for i := range evs {
		ev := &evs[i]
		switch ev.Kind {
		case event.KindPrompt:
			if m, ok := tallyPrompt(ev, &fr, &promptCount); ok {
				moves = append(moves, m)
			}
		case event.KindTool, event.KindShell:
			tallyAction(ev, &fr, promptCount)
		}
	}
	if !fr.GoalReached {
		fr.TurnsToGoal = fr.Prompts // goal never met — cost is the whole turn count
	}
	fr.IgnoredConstraints = countIgnoredConstraints(moves)
	return fr
}

// tallyPrompt folds one prompt event into fr, advancing the turn counter, and
// returns the turn's dialogue move. ok=false for an empty prompt (skip, no move).
func tallyPrompt(ev *event.Event, fr *Friction, promptCount *int) (dialogue.Move, bool) {
	if ev.Prompt == "" {
		var zero dialogue.Move
		return zero, false
	}
	*promptCount++
	fr.Prompts++
	if !fr.GoalReached {
		fr.TurnsToGoal = *promptCount
	}
	return classifyTurn(ev.Prompt, fr), true
}

// tallyAction folds one tool/shell event into fr's action-side metrics and
// freezes turns-to-goal on the first terminal VCS action.
func tallyAction(ev *event.Event, fr *Friction, promptCount int) {
	fr.Actions++
	if ev.Status == event.StatusFail || ev.Status == event.StatusCFail {
		fr.FailedActions++
	}
	if ev.Retry {
		// A retry of the same action+target after a failure: the agent
		// discovered a missing precondition mid-act and re-fired once it held.
		fr.LatePreconditions++
	}
	if !fr.GoalReached && ev.Kind == event.KindShell && terminalVCS[ev.Action] {
		// First terminal VCS action = the goal shipped; freeze turns-to-goal.
		fr.GoalReached = true
		fr.TurnsToGoal = promptCount
	}
}

// classifyTurn tags one user turn's friction contribution and returns its dialogue
// move (for the constraint scan). Premature-stop is checked BEFORE the bare-affirm
// confirmation-waste gate so a "continue" prod is never also counted as a
// permission round-trip (mutually exclusive per turn).
func classifyTurn(prompt string, fr *Friction) dialogue.Move {
	if matchesAny(prompt, prematureStopCues) {
		fr.PrematureStops++
	} else if isBareAffirmation(prompt) {
		// A whole-turn "yes" / "go ahead": the agent stopped to ask permission and
		// the human had to grant it. The intent-clear / low-risk qualifier is the
		// SEMANTIC half (deferred to bbp.5); the round-trip itself is deterministic.
		fr.ConfirmationWaste++
	}
	m, _ := dialogue.TagMove(prompt)
	return m
}

// countIgnoredConstraints counts constraint turns (MoveConstrain) that a later
// repair/reject turn contradicts within the window — the deterministic proxy for
// "a stated constraint the acted result dropped". Whether the RESULT actually
// honoured the constraint is the semantic check deferred to bbp.5; here the
// downstream repair stands in as the implicit "it was ignored" label (Joachims).
func countIgnoredConstraints(moves []dialogue.Move) int {
	var n int
	for i := range moves {
		if moves[i] != dialogue.MoveConstrain {
			continue
		}
		for j := i + 1; j <= i+constraintWindow && j < len(moves); j++ {
			if dialogue.IsRepairMove(moves[j]) {
				n++
				break
			}
		}
	}
	return n
}

// isBareAffirmation reports whether a whole turn is a closed-set acknowledgement
// (a forced "yes" / "ok" / "go ahead"), reusing the same affirmations set and
// normalization the boundary guard folds on (segment.go) so confirmation-waste
// can never drift from what counts as a bare ack.
func isBareAffirmation(prompt string) bool {
	low := strings.ToLower(strings.TrimSpace(prompt))
	return affirmations[strings.TrimRight(low, " .!,?…")]
}

func matchesAny(s string, res []*regexp.Regexp) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
