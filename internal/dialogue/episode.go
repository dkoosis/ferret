package dialogue

// Episode rolls a session's per-turn moves up into one OUTCOME — the PARADISE
// task-success leg (Walker et al. 1997) that burn and loop-count alone cannot
// supply. An episode is a task: the natural unit is one segment from the
// deterministic segmenter (cmd/ferret/segment.go), so the caller passes the
// ordered moves of a single segment's user turns.

// Outcome is a task's terminal verdict, read from the human's own language.
type Outcome string

const (
	// OutcomeSuccess — the task ended on acceptance with no trailing repair: the
	// human signalled they got what they wanted.
	OutcomeSuccess Outcome = "success"
	// OutcomeRepairHeavy — the human had to correct repeatedly; the task may have
	// landed, but at a friction cost worth surfacing.
	OutcomeRepairHeavy Outcome = "repair-heavy"
	// OutcomeAbandoned — the task ended without acceptance, often on an unresolved
	// repair or a topic switch: the only direct abandonment signal we have.
	OutcomeAbandoned Outcome = "abandoned"
	// OutcomeUnknown — too few signalling turns to call it either way.
	OutcomeUnknown Outcome = "unknown"
)

// repairHeavyCut is the repair count above which a successful-looking episode is
// still flagged repair-heavy. v1 heuristic — tune against dk-validated episodes.
const repairHeavyCut = 2

// Classify reads an ordered move sequence (one episode's user turns, in turn
// order) into an Outcome. v1 rules, deliberately simple and auditable:
//
//   - ends on Accept, repairs ≤ cut          → success
//   - any Accept but repairs > cut            → repair-heavy
//   - repairs present, no closing Accept      → abandoned
//   - no Accept and no Repair                 → unknown (no outcome signal)
//
// "Abandoned by topic switch" (a new neutral task with no accept on the prior
// one) is a cross-episode signal the segmenter sees but a single move slice does
// not; wiring that in is the ∇ follow-on noted in the bead.
func Classify(moves []Move) Outcome {
	var accepts, repairs int
	last := MoveNeutral
	for _, m := range moves {
		switch m {
		case MoveAccept:
			accepts++
			last = m
		case MoveRepair:
			repairs++
			last = m
		default:
			// neutral and reserved v2 moves don't move the outcome needle
		}
	}
	switch {
	case accepts == 0 && repairs == 0:
		return OutcomeUnknown
	case last == MoveAccept && repairs <= repairHeavyCut:
		return OutcomeSuccess
	case accepts > 0:
		return OutcomeRepairHeavy
	default:
		return OutcomeAbandoned
	}
}
