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

// ClassifyTurns rolls one episode's ordered user turns up into an Outcome: it
// TagMoves each raw turn, then Classify()s the move sequence. This is the
// per-episode binding the segmenter needs — a segment IS an episode, so its
// ordered user turns (the opening prompt plus any folded affirmations) are exactly
// this input. The session-level rollup (cmd/ferret/dialogue.go) is the same
// TagMove→Classify pipeline over a whole transcript; this narrows it to one task,
// so "this routine SUCCEEDS" vs "this loop ends in abandonment" reads at the
// episode grain. PARADISE task-success leg (Walker et al. 1997).
func ClassifyTurns(turns []string) Outcome {
	moves := make([]Move, 0, len(turns))
	for _, t := range turns {
		m, _ := TagMove(t)
		moves = append(moves, m)
	}
	return Classify(moves)
}

// Classify reads an ordered move sequence (one episode's user turns, in turn
// order) into an Outcome. v1 rules, deliberately simple and auditable:
//
//   - ends on Accept, friction ≤ cut          → success
//   - any Accept but friction > cut            → repair-heavy
//   - friction present, no closing Accept      → abandoned
//   - no Accept and no friction                → unknown (no outcome signal)
//
// v2 (bbp.7) widens "friction" past MoveRepair without changing the signature or
// the rules: MoveReject (the Disagree-Correct split of repair) and MoveRepair both
// count via IsRepairMove — so the split is behavior-preserving — and MoveClarify /
// MoveMetaCommunication add to the friction tally too (the human had to ask, or
// steer the agent's communication). The MoveConstrain flag is a LOW-CONFIDENCE
// candidate (Phase D-b) and stays INERT here; MoveNewTask detection (its
// abandonment wiring is bbp.8) and the catalog moves hit the default arm.
//
// "Abandoned by topic switch" (a new task with no accept on the prior one) is a
// cross-episode signal the segmenter sees but a single move slice does not; wiring
// that in via MoveNewTask is the ∇ follow-on (ferret-bbp.8).
func Classify(moves []Move) Outcome {
	var accepts, friction int
	last := MoveNeutral
	for _, m := range moves {
		switch {
		case m == MoveAccept:
			accepts++
			last = m
		case IsRepairMove(m), m == MoveClarify, m == MoveMetaCommunication:
			friction++
			last = m
		default:
			// neutral, the constrain flag, new-task detection, and the catalog moves
			// don't move the outcome needle
		}
	}
	switch {
	case accepts == 0 && friction == 0:
		return OutcomeUnknown
	case last == MoveAccept:
		// closed on acceptance: success unless the friction count is high
		if friction <= repairHeavyCut {
			return OutcomeSuccess
		}
		return OutcomeRepairHeavy
	default:
		// did not close on acceptance (trailing friction or topic switch)
		return OutcomeAbandoned
	}
}
