package dialogue

import "testing"

// TestClassifyRejectSplitIsBehaviorPreserving is the Phase E.1 regression guard
// (bbp.7 P0): the repair→reject split must not silently drop friction. Turns that
// used to tag MoveRepair and now tag MoveReject still count as repairs in Classify,
// so a Reject-only episode reads repair-heavy / abandoned exactly as the equivalent
// Repair-only episode did — not the "unknown / success" a missed default arm would
// yield.
func TestClassifyRejectSplitIsBehaviorPreserving(t *testing.T) {
	tests := []struct {
		name  string
		moves []Move
		want  Outcome
	}{
		{"single reject, no accept is abandoned",
			[]Move{MoveReject}, OutcomeAbandoned},
		{"reject then trailing reject is abandoned",
			[]Move{MoveNeutral, MoveReject, MoveReject}, OutcomeAbandoned},
		{"three rejects then accept is repair-heavy",
			[]Move{MoveReject, MoveReject, MoveReject, MoveAccept}, OutcomeRepairHeavy},
		{"mixed reject+repair count together toward repair-heavy",
			[]Move{MoveReject, MoveRepair, MoveReject, MoveAccept}, OutcomeRepairHeavy},
		{"one reject then accept still success",
			[]Move{MoveReject, MoveAccept}, OutcomeSuccess},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.moves); got != tt.want {
				t.Errorf("Classify(%v) = %q, want %q", tt.moves, got, tt.want)
			}
		})
	}
}

// TestClassifyFrictionMoves proves clarify + meta-communication add to the friction
// tally (a would-be-success episode tips to repair-heavy when they pile up) while
// the constrain flag, new-task detection, and catalog moves stay inert.
func TestClassifyFrictionMoves(t *testing.T) {
	tests := []struct {
		name  string
		moves []Move
		want  Outcome
	}{
		{"clarify+meta before accept tip to repair-heavy",
			[]Move{MoveClarify, MoveMetaCommunication, MoveClarify, MoveAccept}, OutcomeRepairHeavy},
		{"trailing clarify with no accept is abandoned",
			[]Move{MoveNeutral, MoveClarify}, OutcomeAbandoned},
		{"constrain flag is inert (no outcome signal)",
			[]Move{MoveNeutral, MoveConstrain}, OutcomeUnknown},
		{"new-task detection is inert here (bbp.8 wires abandonment)",
			[]Move{MoveNeutral, MoveNewTask}, OutcomeUnknown},
		{"catalog moves stay inert",
			[]Move{MoveDelegateJudgment, MoveInformFYI, MoveStatusCheck}, OutcomeUnknown},
		{"constrain does not block a clean success",
			[]Move{MoveConstrain, MoveAccept}, OutcomeSuccess},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.moves); got != tt.want {
				t.Errorf("Classify(%v) = %q, want %q", tt.moves, got, tt.want)
			}
		})
	}
}
